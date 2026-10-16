// Package hub holds all shared state between the gRPC server (talking to the
// OpenSnitch daemon) and the web layer (talking to the browser).
package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pb "osui/internal/pb"
)

// Prompt is a pending connection waiting for a user decision.
type Prompt struct {
	ID       string
	Conn     *pb.Connection
	Created  time.Time
	Deadline time.Time // zero if the daemon set none
	resp     chan *pb.Rule
}

// Event is something pushed to browsers over SSE.
type Event struct {
	Type string
	Data any
}

// LogEntry is one connection decision reported by the daemon.
type LogEntry struct {
	Time     string `json:"time"`
	UnixNano int64  `json:"unixnano"`
	Proto    string `json:"proto"`
	Process  string `json:"process"`
	DstIP    string `json:"dstIp"`
	DstHost  string `json:"dstHost"`
	DstPort  uint32 `json:"dstPort"`
	UID      uint32 `json:"uid"`
	Rule     string `json:"rule"`
	Action   string `json:"action"`
}

// Hub is safe for concurrent use.
type Hub struct {
	mu sync.Mutex

	connected  bool
	clientName string
	version    string
	config     string
	fwRunning  bool

	rules   []*pb.Rule
	prompts map[string]*Prompt
	order   []string
	seq     uint64

	stats   *pb.Statistics
	log     []LogEntry
	logKeys []string
	seen    map[string]struct{}

	notifQ  chan *pb.Notification
	replies map[uint64]chan *pb.NotificationReply
	notifID uint64

	subs map[chan Event]struct{}
}

const maxLog = 1000

func New() *Hub {
	return &Hub{
		prompts: map[string]*Prompt{},
		seen:    map[string]struct{}{},
		replies: map[uint64]chan *pb.NotificationReply{},
		subs:    map[chan Event]struct{}{},
		notifQ:  make(chan *pb.Notification, 16),
	}
}

// ---- pub/sub for browsers ----

func (h *Hub) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// publish must be called without h.mu held.
func (h *Hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow browser; it will resync on reconnect
		}
	}
}

// ---- daemon connection state ----

func (h *Hub) SetSubscribed(cfg *pb.ClientConfig) {
	h.mu.Lock()
	h.connected = true
	h.clientName = cfg.GetName()
	h.version = cfg.GetVersion()
	h.config = cfg.GetConfig()
	h.fwRunning = cfg.GetIsFirewallRunning()
	h.rules = append([]*pb.Rule(nil), cfg.GetRules()...)
	h.mu.Unlock()
	h.publish(Event{"status", h.Status()})
	h.publish(Event{"rules", h.Rules()})
}

func (h *Hub) SetDisconnected() {
	h.mu.Lock()
	h.connected = false
	h.mu.Unlock()
	h.publish(Event{"status", h.Status()})
}

type Status struct {
	Connected  bool   `json:"connected"`
	ClientName string `json:"clientName"`
	Version    string `json:"version"`
	Config     string `json:"config"`
	Firewall   bool   `json:"firewall"`
	Stats      any    `json:"stats"`
}

func (h *Hub) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Status{
		Connected:  h.connected,
		ClientName: h.clientName,
		Version:    h.version,
		Config:     h.config,
		Firewall:   h.fwRunning,
	}
	if st := h.stats; st != nil {
		s.Stats = map[string]uint64{
			"rules": st.Rules, "uptime": st.Uptime, "connections": st.Connections,
			"accepted": st.Accepted, "dropped": st.Dropped, "ignored": st.Ignored,
			"ruleHits": st.RuleHits, "ruleMisses": st.RuleMisses, "dnsResponses": st.DnsResponses,
		}
	}
	return s
}

// ---- stats / log ----

func (h *Hub) RecordStats(st *pb.Statistics) {
	var fresh []LogEntry
	h.mu.Lock()
	h.stats = st
	for _, e := range st.GetEvents() {
		c := e.GetConnection()
		key := fmt.Sprintf("%d|%s|%s|%d|%d", e.GetUnixnano(), c.GetProcessPath(), c.GetDstIp(), c.GetDstPort(), c.GetSrcPort())
		if _, dup := h.seen[key]; dup {
			continue
		}
		h.seen[key] = struct{}{}
		le := LogEntry{
			Time: e.GetTime(), UnixNano: e.GetUnixnano(), Proto: c.GetProtocol(),
			Process: c.GetProcessPath(), DstIP: c.GetDstIp(), DstHost: c.GetDstHost(),
			DstPort: c.GetDstPort(), UID: c.GetUserId(),
			Rule: e.GetRule().GetName(), Action: e.GetRule().GetAction(),
		}
		h.log = append(h.log, le)
		h.logKeys = append(h.logKeys, key)
		fresh = append(fresh, le)
	}
	if over := len(h.log) - maxLog; over > 0 {
		for _, k := range h.logKeys[:over] {
			delete(h.seen, k)
		}
		h.log = append([]LogEntry(nil), h.log[over:]...)
		h.logKeys = append([]string(nil), h.logKeys[over:]...)
	}
	h.mu.Unlock()
	if len(fresh) > 0 {
		h.publish(Event{"log", fresh})
	}
	h.publish(Event{"status", h.Status()})
}

