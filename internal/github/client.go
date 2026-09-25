package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

// MergePull merges a pull request. The method is one of merge, squash, or
// rebase. gh pr merge does the work, so the repository settings decide
// whether the method is allowed and whether the branch is deleted.
func (c *Client) MergePull(ref PullRef, method string) error {
	switch method {
	case "merge", "squash", "rebase":
	default:
		return fmt.Errorf("merge %s#%d: unknown method %q", ref.Repo, ref.Number, method)
	}
	_, _, err := c.run("pr", "merge", fmt.Sprint(ref.Number), "--repo", ref.Domain+"/"+ref.Repo, "--"+method)
	return err
}

// pullGraph fetches the GraphQL view of a pull request: the merge queue
// state, the base branch, the legacy branch protection contexts, and the
// check rollup of the head commit. A host whose schema has no merge queue
// fields, such as an older GitHub Enterprise Server, is queried again
// without them.
func (c *Client) pullGraph(ref PullRef) (rawGraph, error) {
	owner, name, ok := strings.Cut(ref.Repo, "/")
	if !ok {
		return rawGraph{}, fmt.Errorf("pull graph: invalid repo %q", ref.Repo)
	}
	const queueFields = "isInMergeQueue isMergeQueueEnabled mergeQueueEntry { state position } "
	build := func(queue bool) string {
		fields := "baseRefName baseRef { branchProtectionRule { requiredStatusCheckContexts } } " +
			"commits(last:1) { nodes { commit { statusCheckRollup { contexts(first:100) { nodes { __typename " +
			fmt.Sprintf("... on StatusContext { context state createdAt isRequired(pullRequestNumber:%d) } ", ref.Number) +
			fmt.Sprintf("... on CheckRun { name status conclusion startedAt isRequired(pullRequestNumber:%d) } ", ref.Number) +
			"} } } } } }"
		if queue {
			fields = queueFields + fields
		}
		return fmt.Sprintf(`{ repository(owner:%q, name:%q) { pullRequest(number:%d) { %s } } }`, owner, name, ref.Number, fields)
	}
	stdout, stderr, err := c.run("api", "graphql", "--hostname", ref.Domain, "-f", "query="+build(true))
	if err != nil && strings.Contains(string(stderr)+err.Error(), "doesn't exist on type") {
		stdout, _, err = c.run("api", "graphql", "--hostname", ref.Domain, "-f", "query="+build(false))
	}
	if err != nil {
		return rawGraph{}, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest rawGraph `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return rawGraph{}, fmt.Errorf("decode pull graph: %w", err)
	}
	return resp.Data.Repository.PullRequest, nil
}

// branchRules lists the status check contexts that the rulesets of a
// repository need on a branch. Legacy branch protection is not part of the
// answer; pullGraph reports it. A host without rulesets gives an empty list.
func (c *Client) branchRules(ref PullRef, branch string) ([]string, error) {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	path := fmt.Sprintf("repos/%s/rules/branches/%s", ref.Repo, url.PathEscape(branch))
	if err := c.api(ref.Domain, path, &rules); err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, ch := range r.Parameters.Checks {
			out = append(out, ch.Context)
		}
	}
	return out, nil
}
