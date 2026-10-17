// Package runner executes external commands behind an interface so tests
// can assert on commands without running them.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Runner interface {
	// Output runs argv with LC_ALL=C and returns stdout.
	Output(ctx context.Context, argv ...string) ([]byte, error)
}

type System struct{}

func (System) Output(ctx context.Context, argv ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Env = append(c.Environ(), "LC_ALL=C")
	out, err := c.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// Fake returns canned output keyed by the joined argv. For tests.
type Fake struct {
	Out   map[string]string
	Calls [][]string
}

func (f *Fake) Output(_ context.Context, argv ...string) ([]byte, error) {
	f.Calls = append(f.Calls, argv)
	key := ""
	for i, a := range argv {
		if i > 0 {
			key += " "
		}
		key += a
	}
	return []byte(f.Out[key]), nil
}