func (h *Hub) Log() []LogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]LogEntry(nil), h.log...)
}

// ---- rules cache ----

func (h *Hub) Rules() []*pb.Rule {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*pb.Rule(nil), h.rules...)
}

func (h *Hub) upsertLocked(r *pb.Rule) {
	for i, old := range h.rules {
		if old.Name == r.Name {
			h.rules[i] = r
			return
		}
	}
	h.rules = append(h.rules, r)
}

func (h *Hub) RuleExists(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

// ---- prompts ----

func (h *Hub) Prompts() []*Prompt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Prompt, 0, len(h.order))
	for _, id := range h.order {
		if p, ok := h.prompts[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Ask blocks until the user answers, ctx ends, or the daemon gives up.
// Returns nil rule + error if no answer arrived.
func (h *Hub) Ask(ctx context.Context, c *pb.Connection) (*pb.Rule, error) {
	h.mu.Lock()
	h.seq++
	p := &Prompt{
		ID:      fmt.Sprintf("p%d", h.seq),
		Conn:    c,
		Created: time.Now(),
		resp:    make(chan *pb.Rule, 1),
	}
	if dl, ok := ctx.Deadline(); ok {
		p.Deadline = dl
	}
	h.prompts[p.ID] = p
	h.order = append(h.order, p.ID)
	h.mu.Unlock()
	h.publish(Event{"prompt", p})

	defer func() {
		h.mu.Lock()
		delete(h.prompts, p.ID)
		for i, id := range h.order {
			if id == p.ID {
				h.order = append(h.order[:i], h.order[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		h.publish(Event{"prompt_gone", p.ID})
	}()

	select {
	case r := <-p.resp:
		if !(r.Duration == "once") {
			h.mu.Lock()
			h.upsertLocked(r)
			h.mu.Unlock()
			h.publish(Event{"rules", h.Rules()})
		}
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var ErrNoPrompt = errors.New("prompt expired or already answered")

func (h *Hub) Answer(id string, r *pb.Rule) error {
	h.mu.Lock()
	p, ok := h.prompts[id]
	h.mu.Unlock()
	if !ok {
		return ErrNoPrompt
	}
	select {
	case p.resp <- r:
		return nil
	default:
		return ErrNoPrompt
	}
}

// ---- notifications to the daemon ----

// NotifyQueue is drained by the gRPC Notifications stream.
func (h *Hub) NotifyQueue() <-chan *pb.Notification { return h.notifQ }

// HandleReply routes a daemon reply to whoever is waiting for it.
func (h *Hub) HandleReply(r *pb.NotificationReply) {
	h.mu.Lock()
	ch := h.replies[r.GetId()]
	delete(h.replies, r.GetId())
	h.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// Send pushes a notification to the daemon and waits for its reply.
func (h *Hub) Send(ctx context.Context, typ pb.Action, rules ...*pb.Rule) error {
	h.mu.Lock()
	if !h.connected {
		h.mu.Unlock()
		return errors.New("daemon not connected")
	}
	h.notifID++
	id := h.notifID
	ch := make(chan *pb.NotificationReply, 1)
	h.replies[id] = ch
	h.mu.Unlock()

	n := &pb.Notification{Id: id, Type: typ, Rules: rules, ServerName: "osui"}
	select {
	case h.notifQ <- n:
	case <-ctx.Done():
		h.dropReply(id)
		return ctx.Err()
	}
	select {
	case r := <-ch:
		if r.Code != pb.NotificationReplyCode_OK {
			return fmt.Errorf("daemon: %s", r.Data)
		}
		return nil
	case <-ctx.Done():
		h.dropReply(id)
		return ctx.Err()
	}
}

func (h *Hub) dropReply(id uint64) {
	h.mu.Lock()
	delete(h.replies, id)
	h.mu.Unlock()
}

// Apply sends a rule change to the daemon and, on success, mirrors it locally.
func (h *Hub) Apply(ctx context.Context, typ pb.Action, r *pb.Rule) error {
	if err := h.Send(ctx, typ, r); err != nil {
		return err
	}
	h.mu.Lock()
	switch typ {
	case pb.Action_DELETE_RULE:
		for i, old := range h.rules {
			if old.Name == r.Name {
				h.rules = append(h.rules[:i], h.rules[i+1:]...)
				break
			}
		}
	case pb.Action_ENABLE_RULE, pb.Action_DISABLE_RULE:
		for _, old := range h.rules {
			if old.Name == r.Name {
				old.Enabled = typ == pb.Action_ENABLE_RULE
			}
		}
	default:
		h.upsertLocked(r)
	}
	h.mu.Unlock()
	h.publish(Event{"rules", h.Rules()})
	return nil
}
