// osui: a web-based replacement for opensnitch-ui.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"osui/internal/hub"
	pb "osui/internal/pb"
	"osui/internal/server"
	"osui/internal/web"
)

func main() {
	sock := flag.String("socket", "unix:///tmp/osui.sock", "gRPC address the daemon connects to (Server.Address in default-config.json)")
	listen := flag.String("listen", "127.0.0.1:8899", "web UI listen address (loopback only)")
	notify := flag.Bool("notify", true, "send a desktop notification (notify-send) for new prompts")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		log.Fatalf("--listen must be a loopback address with a port, got %q", *listen)
	}

	token, err := loadToken()
	if err != nil {
		log.Fatalf("token: %v", err)
	}

	h := hub.New()
	srv := &server.Server{Hub: h}
	if *notify {
		if _, err := exec.LookPath("notify-send"); err == nil {
			srv.OnPrompt = func(c *pb.Connection) { desktopNotify(c, *listen) }
		}
	}

	gl, err := server.Listen(*sock)
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}
	g := server.Serve(gl, srv)
	defer g.Stop()

	w := web.New(h, token, *listen)
	hs := &http.Server{Addr: *listen, Handler: w.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("web listen: %v", err)
	}
	go func() {
		if err := hs.Serve(ln); err != http.ErrServerClosed {
			log.Fatalf("web: %v", err)
		}
	}()

	log.Printf("daemon socket: %s", *sock)
	fmt.Printf("\nOpen the UI:  http://%s/?token=%s\n\n", *listen, token)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
	if p, ok := strings.CutPrefix(*sock, "unix://"); ok {
		_ = os.Remove(p)
	}
}

// loadToken keeps a stable per-user token so the bookmark URL survives restarts.
func loadToken() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "osui")
	path := filepath.Join(dir, "token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			return t, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return t, os.WriteFile(path, []byte(t+"\n"), 0o600)
}

func desktopNotify(c *pb.Connection, listen string) {
	dst := c.GetDstHost()
	if dst == "" {
		dst = c.GetDstIp()
	}
	body := fmt.Sprintf("%s → %s:%d", filepath.Base(c.GetProcessPath()), dst, c.GetDstPort())
	_ = exec.Command("notify-send", "-u", "critical", "-a", "osui", "-t", "12000",
		"OpenSnitch: connection needs a decision", body+"\nhttp://"+listen).Run()
}
