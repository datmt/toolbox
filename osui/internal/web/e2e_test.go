package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"osui/internal/hub"
	"osui/internal/llm"
	pb "osui/internal/pb"
	"osui/internal/server"
	"osui/internal/web"
)

const token = "0123456789abcdef0123456789abcdef"

type env struct {
	t      *testing.T
	hub    *hub.Hub
	h      http.Handler
	client pb.UIClient
	port   string
	origin string
}

func setup(t *testing.T) *env {
	t.Helper()
	h := hub.New()
	sock := filepath.Join(t.TempDir(), "osui.sock")
	l, err := server.Listen("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	g := server.Serve(l, &server.Server{Hub: h})
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	// The HTTP handler is driven in-process (no TCP) so the test never opens
	// sockets that OpenSnitch would intercept and prompt for.
	const port = "8899"
	w := web.New(h, token, "127.0.0.1:"+port)
	return &env{t: t, hub: h, h: w.Handler(), client: pb.NewUIClient(conn), port: port, origin: "http://127.0.0.1:" + port}
}

func (e *env) do(method, path string, body any, mod func(*http.Request)) (*http.Response, []byte) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "127.0.0.1:" + e.port
	req.AddCookie(&http.Cookie{Name: "osui_token", Value: token})
	req.Header.Set("Origin", e.origin)
	if mod != nil {
		mod(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Result(), rec.Body.Bytes()
}

// fakeDaemon subscribes and answers notifications like the real daemon would.
func (e *env) fakeDaemon(rules []*pb.Rule) (got chan *pb.Notification) {
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	if _, err := e.client.Subscribe(ctx, &pb.ClientConfig{Name: "fake", Version: "1.7.2", Rules: rules}); err != nil {
		e.t.Fatal(err)
	}
	st, err := e.client.Notifications(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	got = make(chan *pb.Notification, 8)
	go func() {
		for {
			n, err := st.Recv()
			if err != nil {
				return
			}
			got <- n
			_ = st.Send(&pb.NotificationReply{Id: n.Id, Code: pb.NotificationReplyCode_OK})
		}
	}()
	// wait until the hub sees the daemon
	deadline := time.Now().Add(2 * time.Second)
	for !e.hub.Status().Connected {
		if time.Now().After(deadline) {
			e.t.Fatal("daemon never marked connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return got
}

func TestPromptRoundTrip(t *testing.T) {
	e := setup(t)
	e.fakeDaemon(nil)

	type res struct {
		r   *pb.Rule
		err error
	}
	out := make(chan res, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := e.client.AskRule(ctx, &pb.Connection{ProcessPath: "/usr/bin/curl", DstHost: "github.com", DstIp: "1.2.3.4", DstPort: 443, Protocol: "tcp"})
		out <- res{r, err}
	}()

	var id string
	t0 := time.Now()
	defer func() { t.Logf("prompt visible after %v", time.Since(t0)) }()
	for i := 0; i < 300 && id == ""; i++ {
		_, b := e.do("GET", "/api/state", nil, nil)
		var st struct{ Prompts []struct{ ID string } }
		json.Unmarshal(b, &st)
		if len(st.Prompts) > 0 {
			id = st.Prompts[0].ID
		}
		time.Sleep(20 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("prompt never appeared")
	}

	op := map[string]any{"type": "list", "operand": "list", "data": "", "list": []map[string]any{
		{"type": "simple", "operand": "process.path", "data": "/usr/bin/curl"},
		{"type": "simple", "operand": "dest.host", "data": "github.com"},
	}}
	resp, body := e.do("POST", "/api/prompts/"+id+"/answer", map[string]any{
		"name": "allow-always-curl-github.com", "action": "allow", "duration": "always", "operator": op}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answer: %d %s", resp.StatusCode, body)
	}
	r := <-out
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.r.Action != "allow" || r.r.Duration != "always" || r.r.Operator.Type != "list" || len(r.r.Operator.List) != 2 {
		t.Fatalf("bad rule returned to daemon: %v", r.r)
	}

	// answering twice must fail
	resp, _ = e.do("POST", "/api/prompts/"+id+"/answer", map[string]any{"name": "x", "action": "allow", "duration": "once", "operator": op}, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("second answer: want 410 got %d", resp.StatusCode)
	}
}

func TestPromptTimeoutDropsPrompt(t *testing.T) {
	e := setup(t)
	e.fakeDaemon(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := e.client.AskRule(ctx, &pb.Connection{ProcessPath: "/x", DstIp: "1.1.1.1", DstPort: 53}); err == nil {
		t.Fatal("expected deadline error")
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(e.hub.Prompts()); n != 0 {
		t.Fatalf("prompt leaked: %d", n)
	}
}

func TestRuleChangeAndDelete(t *testing.T) {
	e := setup(t)
	existing := &pb.Rule{Name: "allow-always-simple-usr-bin-curl", Enabled: true, Action: "allow", Duration: "always",
		Operator: &pb.Operator{Type: "simple", Operand: "process.path", Data: "/usr/bin/curl"}}
	got := e.fakeDaemon([]*pb.Rule{existing})

	_, b := e.do("GET", "/api/state", nil, nil)
	var st struct{ Rules []struct{ Name, Risk string } }
	json.Unmarshal(b, &st)
	if len(st.Rules) != 1 || st.Rules[0].Risk != "broad" {
		t.Fatalf("expected the curl rule flagged broad, got %+v", st.Rules)
	}

	rj := map[string]any{"name": "curl-allowlist", "enabled": true, "action": "allow", "duration": "always",
		"operator": map[string]any{"type": "regexp", "operand": "dest.host", "data": `^(.*\.)?github\.com$`}}
	resp, body := e.do("POST", "/api/rules", rj, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("upsert: %d %s", resp.StatusCode, body)
	}
	if n := <-got; n.Type != pb.Action_CHANGE_RULE || n.Rules[0].Name != "curl-allowlist" {
		t.Fatalf("daemon got %v", n)
	}

	resp, body = e.do("DELETE", "/api/rules/allow-always-simple-usr-bin-curl", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if n := <-got; n.Type != pb.Action_DELETE_RULE {
		t.Fatalf("daemon got %v", n)
	}
	if rs := e.hub.Rules(); len(rs) != 1 || rs[0].Name != "curl-allowlist" {
		t.Fatalf("cache wrong: %v", rs)
	}

	resp, _ = e.do("POST", "/api/rules/curl-allowlist/disable", nil, nil)
	if resp.StatusCode != 200 || e.hub.Rules()[0].Enabled {
		t.Fatal("disable failed")
	}
}

func TestValidationRejectsBadRules(t *testing.T) {
	e := setup(t)
	e.fakeDaemon(nil)
	bad := []map[string]any{
		{"name": "../evil", "action": "allow", "duration": "always", "operator": map[string]any{"type": "simple", "operand": "process.path", "data": "/x"}},
		{"name": "ok", "action": "maybe", "duration": "always", "operator": map[string]any{"type": "simple", "operand": "process.path", "data": "/x"}},
		{"name": "ok", "action": "allow", "duration": "forever", "operator": map[string]any{"type": "simple", "operand": "process.path", "data": "/x"}},
		{"name": "ok", "action": "allow", "duration": "always", "operator": map[string]any{"type": "regexp", "operand": "dest.host", "data": "("}},
		{"name": "ok", "action": "allow", "duration": "always", "operator": map[string]any{"type": "list", "operand": "list"}},
	}
	for i, b := range bad {
		if resp, body := e.do("POST", "/api/rules", b, nil); resp.StatusCode != 400 {
			t.Errorf("case %d: want 400 got %d %s", i, resp.StatusCode, body)
		}
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	noCookie := func(r *http.Request) { r.Header.Del("Cookie") }
	if resp, _ := e.do("GET", "/api/state", nil, noCookie); resp.StatusCode != 401 {
		t.Errorf("no cookie: want 401 got %d", resp.StatusCode)
	}
	badHost := func(r *http.Request) { r.Host = "evil.example:" + e.port }
	if resp, _ := e.do("GET", "/api/state", nil, badHost); resp.StatusCode != 403 {
		t.Errorf("rebinding host: want 403 got %d", resp.StatusCode)
	}
	badOrigin := func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }
	if resp, _ := e.do("POST", "/api/rules", map[string]any{}, badOrigin); resp.StatusCode != 403 {
		t.Errorf("cross-origin POST: want 403 got %d", resp.StatusCode)
	}
	noOrigin := func(r *http.Request) { r.Header.Del("Origin") }
	if resp, _ := e.do("POST", "/api/rules", map[string]any{}, noOrigin); resp.StatusCode != 403 {
		t.Errorf("POST without Origin: want 403 got %d", resp.StatusCode)
	}
	// token bootstrap
	boot := func(tok string) *http.Response {
		req := httptest.NewRequest("GET", "/?token="+tok, nil)
		req.Host = "127.0.0.1:" + e.port
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Result()
	}
	if resp := boot(token); resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Set-Cookie"), "HttpOnly") {
		t.Errorf("bootstrap: %v", resp.Status)
	}
	if resp := boot("wrong"); resp.StatusCode != 403 {
		t.Errorf("wrong token: want 403 got %d", resp.StatusCode)
	}
	// static page served with the cookie
	if resp, body := e.do("GET", "/", nil, nil); resp.StatusCode != 200 || !bytes.Contains(body, []byte("osui")) {
		t.Errorf("index: %d", resp.StatusCode)
	}
}

type fakeLLM func(*http.Request) (*http.Response, error)

func (f fakeLLM) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAssistantTurnEndpoint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OSUI_LLM_KEY", "")
	h := hub.New()
	w := web.New(h, token, "127.0.0.1:8899")
	w.LLM = llm.Load()
	var lastBody string
	release := make(chan struct{})
	block := false
	started := make(chan struct{}, 1)
	w.LLM.HTTP = &http.Client{Transport: fakeLLM(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		if block {
			started <- struct{}{}
			<-release
		}
		out, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"reply":"hi there","actions":[{"type":"allow_entries","program":"/usr/bin/curl","destinations":["github.com"],"port":"","include_subdomains":true},{"type":"delete_rule","rule":"r9"}]}`}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(out)), Header: http.Header{}}, nil
	})}
	e := &env{t: t, hub: h, h: w.Handler(), port: "8899", origin: "http://127.0.0.1:8899"}
	turn := map[string]any{"message": "curl github only", "inspect": true,
		"history": []map[string]any{{"role": "user", "text": "earlier question"}, {"role": "assistant", "text": "earlier answer", "outcome": "none"}}}

	if resp, _ := e.do("POST", "/api/assistant/turn", turn, nil); resp.StatusCode != 502 {
		t.Errorf("unconfigured: want 502 got %d", resp.StatusCode)
	}
	if resp, body := e.do("POST", "/api/llm", map[string]any{"url": "http://llm.test/v1", "model": "m", "key": "k"}, nil); resp.StatusCode != 200 || bytes.Contains(body, []byte(`"k"`)) {
		t.Fatalf("set: %d %s", resp.StatusCode, body)
	}
	// remote endpoint: nothing injected by default
	_, body := e.do("GET", "/api/llm", nil, nil)
	if !bytes.Contains(body, []byte(`"context":{"rules":false,"daemon":false,"traffic":false}`)) {
		t.Errorf("remote defaults: %s", body)
	}
	resp, body := e.do("POST", "/api/assistant/turn", turn, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("turn: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Reply     string
		Actions   []map[string]any
		Notes     []string
		Inspector map[string]any
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reply != "hi there" || len(out.Actions) != 1 || out.Actions[0]["type"] != "allow_entries" || len(out.Notes) != 1 || out.Inspector == nil {
		t.Errorf("response: %s", body)
	}
	if !strings.Contains(lastBody, "earlier answer") || strings.Contains(lastBody, `"k"`) || strings.Contains(lastBody, "RULES:") {
		t.Errorf("sent body: %s", lastBody)
	}
	// inspector only when asked
	turn["inspect"] = false
	if _, body := e.do("POST", "/api/assistant/turn", turn, nil); bytes.Contains(body, []byte(`"inspector"`)) {
		t.Errorf("inspector present without inspect: %s", body)
	}
	// bad input -> 400
	if resp, _ := e.do("POST", "/api/assistant/turn", map[string]any{"message": ""}, nil); resp.StatusCode != 400 {
		t.Errorf("empty message: want 400 got %d", resp.StatusCode)
	}
	// concurrent turn -> 429
	block = true
	done := make(chan int)
	go func() { r, _ := e.do("POST", "/api/assistant/turn", turn, nil); done <- r.StatusCode }()
	<-started
	if resp, _ := e.do("POST", "/api/assistant/turn", turn, nil); resp.StatusCode != 429 {
		t.Errorf("concurrent turn: want 429 got %d", resp.StatusCode)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Errorf("first turn: %d", code)
	}
	block = false
	// settings round trip, validation and old endpoint removal
	if resp, _ := e.do("POST", "/api/llm", map[string]any{"url": "file:///etc/passwd", "model": "m"}, nil); resp.StatusCode != 400 {
		t.Errorf("bad url: want 400 got %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/llm", map[string]any{"url": "http://llm.test/v1", "model": "m", "instructions": strings.Repeat("a", 2001)}, nil); resp.StatusCode != 400 {
		t.Errorf("long instructions: want 400 got %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/llm/propose", map[string]string{"text": "x"}, nil); resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Errorf("old propose endpoint should be gone, got %d", resp.StatusCode)
	}
	// auth / origin
	if resp, _ := e.do("POST", "/api/assistant/turn", turn, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); resp.StatusCode != 403 {
		t.Errorf("cross-origin turn: want 403 got %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/assistant/turn", turn, func(r *http.Request) { r.Header.Del("Cookie") }); resp.StatusCode != 401 {
		t.Errorf("no token: want 401 got %d", resp.StatusCode)
	}
}
