package assistant

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"osui/internal/hub"
	"osui/internal/llm"
	pb "osui/internal/pb"
	"osui/internal/rules"
)

// Source is the read-only slice of the hub the assistant needs.
type Source interface {
	Rules() []*pb.Rule
	Log() []hub.LogEntry
	Status() hub.Status
}

// RuleInfo is a rule the model was shown, addressable by its per-turn id.
type RuleInfo struct {
	ID   string
	Name string
	JSON rules.RuleJSON
}

// Snapshot is the context text for one turn plus the id table that lets the
// server resolve the model's references. Names never come back from the model.
type Snapshot struct {
	Text   string
	byID   map[string]RuleInfo
	byName map[string]string
}

// Section shares of MaxContextChars; unused shares flow to the rules section.
const (
	shareRules   = 50
	shareDaemon  = 20
	shareTraffic = 30
)

// daemonKeys is the ONLY part of the daemon's config JSON that is ever read
// into a prompt. Server.* (including Authentication) is deliberately absent.
var daemonKeys = []string{"DefaultAction", "DefaultDuration", "InterceptUnknown", "ProcMonitorMethod", "Firewall", "LogLevel"}

func fit(lines []string, budget int) (kept []string, dropped int) {
	used := 0
	for i, l := range lines {
		if used+len(l)+1 > budget {
			return kept, len(lines) - i
		}
		used += len(l) + 1
		kept = append(kept, l)
	}
	return kept, 0
}

// BuildContext assembles the snapshot for one turn from the categories enabled
// in settings. Every value taken from rules, traffic or config is sanitized.
func BuildContext(src Source, s llm.Settings, message string) Snapshot {
	snap := Snapshot{byID: map[string]RuleInfo{}, byName: map[string]string{}}
	total := s.MaxContextChars
	var sections []string

	var daemon, traffic []string
	if s.Context.Daemon {
		daemon, _ = fit(daemonLines(src.Status()), total*shareDaemon/100)
	}
	if s.Context.Traffic {
		b := total * shareTraffic / 100
		if !s.Context.Rules {
			b += total * shareRules / 100
		}
		traffic, _ = fit(trafficLines(src.Log()), b)
	}
	if s.Context.Rules {
		budget := total - lenOf(daemon) - lenOf(traffic)
		sections = append(sections, snap.rulesSection(src.Rules(), message, budget))
	}
	if len(daemon) > 0 {
		sections = append(sections, strings.Join(daemon, "\n"))
	}
	if len(traffic) > 0 {
		sections = append(sections, strings.Join(traffic, "\n"))
	}
	if len(sections) == 0 {
		snap.Text = "(no local state is shared with you; sharing is disabled in the user's settings)"
	} else {
		snap.Text = strings.Join(sections, "\n\n")
	}
	return snap
}

func lenOf(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	return n
}

// ---- rules ----

var noiseTokens = map[string]bool{"com": true, "org": true, "net": true, "www": true, "dest": true, "host": true, "usr": true, "bin": true}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

func relevance(r rules.RuleJSON, msg string) int {
	score := 0
	if p := rules.ProcOf(r.Operator); p != "" {
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		if p = strings.ToLower(p); len(p) >= 2 && strings.Contains(msg, p) {
			score += 2
		}
	}
	for _, l := range rules.Flatten(r.Operator) {
		if !strings.HasPrefix(l.Operand, "dest.") {
			continue
		}
		for _, t := range tokens(l.Data) {
			if len(t) >= 3 && !noiseTokens[t] && strings.Contains(msg, t) {
				score++
			}
		}
	}
	return score
}

func ruleLine(id string, r rules.RuleJSON) string {
	en := "enabled"
	if !r.Enabled {
		en = "disabled"
	}
	prec := "no"
	if r.Precedence {
		prec = "yes"
	}
	risk := r.Risk
	if risk == "" {
		risk = "-"
	}
	parts := []string{id, clean(r.Action, 10), clean(r.Duration, 20), en, "precedence=" + prec, "risk=" + risk}
	if p := rules.ProcOf(r.Operator); p != "" {
		parts = append(parts, "proc="+clean(p, limPath))
	}
	parts = append(parts, "match: "+clean(rules.Summarize(r.Operator), limSummary), "name: "+clean(r.Name, limName))
	if d := clean(r.Description, limDesc); d != "" {
		parts = append(parts, "desc: "+d)
	}
	return strings.Join(parts, " | ")
}

