// Package web serves the browser UI and its JSON API. It controls a firewall,
// so every request must carry a secret token and pass Host/Origin checks
// (blocks DNS rebinding and cross-site requests).
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"osui/internal/assistant"
	"osui/internal/hub"
	"osui/internal/llm"
	pb "osui/internal/pb"
)

//go:embed static
var staticFS embed.FS

type Web struct {
	Hub   *hub.Hub
	Token string
	Addr  string // host:port the browser uses
	LLM   *llm.Client

	turning atomic.Bool

	mux *http.ServeMux
}

const cookieName = "osui_token"

func New(h *hub.Hub, token, addr string) *Web {
	w := &Web{Hub: h, Token: token, Addr: addr, mux: http.NewServeMux()}
	sub, _ := fs.Sub(staticFS, "static")
	w.mux.Handle("GET /", http.FileServer(http.FS(sub)))
	w.mux.HandleFunc("GET /api/state", w.state)
	w.mux.HandleFunc("GET /api/events", w.events)
	w.mux.HandleFunc("POST /api/prompts/{id}/answer", w.answer)
	w.mux.HandleFunc("POST /api/rules", w.upsertRule)
	w.mux.HandleFunc("POST /api/rules/{name}/{op}", w.ruleOp)
	w.mux.HandleFunc("DELETE /api/rules/{name}", w.deleteRule)
	w.mux.HandleFunc("GET /api/llm", w.llmGet)
	w.mux.HandleFunc("POST /api/llm", w.llmSet)
	w.mux.HandleFunc("POST /api/assistant/turn", w.assistantTurn)
	return w
}

func (w *Web) Handler() http.Handler { return http.HandlerFunc(w.guard) }

func (w *Web) hostOK(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	_, port, _ := net.SplitHostPort(w.Addr)
	if hp := host; strings.Contains(hp, ":") {
		_, p, _ := net.SplitHostPort(hp)
		if p != port {
			return false
		}
	}
	return h == "127.0.0.1" || h == "localhost" || h == "[::1]" || h == "::1"
}

