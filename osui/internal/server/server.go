// Package server implements the OpenSnitch "UI" gRPC service. The daemon
// connects to us as a client, so we play the role opensnitch-ui normally has.
package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	"osui/internal/hub"
	pb "osui/internal/pb"
)

type Server struct {
	pb.UnimplementedUIServer
	Hub *hub.Hub
	// OnPrompt, if set, is called for each new prompt (desktop notification).
	OnPrompt func(*pb.Connection)
}

func (s *Server) Ping(_ context.Context, r *pb.PingRequest) (*pb.PingReply, error) {
	if st := r.GetStats(); st != nil {
		s.Hub.RecordStats(st)
	}
	return &pb.PingReply{Id: r.GetId()}, nil
}

func (s *Server) AskRule(ctx context.Context, c *pb.Connection) (*pb.Rule, error) {
	if s.OnPrompt != nil {
		go s.OnPrompt(c)
	}
	return s.Hub.Ask(ctx, c)
}

func (s *Server) Subscribe(_ context.Context, cfg *pb.ClientConfig) (*pb.ClientConfig, error) {
	log.Printf("daemon subscribed: %s %s, %d rules", cfg.GetName(), cfg.GetVersion(), len(cfg.GetRules()))
	s.Hub.SetSubscribed(cfg)
	// Echo the config back unchanged: we never override the daemon's settings.
	return cfg, nil
}

func (s *Server) PostAlert(_ context.Context, a *pb.Alert) (*pb.MsgResponse, error) {
	log.Printf("alert: type=%s what=%s text=%q", a.GetType(), a.GetWhat(), a.GetText())
	return &pb.MsgResponse{Id: a.GetId()}, nil
}

func (s *Server) Notifications(stream pb.UI_NotificationsServer) error {
	log.Printf("notification stream open")
	defer s.Hub.SetDisconnected()

	errc := make(chan error, 1)
	go func() {
		for {
			r, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			s.Hub.HandleReply(r)
		}
	}()

	for {
		select {
		case n := <-s.Hub.NotifyQueue():
			if err := stream.Send(n); err != nil {
				return err
			}
		case err := <-errc:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// Listen opens the gRPC socket. addr is "unix:///path" or "host:port".
// It refuses to steal a socket that another UI is still serving.
func Listen(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, errors.New("socket " + path + " is in use (is opensnitch-ui still running?)")
		}
		_ = os.Remove(path) // stale
		l, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		_ = os.Chmod(path, 0o600)
		return l, nil
	}
	return net.Listen("tcp", addr)
}

func Serve(l net.Listener, s *Server) *grpc.Server {
	g := grpc.NewServer()
	pb.RegisterUIServer(g, s)
	go func() {
		if err := g.Serve(l); err != nil {
			log.Printf("grpc: %v", err)
		}
	}()
	return g
}
