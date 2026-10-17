// Package llm holds the LLM endpoint settings and a minimal OpenAI-compatible
// chat transport (Ollama, llama.cpp, vLLM, OpenAI, ...). It knows nothing about
// firewall rules: prompt building and output validation live in
// internal/assistant, and the user must always click Apply.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MaxInstructions  = 2000
	DefaultMaxChars  = 12000
	minMaxChars      = 2000
	maxMaxChars      = 60000
	DefaultHistory   = 8
	maxHistoryTurns  = 16
	defaultMaxTokens = 800
)

// ContextCfg selects which categories of local state may be sent to the model.
type ContextCfg struct {
	Rules   bool `json:"rules"`
	Daemon  bool `json:"daemon"`
	Traffic bool `json:"traffic"`
}

// DefaultContext is safe by default: nothing leaves the machine unless the
// endpoint is local, and even then recent traffic stays out.
func DefaultContext(loopback bool) ContextCfg {
	if loopback {
		return ContextCfg{Rules: true, Daemon: true}
	}
	return ContextCfg{}
}

// Config is persisted at <user config dir>/osui/llm.json (mode 0600).
type Config struct {
	URL             string      `json:"url"`   // base URL, e.g. http://127.0.0.1:11434/v1
	Model           string      `json:"model"` // e.g. qwen2.5:7b
	Key             string      `json:"key"`   // optional bearer token; never sent to the browser
	Instructions    string      `json:"instructions"`
	Context         *ContextCfg `json:"context,omitempty"` // nil = endpoint-dependent defaults
	MaxContextChars int         `json:"maxContextChars,omitempty"`
	HistoryTurns    *int        `json:"historyTurns,omitempty"`
	Inspector       bool        `json:"inspector"`
}

// Settings is a resolved copy of the config without the key.
type Settings struct {
	URL             string
	Model           string
	Instructions    string
	Context         ContextCfg
	MaxContextChars int
	HistoryTurns    int
	Inspector       bool
	Loopback        bool
}

// Public is what the browser may see.
type Public struct {
	URL             string     `json:"url"`
	Model           string     `json:"model"`
	HasKey          bool       `json:"hasKey"`
	Loopback        bool       `json:"loopback"`
	Instructions    string     `json:"instructions"`
	Context         ContextCfg `json:"context"`
	MaxContextChars int        `json:"maxContextChars"`
	HistoryTurns    int        `json:"historyTurns"`
	Inspector       bool       `json:"inspector"`
}

// Message is one chat message sent to the endpoint.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	mu   sync.Mutex
	cfg  Config
	path string
	HTTP *http.Client
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "osui", "llm.json"), nil
}

// Load reads the saved config (missing file = unconfigured). OSUI_LLM_KEY, if
// set, overrides the stored key so it never has to touch disk.
func Load() *Client {
	c := &Client{HTTP: &http.Client{Timeout: 90 * time.Second}}
	if p, err := configPath(); err == nil {
		c.path = p
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &c.cfg)
		}
	}
	if k := os.Getenv("OSUI_LLM_KEY"); k != "" {
		c.cfg.Key = k
	}
	return c
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func resolve(cfg Config) Settings {
	lb := isLoopback(cfg.URL)
	s := Settings{
		URL: cfg.URL, Model: cfg.Model, Instructions: cfg.Instructions, Inspector: cfg.Inspector,
		Loopback: lb, Context: DefaultContext(lb), MaxContextChars: DefaultMaxChars, HistoryTurns: DefaultHistory,
	}
	if cfg.Context != nil {
		s.Context = *cfg.Context
	}
	if cfg.MaxContextChars != 0 {
		s.MaxContextChars = clamp(cfg.MaxContextChars, minMaxChars, maxMaxChars)
	}
	if cfg.HistoryTurns != nil {
		s.HistoryTurns = clamp(*cfg.HistoryTurns, 0, maxHistoryTurns)
	}
	return s
}

// Settings returns the resolved settings (never the key).
func (c *Client) Settings() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return resolve(c.cfg)
}

