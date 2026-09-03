package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Client runs gh commands. The gh CLI supplies the authentication for each
// host, so the tool needs no token of its own.
type Client struct {
	run   func(args ...string) (stdout, stderr []byte, err error)
	mu    sync.Mutex
	users map[string]string
}

// NewClient returns a client that runs the gh binary from PATH.
func NewClient() *Client {
	return &Client{run: runGH, users: map[string]string{}}
}

func runGH(args ...string) ([]byte, []byte, error) {
	cmd := exec.Command("gh", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil, errors.New("gh: command not found; install the GitHub CLI")
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errb.Bytes(), fmt.Errorf("gh %s: %s", strings.Join(args[:min(len(args), 2)], " "), msg)
	}
	return out.Bytes(), errb.Bytes(), nil
}

// api calls a REST path on a host and decodes the JSON response into out.
func (c *Client) api(domain, path string, out any) error {
	stdout, _, err := c.run("api", "--hostname", domain, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Username returns the login of the authenticated user on a host. The value
// is cached for the life of the client.
func (c *Client) Username(domain string) (string, error) {
	c.mu.Lock()
	u, ok := c.users[domain]
	c.mu.Unlock()
	if ok {
		return u, nil
	}
	stdout, _, err := c.run("api", "--hostname", domain, "user", "-q", ".login")
	if err != nil {
		return "", err
	}
	u = strings.TrimSpace(string(stdout))
	if u == "" {
		return "", fmt.Errorf("gh api user: empty login for %s", domain)
	}
	c.mu.Lock()
	c.users[domain] = u
	c.mu.Unlock()
	return u, nil
}

// requiredChecks lists the states of the required checks on a pull request.
// A pull request with no required checks gives an empty list.
func (c *Client) requiredChecks(ref PullRef) ([]rawCheck, error) {
	stdout, stderr, err := c.run("pr", "checks", fmt.Sprint(ref.Number), "--repo", ref.Domain+"/"+ref.Repo, "--required", "--json", "state")
	if err != nil {
		msg := string(stderr) + err.Error()
		if strings.Contains(msg, "no checks reported") || strings.Contains(msg, "no required checks reported") {
			return nil, nil
		}
		// gh exits 8 when checks are pending and 1 when checks fail; the
		// json output is still complete in both cases.
		if len(bytes.TrimSpace(stdout)) == 0 {
			return nil, err
		}
	}
	var checks []rawCheck
	if err := json.Unmarshal(stdout, &checks); err != nil {
		return nil, fmt.Errorf("decode pr checks: %w", err)
	}
	return checks, nil
}
