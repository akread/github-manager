// Package github reads pull request and review request data through the gh
// CLI, and derives the watch status from it.
package github

import (
	"fmt"
	"regexp"
	"strconv"
)

// PullRef identifies one pull request.
type PullRef struct {
	URL    string
	Domain string
	Repo   string // owner/name
	Number int
}

// RepoRef identifies one repository.
type RepoRef struct {
	Domain string
	Repo   string // owner/name
}

var (
	pullURLRe   = regexp.MustCompile(`^https://([^/]+)/([^/]+/[^/]+)/pull/([0-9]+)`)
	repoURLRe   = regexp.MustCompile(`^https://([^/]+)/([^/]+)/([^/]+?)/?$`)
	repoSlashRe = regexp.MustCompile(`^([^/\s]+)/([^/\s]+)$`)
)

// ParsePullURL parses a pull request URL such as
// https://github.com/owner/name/pull/12. Text after the number is ignored.
func ParsePullURL(input string) (PullRef, error) {
	m := pullURLRe.FindStringSubmatch(input)
	if m == nil {
		return PullRef{}, fmt.Errorf("not a pull request url: %q", input)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return PullRef{}, fmt.Errorf("not a pull request url: %q", input)
	}
	return PullRef{
		URL:    fmt.Sprintf("https://%s/%s/pull/%d", m[1], m[2], n),
		Domain: m[1],
		Repo:   m[2],
		Number: n,
	}, nil
}

// ParseRepo parses "owner/name" (on github.com) or a repository URL such as
// https://ghe.example.com/owner/name.
func ParseRepo(input string) (RepoRef, error) {
	if m := repoURLRe.FindStringSubmatch(input); m != nil {
		return RepoRef{Domain: m[1], Repo: m[2] + "/" + m[3]}, nil
	}
	if m := repoSlashRe.FindStringSubmatch(input); m != nil {
		return RepoRef{Domain: "github.com", Repo: m[1] + "/" + m[2]}, nil
	}
	return RepoRef{}, fmt.Errorf("not a repository: %q (use owner/name or a url)", input)
}

// URL returns the web address of the repository.
func (r RepoRef) URL() string { return "https://" + r.Domain + "/" + r.Repo }
