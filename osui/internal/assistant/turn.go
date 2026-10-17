package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"osui/internal/llm"
)

const (
	maxMessage = 1000
	maxHistory = 1000
	maxTokens  = 800
)

// InputError marks a bad request (HTTP 400) as opposed to an endpoint failure.
type InputError string

func (e InputError) Error() string { return string(e) }

type HistoryEntry struct {
	Role    string      `json:"role"` // "user" | "assistant"
	Text    string      `json:"text"`
	Outcome string      `json:"outcome"` // applied | discarded | none
	Actions []rawAction `json:"actions"`
}

type Request struct {
	Message string         `json:"message"`
	History []HistoryEntry `json:"history"`
	Inspect bool           `json:"inspect"`
}

type Inspector struct {
	Version      string        `json:"version"`
	System       string        `json:"system"`
	Context      string        `json:"context"`
	Messages     []llm.Message `json:"messages"`
	ApproxTokens int           `json:"approxTokens"`
}

type Response struct {
	Reply     string     `json:"reply"`
	Actions   []Action   `json:"actions"`
	Notes     []string   `json:"notes"`
	Inspector *Inspector `json:"inspector,omitempty"`
}

type Assistant struct {
	Src Source
	LLM *llm.Client
}

// outcomeLine tells the model what the user did with an earlier proposal. It is
// built from echoed actions that are re-validated like fresh model output, so a
// tampered history cannot say anything a real turn could not.
func outcomeLine(e HistoryEntry, snap *Snapshot) string {
	if e.Outcome != "applied" && e.Outcome != "discarded" {
		return ""
	}
	var parts []string
	for _, ra := range e.Actions {
		if len(parts) == maxActions {
			break
		}
		if a, why := validate(ra, snap.byEchoedName); why == "" {
			parts = append(parts, describe(a))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[user " + e.Outcome + ": " + strings.Join(parts, "; ") + "]"
}

func buildHistory(h []HistoryEntry, keep int, snap *Snapshot) []llm.Message {
	if keep <= 0 {
		return nil
	}
	if len(h) > keep {
		h = h[len(h)-keep:]
	}
	var out []llm.Message
	for _, e := range h {
		switch e.Role {
		case "user":
			if t := truncRunes(strings.TrimSpace(e.Text), maxHistory); t != "" {
				out = append(out, llm.Message{Role: "user", Content: t})
			}
		case "assistant":
			t := clean(e.Text, maxHistory)
			if l := outcomeLine(e, snap); l != "" {
				t = strings.TrimSpace(t + "\n" + l)
			}
			if t != "" {
				out = append(out, llm.Message{Role: "assistant", Content: t})
			}
		}
	}
	return out
}

// Turn runs one stateless assistant turn.
func (a *Assistant) Turn(ctx context.Context, req Request) (*Response, error) {
	if !a.LLM.Configured() {
		return nil, errors.New("no LLM endpoint configured")
	}
	msg := strings.TrimSpace(req.Message)
	if n := utf8.RuneCountInString(msg); n < 1 || n > maxMessage {
		return nil, InputError("message must be 1-1000 characters")
	}
	s := a.LLM.Settings()
	snap := BuildContext(a.Src, s, msg)
	p := BuildPrompt(s, snap, buildHistory(req.History, s.HistoryTurns, &snap), msg, newNonce())

	reply, err := a.LLM.Chat(ctx, p.Messages, maxTokens)
	if err != nil {
		return nil, err
	}
	parsed := ParseReply(reply, &snap)
	resp := &Response{Reply: parsed.Reply, Actions: parsed.Actions, Notes: parsed.Notes}
	if resp.Actions == nil {
		resp.Actions = []Action{}
	}
	if resp.Notes == nil {
		resp.Notes = []string{}
	}
	if req.Inspect {
		b, _ := json.Marshal(p.Messages)
		resp.Inspector = &Inspector{Version: PromptVersion, System: p.System, Context: p.Context,
			Messages: p.Messages[1:], ApproxTokens: len(b) / 4}
	}
	return resp, nil
}
