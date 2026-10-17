package assistant

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"osui/internal/rules"
)

const (
	maxActions = 10
	maxDests   = 50
	maxReply   = 1500
)

// Action is a validated proposal. Rule actions carry the per-turn id the model
// used plus the real name, resolved server-side for the preview.
type Action struct {
	Type              string   `json:"type"`
	Program           string   `json:"program,omitempty"`
	Destinations      []string `json:"destinations,omitempty"`
	Port              string   `json:"port,omitempty"`
	IncludeSubdomains bool     `json:"include_subdomains,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"` // set_block_rest
	Rule              string   `json:"rule,omitempty"`
	Name              string   `json:"name,omitempty"`
	Summary           string   `json:"summary,omitempty"`
	Risk              string   `json:"risk,omitempty"`
	RuleEnabled       *bool    `json:"ruleEnabled,omitempty"`
}

var (
	safePath = regexp.MustCompile(`^/[A-Za-z0-9._/+@-]{1,200}$`)
	ruleID   = regexp.MustCompile(`^r\d{1,5}$`)
	domain   = regexp.MustCompile(`^(\*\.)?([A-Za-z0-9-]+\.)+[A-Za-z]{2,}$`)
	v6chars  = regexp.MustCompile(`^[0-9a-fA-F:]+(/\d{1,3})?$`)
	v4chars  = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$`)
)

// flexString accepts a JSON string or number (models often send 443 for "443").
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

type rawAction struct {
	Type              string     `json:"type"`
	Program           string     `json:"program"`
	Destinations      []string   `json:"destinations"`
	Port              flexString `json:"port"`
	IncludeSubdomains bool       `json:"include_subdomains"`
	Enabled           *bool      `json:"enabled"`
	Rule              string     `json:"rule"`
	Name              string     `json:"name"` // only used when re-validating echoed history
}

func validDest(d string) (string, bool) {
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "" || len(d) > 255 {
		return "", false
	}
	switch {
	case strings.Contains(d, ":"):
		if !v6chars.MatchString(d) {
			return "", false
		}
	case v4chars.MatchString(d):
	case domain.MatchString(d):
		return d, true
	default:
		return "", false
	}
	if strings.Contains(d, "/") {
		_, _, err := net.ParseCIDR(d)
		return d, err == nil
	}
	return d, net.ParseIP(d) != nil
}

func validPort(p string) bool {
	if p == "" {
		return true
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && len(p) <= 5
}

// entryRisk reuses the rule classifier on the rule an entry would create.
func entryRisk(dest string) string {
	op := rules.OpJSON{Type: "simple", Operand: "dest.host", Data: dest}
	if strings.Contains(dest, "/") {
		op = rules.OpJSON{Type: "network", Operand: "dest.network", Data: dest}
	} else if net.ParseIP(dest) != nil {
		op.Operand = "dest.ip"
	}
	r, _ := rules.Classify(rules.RuleJSON{Action: "allow", Operator: op})
	return r
}

// resolveRule maps a raw action's rule reference to a shown rule.
type resolveRule func(rawAction) (RuleInfo, bool)

func (snap *Snapshot) byModelID(a rawAction) (RuleInfo, bool) {
	if !ruleID.MatchString(a.Rule) {
		return RuleInfo{}, false
	}
	ri, ok := snap.byID[a.Rule]
	return ri, ok
}

// byEchoedName is for history echoed by the browser: ids are per-turn, so the
// name is looked up against this turn's snapshot instead.
func (snap *Snapshot) byEchoedName(a rawAction) (RuleInfo, bool) {
	id, ok := snap.byName[a.Name]
	if !ok {
		return RuleInfo{}, false
	}
	return snap.byID[id], true
}

// validate checks one raw action against the schema and the snapshot. Invalid
// actions are rejected with a reason, never repaired.
func validate(a rawAction, resolve resolveRule) (Action, string) {
	switch a.Type {
	case "allow_entries":
		if !safePath.MatchString(a.Program) {
			return Action{}, "an allow_entries action had an invalid program path"
		}
		if !validPort(string(a.Port)) {
			return Action{}, "an allow_entries action had an invalid port"
		}
		out := Action{Type: a.Type, Program: a.Program, Port: string(a.Port), IncludeSubdomains: a.IncludeSubdomains}
		seen := map[string]bool{}
		risk := "narrow"
		for _, d := range a.Destinations {
			v, ok := validDest(d)
			if !ok || seen[v] {
				continue
			}
			seen[v] = true
			out.Destinations = append(out.Destinations, v)
			if entryRisk(v) == "medium" {
				risk = "medium"
			}
			if len(out.Destinations) == maxDests {
				break
			}
		}
		if len(out.Destinations) == 0 {
			return Action{}, "an allow_entries action had no valid destinations"
		}
		out.Risk = risk
		return out, ""
	case "set_block_rest":
		if !safePath.MatchString(a.Program) {
			return Action{}, "a set_block_rest action had an invalid program path"
		}
		if a.Enabled == nil {
			return Action{}, "a set_block_rest action had no boolean 'enabled'"
		}
		en := *a.Enabled
		return Action{Type: a.Type, Program: a.Program, Enabled: &en}, ""
	case "enable_rule", "disable_rule", "delete_rule":
		ri, ok := resolve(a)
		if !ok {
			return Action{}, fmt.Sprintf("dropped an action that referenced an unknown rule (%s)", clean(a.Rule+a.Name, 20))
		}
		en := ri.JSON.Enabled
		return Action{Type: a.Type, Rule: ri.ID, Name: ri.Name, Summary: clean(rules.Summarize(ri.JSON.Operator), limSummary),
			Risk: ri.JSON.Risk, RuleEnabled: &en}, ""
	}
	return Action{}, fmt.Sprintf("dropped an action of unknown type %q", clean(a.Type, 30))
}

// Parsed is a validated model reply.
type Parsed struct {
	Reply   string
	Actions []Action
	Notes   []string
}

// ParseReply extracts and strictly validates the model's JSON. If the reply is
// not JSON at all, the raw text becomes a plain answer with no actions.
func ParseReply(raw string, snap *Snapshot) Parsed {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	var obj struct {
		Reply   string            `json:"reply"`
		Actions []json.RawMessage `json:"actions"`
	}
	if i < 0 || j <= i || json.Unmarshal([]byte(s[i:j+1]), &obj) != nil {
		return Parsed{Reply: cleanText(raw, maxReply)}
	}
	p := Parsed{Reply: cleanText(obj.Reply, maxReply)}
	for _, m := range obj.Actions {
		if len(p.Actions) == maxActions {
			p.Notes = append(p.Notes, fmt.Sprintf("ignored actions beyond the first %d", maxActions))
			break
		}
		var ra rawAction
		if err := json.Unmarshal(m, &ra); err != nil {
			p.Notes = append(p.Notes, "dropped a malformed action")
			continue
		}
		a, why := validate(ra, snap.byModelID)
		if why != "" {
			p.Notes = append(p.Notes, why)
			continue
		}
		p.Actions = append(p.Actions, a)
	}
	return p
}

// describe renders an action as short, safe text for history outcome lines.
func describe(a Action) string {
	switch a.Type {
	case "allow_entries":
		port := ""
		if a.Port != "" {
			port = " port " + a.Port
		}
		return fmt.Sprintf("allow_entries %s -> %s%s", a.Program, strings.Join(a.Destinations, ", "), port)
	case "set_block_rest":
		return fmt.Sprintf("set_block_rest %s enabled=%v", a.Program, a.Enabled != nil && *a.Enabled)
	}
	return a.Type + " " + a.Rule
}
