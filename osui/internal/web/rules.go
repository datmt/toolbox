package web

import (
	"fmt"
	"regexp"
	"strings"
	"time"

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

func opToJSON(o *pb.Operator) OpJSON {
	if o == nil {
		return OpJSON{}
	}
	j := OpJSON{Type: o.Type, Operand: o.Operand, Data: o.Data, Sensitive: o.Sensitive}
	for _, c := range o.List {
		j.List = append(j.List, opToJSON(c))
	}
	return j
}

func opFromJSON(j OpJSON) *pb.Operator {
	o := &pb.Operator{Type: j.Type, Operand: j.Operand, Data: j.Data, Sensitive: j.Sensitive}
	for _, c := range j.List {
		o.List = append(o.List, opFromJSON(c))
	}
	return o
}

func ruleToJSON(r *pb.Rule) RuleJSON {
	j := RuleJSON{
		Name: r.Name, Description: r.Description, Enabled: r.Enabled,
		Precedence: r.Precedence, Nolog: r.Nolog, Action: r.Action,
		Duration: r.Duration, Operator: opToJSON(r.Operator), Created: r.Created,
	}
	j.Risk, j.RiskWhy = classify(j)
	return j
}

var (
	validName     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
	validAction   = map[string]bool{"allow": true, "deny": true, "reject": true}
	validDuration = regexp.MustCompile(`^(once|until restart|always|\d+[smhd])$`)
	validType     = map[string]bool{"simple": true, "regexp": true, "network": true, "lists": true, "list": true}
)

func validateOp(o OpJSON, depth int) error {
	if depth > 4 {
		return fmt.Errorf("operator nested too deep")
	}
	if !validType[o.Type] {
		return fmt.Errorf("unknown operator type %q", o.Type)
	}
	if o.Type == "list" {
		if len(o.List) == 0 {
			return fmt.Errorf("list operator is empty")
		}
		for _, c := range o.List {
			if err := validateOp(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if o.Operand == "" {
		return fmt.Errorf("operator missing operand")
	}
	if o.Type == "regexp" {
		if _, err := regexp.Compile(o.Data); err != nil {
			return fmt.Errorf("bad regexp %q: %v", o.Data, err)
		}
	}
	return nil
}

func ruleFromJSON(j RuleJSON) (*pb.Rule, error) {
	if !validName.MatchString(j.Name) {
		return nil, fmt.Errorf("rule name must match %s", validName)
	}
	if !validAction[j.Action] {
		return nil, fmt.Errorf("action must be allow, deny or reject")
	}
	if !validDuration.MatchString(j.Duration) {
		return nil, fmt.Errorf("bad duration %q", j.Duration)
	}
	if err := validateOp(j.Operator, 0); err != nil {
		return nil, err
	}
	created := j.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	return &pb.Rule{
		Created: created, Name: j.Name, Description: j.Description, Enabled: j.Enabled,
		Precedence: j.Precedence, Nolog: j.Nolog, Action: j.Action, Duration: j.Duration,
		Operator: opFromJSON(j.Operator),
	}, nil
}

// flatten returns the leaf operators of an operator tree.
func flatten(o OpJSON) []OpJSON {
	if o.Type == "list" {
		var out []OpJSON
		for _, c := range o.List {
			out = append(out, flatten(c)...)
		}
		return out
	}
	return []OpJSON{o}
}

var matchAll = map[string]bool{"": true, ".*": true, "^.*$": true, ".+": true, "^.+$": true}

// classify rates how much an allow rule lets through.
//
//	broad  - no destination restriction at all (the curl problem)
//	medium - restricts destination only by port or network
//	narrow - restricts by host/ip
//	""     - not an allow rule
func classify(r RuleJSON) (string, string) {
	if r.Action != "allow" {
		return "", ""
	}
	var hasHost, hasPort, hasNet bool
	for _, l := range flatten(r.Operator) {
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

// uniqueName returns base, or base-2, base-3... if taken.
func uniqueName(base string, exists func(string) bool) string {
	base = sanitizeName(base)
	if !exists(base) {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d", base, i)
		if !exists(n) {
			return n
		}
	}
}

var nameJunk = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(s string) string {
	s = strings.Trim(nameJunk.ReplaceAllString(s, "-"), "-")
	if len(s) > 150 {
		s = s[:150]
	}
	if s == "" {
		s = "rule"
	}
	return s
}