func (w *Web) guard(rw http.ResponseWriter, r *http.Request) {
	if !w.hostOK(r.Host) {
		http.Error(rw, "bad host", http.StatusForbidden)
		return
	}
	// Token bootstrap: /?token=... sets a cookie, then redirects to a clean URL.
	if t := r.URL.Query().Get("token"); t != "" {
		if tokenEq(t, w.Token) {
			http.SetCookie(rw, &http.Cookie{
				Name: cookieName, Value: t, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(rw, r, "/", http.StatusSeeOther)
			return
		}
		http.Error(rw, "bad token", http.StatusForbidden)
		return
	}
	c, err := r.Cookie(cookieName)
	if err != nil || !tokenEq(c.Value, w.Token) {
		http.Error(rw, "unauthorized: open the URL printed by osui (it contains ?token=...)", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		// Browsers always send Origin on cross-origin and on same-origin POST/DELETE.
		if o := r.Header.Get("Origin"); o == "" || !w.originOK(o) {
			http.Error(rw, "bad origin", http.StatusForbidden)
			return
		}
	}
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
	rw.Header().Set("Cache-Control", "no-store")
	w.mux.ServeHTTP(rw, r)
}

func (w *Web) originOK(o string) bool {
	u, err := url.Parse(o)
	return err == nil && u.Scheme == "http" && w.hostOK(u.Host)
}

func tokenEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// ---- JSON helpers ----

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

func fail(rw http.ResponseWriter, code int, err error) {
	writeJSON(rw, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// ---- view models ----

type promptView struct {
	ID       string   `json:"id"`
	Created  int64    `json:"created"`
	Deadline int64    `json:"deadline"` // unix ms, 0 = none
	Conn     connView `json:"conn"`
}

type connView struct {
	Protocol string   `json:"protocol"`
	SrcIP    string   `json:"srcIp"`
	SrcPort  uint32   `json:"srcPort"`
	DstIP    string   `json:"dstIp"`
	DstHost  string   `json:"dstHost"`
	DstPort  uint32   `json:"dstPort"`
	UID      uint32   `json:"uid"`
	PID      uint32   `json:"pid"`
	Path     string   `json:"path"`
	Cwd      string   `json:"cwd"`
	Args     []string `json:"args"`
}

func viewPrompt(p *hub.Prompt) promptView {
	c := p.Conn
	v := promptView{ID: p.ID, Created: p.Created.UnixMilli(), Conn: connView{
		Protocol: c.Protocol, SrcIP: c.SrcIp, SrcPort: c.SrcPort, DstIP: c.DstIp,
		DstHost: c.DstHost, DstPort: c.DstPort, UID: c.UserId, PID: c.ProcessId,
		Path: c.ProcessPath, Cwd: c.ProcessCwd, Args: c.ProcessArgs,
	}}
	if !p.Deadline.IsZero() {
		v.Deadline = p.Deadline.UnixMilli()
	}
	return v
}

func viewRules(rs []*pb.Rule) []RuleJSON {
	out := make([]RuleJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, ruleToJSON(r))
	}
	return out
}

// ---- handlers ----

func (w *Web) state(rw http.ResponseWriter, _ *http.Request) {
	ps := w.Hub.Prompts()
	pv := make([]promptView, 0, len(ps))
	for _, p := range ps {
		pv = append(pv, viewPrompt(p))
	}
	writeJSON(rw, 200, map[string]any{
		"status":  w.Hub.Status(),
		"rules":   viewRules(w.Hub.Rules()),
		"prompts": pv,
		"log":     w.Hub.Log(),
	})
}

func (w *Web) events(rw http.ResponseWriter, r *http.Request) {
	fl, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "no streaming", 500)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Connection", "keep-alive")
	ch, cancel := w.Hub.Subscribe()
	defer cancel()
	fmt.Fprint(rw, ": hello\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(rw, ": keepalive\n\n")
			fl.Flush()
		case ev := <-ch:
			data := ev.Data
			switch d := ev.Data.(type) {
			case *hub.Prompt:
				data = viewPrompt(d)
			case []*pb.Rule:
				data = viewRules(d)
			}
			b, err := json.Marshal(data)
			if err != nil {
				continue
			}
			fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", ev.Type, b)
			fl.Flush()
		}
	}
}

type answerReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Duration    string `json:"duration"`
	Operator    OpJSON `json:"operator"`
}

func (w *Web) answer(rw http.ResponseWriter, r *http.Request) {
	var a answerReq
	if err := readJSON(r, &a); err != nil {
		fail(rw, 400, err)
		return
	}
	if a.Name == "" {
		a.Name = fmt.Sprintf("%s-%s-%s", a.Action, a.Duration, "rule")
	}
	a.Name = uniqueName(a.Name, w.Hub.RuleExists)
	rule, err := ruleFromJSON(RuleJSON{
		Name: a.Name, Description: a.Description, Enabled: true, Action: a.Action,
		Duration: a.Duration, Operator: a.Operator,
	})
	if err != nil {
		fail(rw, 400, err)
		return
	}
	if err := w.Hub.Answer(r.PathValue("id"), rule); err != nil {
		fail(rw, 410, err)
		return
	}
	writeJSON(rw, 200, map[string]string{"name": rule.Name})
}

func (w *Web) upsertRule(rw http.ResponseWriter, r *http.Request) {
	var j RuleJSON
	if err := readJSON(r, &j); err != nil {
		fail(rw, 400, err)
		return
	}
	rule, err := ruleFromJSON(j)
	if err != nil {
		fail(rw, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := w.Hub.Apply(ctx, pb.Action_CHANGE_RULE, rule); err != nil {
		fail(rw, 502, err)
		return
	}
	writeJSON(rw, 200, ruleToJSON(rule))
}

func (w *Web) findRule(name string) *pb.Rule {
	for _, r := range w.Hub.Rules() {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func (w *Web) ruleOp(rw http.ResponseWriter, r *http.Request) {
	rule := w.findRule(r.PathValue("name"))
	if rule == nil {
		fail(rw, 404, fmt.Errorf("no such rule"))
		return
	}
	var typ pb.Action
	switch r.PathValue("op") {
	case "enable":
		typ = pb.Action_ENABLE_RULE
	case "disable":
		typ = pb.Action_DISABLE_RULE
	default:
		fail(rw, 404, fmt.Errorf("unknown op"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := w.Hub.Apply(ctx, typ, rule); err != nil {
		fail(rw, 502, err)
		return
	}
	writeJSON(rw, 200, map[string]bool{"ok": true})
}

func (w *Web) deleteRule(rw http.ResponseWriter, r *http.Request) {
	rule := w.findRule(r.PathValue("name"))
	if rule == nil {
		fail(rw, 404, fmt.Errorf("no such rule"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := w.Hub.Apply(ctx, pb.Action_DELETE_RULE, rule); err != nil {
		fail(rw, 502, err)
		return
	}
	writeJSON(rw, 200, map[string]bool{"ok": true})
}

// ---- LLM assistant: proposes changes, never applies them ----

func (w *Web) llmGet(rw http.ResponseWriter, _ *http.Request) {
	if w.LLM == nil {
		writeJSON(rw, 200, llm.Public{})
		return
	}
	writeJSON(rw, 200, w.LLM.Public())
}

func (w *Web) llmSet(rw http.ResponseWriter, r *http.Request) {
	if w.LLM == nil {
		fail(rw, 404, fmt.Errorf("LLM support disabled"))
		return
	}
	var in llm.Update
	if err := readJSON(r, &in); err != nil {
		fail(rw, 400, err)
		return
	}
	if err := w.LLM.Set(in); err != nil {
		fail(rw, 400, err)
		return
	}
	writeJSON(rw, 200, w.LLM.Public())
}

// assistantTurn runs one stateless assistant turn. Only one may be in flight:
// the model call is slow and the UI has a single user.
func (w *Web) assistantTurn(rw http.ResponseWriter, r *http.Request) {
	if w.LLM == nil {
		fail(rw, 404, fmt.Errorf("LLM support disabled"))
		return
	}
	var in assistant.Request
	if err := readJSON(r, &in); err != nil {
		fail(rw, 400, err)
		return
	}
	if !w.turning.CompareAndSwap(false, true) {
		fail(rw, 429, fmt.Errorf("another assistant turn is still running"))
		return
	}
	defer w.turning.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	resp, err := (&assistant.Assistant{Src: w.Hub, LLM: w.LLM}).Turn(ctx, in)
	if err != nil {
		var ie assistant.InputError
		if errors.As(err, &ie) {
			fail(rw, 400, err)
			return
		}
		fail(rw, 502, err)
		return
	}
	writeJSON(rw, 200, resp)
}
