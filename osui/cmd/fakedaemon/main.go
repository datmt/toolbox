// fakedaemon pretends to be opensnitchd so osui can be tried without touching
// the real firewall: it subscribes with sample rules, acknowledges rule
// changes, and fires a connection prompt every few seconds.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "osui/internal/pb"
)

func main() {
	addr := flag.String("addr", "unix:///tmp/osui-demo.sock", "osui gRPC address")
	every := flag.Duration("every", 8*time.Second, "interval between fake prompts")
	flag.Parse()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	c := pb.NewUIClient(conn)
	ctx := context.Background()

	simple := func(op, d string) *pb.Operator { return &pb.Operator{Type: "simple", Operand: op, Data: d} }
	rules := []*pb.Rule{
		{Name: "allow-always-simple-usr-bin-curl", Enabled: true, Action: "allow", Duration: "always", Operator: simple("process.path", "/usr/bin/curl")},
		{Name: "allow-always-simple-usr-bin-git-lfs", Enabled: true, Action: "allow", Duration: "always", Operator: simple("process.path", "/usr/bin/git-lfs")},
		{Name: "deny-always-simple-usr-bin-wget", Enabled: true, Action: "deny", Duration: "always", Operator: simple("process.path", "/usr/bin/wget")},
		{Name: "allow-resolved-nextdns-dot", Enabled: true, Action: "allow", Duration: "always", Precedence: true, Operator: &pb.Operator{
			Type: "list", Operand: "list", List: []*pb.Operator{
				simple("process.path", "/usr/lib/systemd/systemd-resolved"), simple("dest.port", "853"),
				{Type: "regexp", Operand: "dest.ip", Data: `^(45\.90\.(28|30)\.0|2a07:a8c[01]::)$`}}}},
	}
	if _, err := c.Subscribe(ctx, &pb.ClientConfig{Name: "fakedaemon", Version: "demo", Rules: rules}); err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	st, err := c.Notifications(ctx)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			n, err := st.Recv()
			if err != nil {
				log.Fatalf("notifications: %v", err)
			}
			log.Printf("notification %s: %d rule(s)", n.Type, len(n.Rules))
			_ = st.Send(&pb.NotificationReply{Id: n.Id, Code: pb.NotificationReplyCode_OK})
		}
	}()

	hosts := []struct {
		path, host, ip string
		port           uint32
	}{
		{"/usr/bin/curl", "api.github.com", "140.82.121.6", 443},
		{"/usr/lib/firefox/firefox", "www.bbc.co.uk", "151.101.0.81", 443},
		{"/home/jack/.local/bin/uv", "pypi.org", "151.101.64.223", 443},
	}
	for i := 0; ; i++ {
		h := hosts[i%len(hosts)]
		go func() {
			cx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			r, err := c.AskRule(cx, &pb.Connection{Protocol: "tcp", SrcIp: "192.168.1.5", SrcPort: 40000, DstIp: h.ip, DstHost: h.host,
				DstPort: h.port, UserId: 1000, ProcessId: 4242, ProcessPath: h.path, ProcessArgs: []string{h.path, "-sS", "https://" + h.host}})
			if err != nil {
				log.Printf("ask %s: %v (daemon would apply default action)", h.host, err)
				return
			}
			log.Printf("ask %s -> %s %s %s", h.host, r.Action, r.Duration, r.Name)
		}()
		time.Sleep(*every)
	}
}
