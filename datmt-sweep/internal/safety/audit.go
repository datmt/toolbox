package safety

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one audit log line.
type Record struct {
	Time     time.Time `json:"time"`
	Event    string    `json:"event"` // "confirm" | "result"
	Category string    `json:"category"`
	Summary  string    `json:"summary"`
	Items    []string  `json:"items,omitempty"`
	Done     []string  `json:"done,omitempty"`
	Failed   []string  `json:"failed,omitempty"` // "id: reason"
	Error    string    `json:"error,omitempty"`
}

// Audit appends JSON lines to a file. A nil *Audit discards records.
type Audit struct {
	mu   sync.Mutex
	path string
}

// DefaultAuditPath is ~/.local/state/datmt-sweep/actions.log (XDG aware).
func DefaultAuditPath(home string) string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "datmt-sweep", "actions.log")
}

func NewAudit(path string) *Audit { return &Audit{path: path} }

// Log appends r; errors are returned but callers should not block removal on them.
func (a *Audit) Log(r Record) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
