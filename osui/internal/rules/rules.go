// Package rules holds the rule JSON model and read-only helpers shared by the
// web layer and the assistant.
package rules

import (
	"strings"

	pb "osui/internal/pb"
)

// OpJSON / RuleJSON mirror the on-disk rule format the daemon uses.
type OpJSON struct {
	Type      string   `json:"type"`
	Operand   string   `json:"operand"`
	Data      string   `json:"data"`
	Sensitive bool     `json:"sensitive"`
	List      []OpJSON `json:"list,omitempty"`
}

type RuleJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Precedence  bool   `json:"precedence"`
	Nolog       bool   `json:"nolog"`
	Action      string `json:"action"`
	Duration    string `json:"duration"`
	Operator    OpJSON `json:"operator"`
	Created     int64  `json:"created"`
	Risk        string `json:"risk,omitempty"` // output only
	RiskWhy     string `json:"riskWhy,omitempty"`
}

func OpToJSON(o *pb.Operator) OpJSON {
	if o == nil {
		return OpJSON{}
	}
	j := OpJSON{Type: o.Type, Operand: o.Operand, Data: o.Data, Sensitive: o.Sensitive}
	for _, c := range o.List {
		j.List = append(j.List, OpToJSON(c))
	}
	return j
}

func RuleToJSON(r *pb.Rule) RuleJSON {
	j := RuleJSON{
		Name: r.Name, Description: r.Description, Enabled: r.Enabled,
		Precedence: r.Precedence, Nolog: r.Nolog, Action: r.Action,
		Duration: r.Duration, Operator: OpToJSON(r.Operator), Created: r.Created,
	}
	j.Risk, j.RiskWhy = Classify(j)
	return j
}

// Flatten returns the leaf operators of an operator tree.
func Flatten(o OpJSON) []OpJSON {
	if o.Type == "list" {
		var out []OpJSON
		for _, c := range o.List {
			out = append(out, Flatten(c)...)
		}
		return out
	}
	return []OpJSON{o}
}

var matchAll = map[string]bool{"": true, ".*": true, "^.*$": true, ".+": true, "^.+$": true}

// Classify rates how much an allow rule lets through.
//
//	broad  - no destination restriction at all (the curl problem)
//	medium - restricts destination only by port or network
//	narrow - restricts by host/ip
//	""     - not an allow rule
func Classify(r RuleJSON) (string, string) {
	if r.Action != "allow" {
		return "", ""
	}
	var hasHost, hasPort, hasNet bool
	for _, l := range Flatten(r.Operator) {
		switch l.Operand {
		case "dest.host", "dest.ip", "lists.domains", "lists.domains_regexp", "lists.ips":
			if l.Type == "regexp" && matchAll[l.Data] {
				continue
			}
			hasHost = true
		case "dest.port":
			hasPort = true
		case "dest.network", "lists.nets":
			hasNet = true
		}
	}
	switch {
	case hasHost:
		return "narrow", ""
	case hasNet:
		return "medium", "limited to a network range"
	case hasPort:
		return "medium", "limited to a port, any host"
	default:
		return "broad", "allows any destination"
	}
}

// Summarize is a one-line description of an operator tree (mirrors the UI).
func Summarize(o OpJSON) string {
	var parts []string
	for _, l := range Flatten(o) {
		op := "="
		switch l.Type {
		case "regexp":
			op = "~"
		case "network":
			op = "∈"
		}
		parts = append(parts, l.Operand+" "+op+" "+l.Data)
	}
	return strings.Join(parts, "  AND  ")
}

// ProcOf returns the process.path a rule is restricted to, or "".
func ProcOf(o OpJSON) string {
	for _, l := range Flatten(o) {
		if l.Operand == "process.path" && l.Type == "simple" {
			return l.Data
		}
	}
	return ""
}