func (snap *Snapshot) rulesSection(pbRules []*pb.Rule, message string, budget int) string {
	all := make([]rules.RuleJSON, 0, len(pbRules))
	for _, r := range pbRules {
		all = append(all, rules.RuleToJSON(r))
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	counts := map[string]int{}
	broad := 0
	for _, r := range all {
		counts[r.Action]++
		if r.Risk == "broad" && r.Enabled {
			broad++
		}
	}
	header := fmt.Sprintf("RULES: %d total; allow %d, deny %d, reject %d; %d enabled allow rule(s) permit any destination (risk=broad)",
		len(all), counts["allow"], counts["deny"], counts["reject"], broad)
	if len(all) == 0 {
		return header
	}

	type cand struct {
		id    string
		r     rules.RuleJSON
		score int
	}
	msg := strings.ToLower(message)
	cands := make([]cand, len(all))
	for i, r := range all {
		cands[i] = cand{fmt.Sprintf("r%d", i+1), r, relevance(r, msg)}
	}
	rank := func(c cand) int {
		if c.r.Risk == "broad" && c.r.Enabled {
			return 1
		}
		return 0
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return rank(cands[i]) > rank(cands[j])
	})

	const marker = "... %d more rules not shown (use a more specific question)"
	used := len(header) + 1 + len(fmt.Sprintf(marker, 99999)) + 1
	lines := []string{header}
	shown := 0
	for _, c := range cands {
		l := ruleLine(c.id, c.r)
		if used+len(l)+1 > budget {
			break
		}
		used += len(l) + 1
		lines = append(lines, l)
		shown++
		snap.byID[c.id] = RuleInfo{ID: c.id, Name: c.r.Name, JSON: c.r}
		snap.byName[c.r.Name] = c.id
	}
	if shown < len(cands) {
		lines = append(lines, fmt.Sprintf(marker, len(cands)-shown))
	}
	return strings.Join(lines, "\n")
}

// ---- daemon ----

func daemonLines(st hub.Status) []string {
	lines := []string{fmt.Sprintf("DAEMON: connected=%v firewall_running=%v version=%s",
		st.Connected, st.Firewall, clean(st.Version, 30))}
	var cfg map[string]any
	if json.Unmarshal([]byte(st.Config), &cfg) != nil {
		return lines
	}
	for _, k := range daemonKeys {
		switch v := cfg[k].(type) {
		case string:
			lines = append(lines, fmt.Sprintf("config.%s = %s", k, clean(v, limMisc)))
		case bool, float64:
			lines = append(lines, fmt.Sprintf("config.%s = %v", k, v))
		}
	}
	return lines
}

// ---- traffic ----

func trafficLines(log []hub.LogEntry) []string {
	if len(log) == 0 {
		return []string{"TRAFFIC: no recent connections"}
	}
	type dest struct {
		key     string
		n       int
		actions map[string]int
	}
	type prog struct {
		path  string
		n     int
		dests map[string]*dest
	}
	progs := map[string]*prog{}
	for _, e := range log {
		p := progs[e.Process]
		if p == nil {
			p = &prog{path: e.Process, dests: map[string]*dest{}}
			progs[e.Process] = p
		}
		p.n++
		host := e.DstHost
		if host == "" {
			host = e.DstIP
		}
		k := fmt.Sprintf("%s:%d", clean(host, limHost), e.DstPort)
		d := p.dests[k]
		if d == nil {
			d = &dest{key: k, actions: map[string]int{}}
			p.dests[k] = d
		}
		d.n++
		d.actions[clean(e.Action, 10)]++
	}
	plist := make([]*prog, 0, len(progs))
	for _, p := range progs {
		plist = append(plist, p)
	}
	sort.Slice(plist, func(i, j int) bool {
		if plist[i].n != plist[j].n {
			return plist[i].n > plist[j].n
		}
		return plist[i].path < plist[j].path
	})
	if len(plist) > 15 {
		plist = plist[:15]
	}
	lines := []string{fmt.Sprintf("TRAFFIC: %d recent connections from %s to %s, top programs:",
		len(log), clean(log[0].Time, 40), clean(log[len(log)-1].Time, 40))}
	for _, p := range plist {
		dl := make([]*dest, 0, len(p.dests))
		for _, d := range p.dests {
			dl = append(dl, d)
		}
		sort.Slice(dl, func(i, j int) bool {
			if dl[i].n != dl[j].n {
				return dl[i].n > dl[j].n
			}
			return dl[i].key < dl[j].key
		})
		if len(dl) > 5 {
			dl = dl[:5]
		}
		var ds []string
		for _, d := range dl {
			var as []string
			for a, n := range d.actions {
				as = append(as, fmt.Sprintf("%s %d", a, n))
			}
			sort.Strings(as)
			ds = append(ds, fmt.Sprintf("%s x%d (%s)", d.key, d.n, strings.Join(as, ", ")))
		}
		name := clean(p.path, limPath)
		if name == "" {
			name = "(unknown)"
		}
		lines = append(lines, fmt.Sprintf("%s | %d conns | %s", name, p.n, strings.Join(ds, "; ")))
	}
	return lines
}