func (c *Client) Public() Public {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := resolve(c.cfg)
	return Public{URL: s.URL, Model: s.Model, HasKey: c.cfg.Key != "", Loopback: s.Loopback,
		Instructions: s.Instructions, Context: s.Context, MaxContextChars: s.MaxContextChars,
		HistoryTurns: s.HistoryTurns, Inspector: s.Inspector}
}

func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// Update is a settings change. Nil optional fields leave the stored value alone.
type Update struct {
	URL             string      `json:"url"`
	Model           string      `json:"model"`
	Key             string      `json:"key"`
	ClearKey        bool        `json:"clearKey"`
	Instructions    *string     `json:"instructions"`
	Context         *ContextCfg `json:"context"`
	MaxContextChars *int        `json:"maxContextChars"`
	HistoryTurns    *int        `json:"historyTurns"`
	Inspector       *bool       `json:"inspector"`
}

// Set validates and saves new settings. A blank key keeps the stored one (the
// browser never knows it); ClearKey removes it. When the endpoint changes
// between local and remote and the caller did not deliberately change the
// context toggles, they reset to that endpoint's safe defaults.
func (c *Client) Set(u Update) error {
	pu, err := url.Parse(strings.TrimSpace(u.URL))
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return errors.New("url must be http(s)://host[:port]/path")
	}
	if strings.TrimSpace(u.Model) == "" {
		return errors.New("model is required")
	}
	if u.Instructions != nil && len([]rune(*u.Instructions)) > MaxInstructions {
		return fmt.Errorf("instructions are limited to %d characters", MaxInstructions)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := resolve(c.cfg)
	c.cfg.URL = strings.TrimRight(pu.String(), "/")
	c.cfg.Model = strings.TrimSpace(u.Model)
	if u.ClearKey {
		c.cfg.Key = ""
	} else if u.Key != "" {
		c.cfg.Key = u.Key
	}
	if u.Instructions != nil {
		c.cfg.Instructions = strings.TrimSpace(*u.Instructions)
	}
	lb := isLoopback(c.cfg.URL)
	switch {
	case u.Context == nil && c.cfg.Context != nil && lb != prev.Loopback:
		c.cfg.Context = nil
	case u.Context != nil && lb != prev.Loopback && *u.Context == prev.Context:
		c.cfg.Context = nil // toggles untouched across a local/remote switch: use the new defaults
	case u.Context != nil:
		ctx := *u.Context
		c.cfg.Context = &ctx
	}
	if u.MaxContextChars != nil {
		c.cfg.MaxContextChars = clamp(*u.MaxContextChars, minMaxChars, maxMaxChars)
	}
	if u.HistoryTurns != nil {
		n := clamp(*u.HistoryTurns, 0, maxHistoryTurns)
		c.cfg.HistoryTurns = &n
	}
	if u.Inspector != nil {
		c.cfg.Inspector = *u.Inspector
	}
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	stored := c.cfg
	if os.Getenv("OSUI_LLM_KEY") != "" {
		stored.Key = "" // env key stays out of the file
	}
	b, _ := json.MarshalIndent(stored, "", "  ")
	return os.WriteFile(c.path, b, 0o600)
}

// Configured reports whether an endpoint and model are set.
func (c *Client) Configured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.URL != "" && c.cfg.Model != ""
}

// Chat sends messages to the endpoint and returns the reply text. It asks for
// JSON output and retries once without response_format on HTTP 400 (endpoints
// that do not support it).
func (c *Client) Chat(ctx context.Context, msgs []Message, maxTokens int) (string, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg.URL == "" || cfg.Model == "" {
		return "", errors.New("no LLM endpoint configured")
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	reply, err := c.chat(ctx, cfg, msgs, maxTokens, true)
	if err != nil && strings.Contains(err.Error(), "HTTP 400") {
		reply, err = c.chat(ctx, cfg, msgs, maxTokens, false)
	}
	return reply, err
}

func (c *Client) chat(ctx context.Context, cfg Config, msgs []Message, maxTokens int, jsonMode bool) (string, error) {
	body := map[string]any{
		"model":       cfg.Model,
		"temperature": 0,
		"max_tokens":  maxTokens,
		"messages":    msgs,
	}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("LLM endpoint: HTTP %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("LLM endpoint returned an unexpected response")
	}
	return out.Choices[0].Message.Content, nil
}
