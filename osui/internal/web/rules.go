package web

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	pb "osui/internal/pb"
	"osui/internal/rules"
)

type (
	OpJSON   = rules.OpJSON
	RuleJSON = rules.RuleJSON
)

var (
	opToJSON   = rules.OpToJSON
	ruleToJSON = rules.RuleToJSON
	flatten    = rules.Flatten
	classify   = rules.Classify
)

func opFromJSON(j OpJSON) *pb.Operator {
	o := &pb.Operator{Type: j.Type, Operand: j.Operand, Data: j.Data, Sensitive: j.Sensitive}
	for _, c := range j.List {
		o.List = append(o.List, opFromJSON(c))
	}
	return o
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
