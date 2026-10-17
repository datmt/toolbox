package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(code int, content string) *http.Response {
	b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": content}}}})
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}
}

func newClient(t *testing.T, f rt) *Client {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OSUI_LLM_KEY", "")
	c := Load()
	c.HTTP = &http.Client{Transport: f}
	if err := c.Set(Update{URL: "http://llm.test/v1", Model: "m", Key: "sekrit"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChatSendsMessagesAndAuth(t *testing.T) {
	var gotAuth, gotBody string
	c := newClient(t, func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		return reply(200, "hello"), nil
	})
	out, err := c.Chat(context.Background(), []Message{{"system", "S"}, {"user", "U"}}, 0)
	if err != nil || out != "hello" {
		t.Fatalf("%q %v", out, err)
	}
	if gotAuth != "Bearer sekrit" {
		t.Errorf("auth header %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_tokens":800`) || !strings.Contains(gotBody, `"content":"S"`) || !strings.Contains(gotBody, "response_format") {
		t.Errorf("body %s", gotBody)
	}
}

func TestChatRetriesWithoutResponseFormat(t *testing.T) {
	calls := 0
	c := newClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "response_format") {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader("unsupported")), Header: http.Header{}}, nil
		}
		return reply(200, `{}`), nil
	})
	if _, err := c.Chat(context.Background(), []Message{{"user", "x"}}, 0); err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestChatErrors(t *testing.T) {
	c := newClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom")), Header: http.Header{}}, nil
	})
	if _, err := c.Chat(context.Background(), nil, 0); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("want HTTP 500 error, got %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Load().Chat(context.Background(), nil, 0); err == nil {
		t.Error("unconfigured client should error")
	}
}

func TestContextDefaultsDependOnEndpoint(t *testing.T) {
	c := newClient(t, nil) // llm.test = remote
	if got := c.Settings().Context; got != (ContextCfg{}) {
		t.Errorf("remote default should send nothing, got %+v", got)
	}
	_ = c.Set(Update{URL: "http://127.0.0.1:11434/v1", Model: "m"})
	if got := c.Settings().Context; got != (ContextCfg{Rules: true, Daemon: true}) {
		t.Errorf("loopback default: %+v", got)
	}
	// deliberate choice sticks
	_ = c.Set(Update{URL: "http://127.0.0.1:11434/v1", Model: "m", Context: &ContextCfg{Traffic: true}})
	if got := c.Settings().Context; got != (ContextCfg{Traffic: true}) {
		t.Errorf("explicit context lost: %+v", got)
	}
	// switching to a remote endpoint with the toggles untouched resets to the safe default
	same := c.Settings().Context
	_ = c.Set(Update{URL: "https://api.example.com/v1", Model: "m", Context: &same})
	if got := c.Settings().Context; got != (ContextCfg{}) {
		t.Errorf("remote switch should reset context, got %+v", got)
	}
}

func TestSettingsLimits(t *testing.T) {
	c := newClient(t, nil)
	big, huge, neg := 999999, 999, -3
	_ = c.Set(Update{URL: "http://x.test", Model: "m", MaxContextChars: &big, HistoryTurns: &huge})
	if s := c.Settings(); s.MaxContextChars != 60000 || s.HistoryTurns != 16 {
		t.Errorf("not clamped: %+v", s)
	}
	_ = c.Set(Update{URL: "http://x.test", Model: "m", HistoryTurns: &neg})
	if c.Settings().HistoryTurns != 0 {
		t.Error("negative history should clamp to 0")
	}
	long := strings.Repeat("a", MaxInstructions+1)
	if err := c.Set(Update{URL: "http://x.test", Model: "m", Instructions: &long}); err == nil {
		t.Error("oversized instructions accepted")
	}
	ins := "  prefer narrow rules  "
	_ = c.Set(Update{URL: "http://x.test", Model: "m", Instructions: &ins})
	if c.Public().Instructions != "prefer narrow rules" {
		t.Errorf("instructions %q", c.Public().Instructions)
	}
}

func TestConfigSecurity(t *testing.T) {
	c := newClient(t, nil)
	pub, _ := json.Marshal(c.Public())
	if strings.Contains(string(pub), "sekrit") || !c.Public().HasKey {
		t.Errorf("key leaked or missing flag: %s", pub)
	}
	if c.Public().Loopback {
		t.Error("llm.test is not loopback")
	}
	fi, err := os.Stat(c.path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config perms: %v %v", fi, err)
	}
	// blank key on update keeps the old one; clearKey removes it
	_ = c.Set(Update{URL: "http://127.0.0.1:11434/v1/", Model: "m2"})
	if !c.Public().HasKey || !c.Public().Loopback || c.Public().URL != "http://127.0.0.1:11434/v1" {
		t.Errorf("update lost key / bad url: %+v", c.Public())
	}
	_ = c.Set(Update{URL: "http://127.0.0.1:11434/v1", Model: "m2", ClearKey: true})
	if c.Public().HasKey {
		t.Error("clearKey failed")
	}
	for _, bad := range []string{"file:///etc/passwd", "ftp://x", "not a url", ""} {
		if err := c.Set(Update{URL: bad, Model: "m"}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := c.Set(Update{URL: "http://x.test"}); err == nil {
		t.Error("accepted empty model")
	}
	// env key overrides and never lands on disk
	t.Setenv("OSUI_LLM_KEY", "from-env")
	c2 := Load()
	_ = c2.Set(Update{URL: "http://x.test/v1", Model: "m"})
	b, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "osui", "llm.json"))
	if strings.Contains(string(b), "from-env") {
		t.Error("env key written to disk")
	}
}
