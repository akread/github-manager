// Package hook runs the external commands from the config. The categorize
// hook takes one review request and returns its priority.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Input is the pull request that ghw passes to the categorize command as
// JSON on stdin.
type Input struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Draft  bool   `json:"draft"`
}

// env returns the GHW_PULL_* variables for the command, so a shell
// one-liner does not need to parse stdin.
func (in Input) env() []string {
	return []string{
		"GHW_PULL_URL=" + in.URL,
		"GHW_PULL_DOMAIN=" + in.Domain,
		"GHW_PULL_REPO=" + in.Repo,
		"GHW_PULL_NUMBER=" + strconv.Itoa(in.Number),
		"GHW_PULL_TITLE=" + in.Title,
		"GHW_PULL_AUTHOR=" + in.Author,
		"GHW_PULL_DRAFT=" + strconv.FormatBool(in.Draft),
	}
}

// Priorities are the accepted values of the priority field, from the most
// to the least important.
var Priorities = []string{"high", "normal", "low"}

// Result is the JSON object the categorize command prints on stdout.
type Result struct {
	// Priority is one of high, normal, or low.
	Priority string `json:"priority"`
	// Category is a short label such as "security" or "docs".
	Category string `json:"category,omitempty"`
	// Summary is one line on why the pull request has this priority.
	Summary string `json:"summary,omitempty"`
}

// maxCategory is the longest category label that the list shows.
const maxCategory = 24

// Categorizer runs the categorize command.
type Categorizer struct {
	// Command is run with `sh -c`.
	Command string
	// Timeout kills the command. Zero means no limit.
	Timeout time.Duration
}

// Run passes the pull request to the command and parses its output.
func (c Categorizer) Run(in Input) (Result, error) {
	ctx := context.Background()
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", c.Command)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), in.env()...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("categorize: timed out after %s", c.Timeout)
	}
	if err != nil {
		msg := lastLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, fmt.Errorf("categorize: %s", msg)
	}
	return ParseResult(stdout.Bytes())
}

// lastLine returns the last non-blank line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ParseResult decodes the command output. The output must hold one JSON
// object; text before the object is ignored, so a command can log to
// stdout before it prints the result. The priority is case-insensitive.
func ParseResult(out []byte) (Result, error) {
	i := bytes.IndexByte(out, '{')
	if i < 0 {
		return Result{}, fmt.Errorf("categorize: no JSON object in output %q", lastLine(string(out)))
	}
	var r Result
	if err := json.Unmarshal(out[i:], &r); err != nil {
		// tolerate trailing text after the object
		dec := json.NewDecoder(bytes.NewReader(out[i:]))
		if err2 := dec.Decode(&r); err2 != nil {
			return Result{}, fmt.Errorf("categorize: decode output: %w", err)
		}
	}
	r.Priority = strings.ToLower(strings.TrimSpace(r.Priority))
	valid := false
	for _, p := range Priorities {
		if r.Priority == p {
			valid = true
		}
	}
	if !valid {
		return Result{}, fmt.Errorf("categorize: priority %q is not one of %s", r.Priority, strings.Join(Priorities, ", "))
	}
	r.Category = oneLine(r.Category)
	if n := []rune(r.Category); len(n) > maxCategory {
		r.Category = string(n[:maxCategory-1]) + "…"
	}
	r.Summary = oneLine(r.Summary)
	return r, nil
}

// oneLine trims s and joins its lines with spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
