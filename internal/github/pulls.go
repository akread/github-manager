package github

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

type rawUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type rawPull struct {
	Title          string  `json:"title"`
	User           rawUser `json:"user"`
	State          string  `json:"state"`
	Merged         bool    `json:"merged"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	Draft          bool    `json:"draft"`
}

type rawComment struct {
	User      rawUser   `json:"user"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
}

type rawReview struct {
	User        rawUser    `json:"user"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

type rawRequestedReviewers struct {
	Users []rawUser `json:"users"`
}

type rawCheck struct {
	State string `json:"state"`
}

// pullData is everything fetched for one pull request.
type pullData struct {
	pull           rawPull
	comments       []rawComment
	reviewComments []rawComment
	requested      rawRequestedReviewers
	reviews        []rawReview
	checks         []rawCheck
	username       string
}

// Comment is a new issue comment or review comment on a pull request.
type Comment struct {
	Author    string
	Body      string
	URL       string
	CreatedAt time.Time
}

// Check states, in order of priority.
const (
	CheckNone    = ""
	CheckSuccess = "SUCCESS"
	CheckPending = "PENDING"
	CheckFailure = "FAILURE"
)

// PullStatus is the watch view of one pull request at FetchedAt, relative
// to the since time of the subscription.
type PullStatus struct {
	Ref       PullRef
	Since     time.Time
	FetchedAt time.Time

	Title       string
	Author      string
	Ours        bool // the authenticated user is the author
	State       string
	Merged      bool
	MergeCommit string
	Draft       bool

	Comments        []Comment // new since Since, after filters
	ReviewComments  []Comment // new since Since, after filters
	ReviewRequested bool      // the authenticated user is a requested reviewer

	Approvals           int
	NewApprovals        int
	ChangesRequested    int
	NewChangesRequested int

	CheckState string // one of the Check constants
}

// HasUpdates reports whether the pull request has activity to show.
func (p *PullStatus) HasUpdates() bool {
	return p.State != "open" ||
		len(p.Comments) > 0 ||
		len(p.ReviewComments) > 0 ||
		p.ReviewRequested ||
		p.NewApprovals > 0 ||
		p.NewChangesRequested > 0
}

// MarkSeen moves Since to FetchedAt and clears the new activity, as a
// commit does. The review request flag stays: it reflects the current
// state, not an event.
func (p *PullStatus) MarkSeen() {
	p.Since = p.FetchedAt
	p.Comments = nil
	p.ReviewComments = nil
	p.NewApprovals = 0
	p.NewChangesRequested = 0
}

// LoadPull fetches one pull request and derives its status. Comments from
// the authenticated user, from non-user accounts, and from excluded logins
// are dropped.
func (c *Client) LoadPull(ref PullRef, since time.Time, excluded []string) (*PullStatus, error) {
	fetchedAt := time.Now()
	base := fmt.Sprintf("repos/%s/pulls/%d", ref.Repo, ref.Number)
	sinceParam := since.UTC().Format(time.RFC3339)
	var d pullData
	tasks := []func() error{
		func() error { return c.api(ref.Domain, base, &d.pull) },
		func() error {
			return c.api(ref.Domain, fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100&since=%s", ref.Repo, ref.Number, sinceParam), &d.comments)
		},
		func() error {
			return c.api(ref.Domain, base+"/comments?per_page=100&since="+sinceParam, &d.reviewComments)
		},
		func() error { return c.api(ref.Domain, base+"/requested_reviewers", &d.requested) },
		func() error { return c.api(ref.Domain, base+"/reviews?per_page=100", &d.reviews) },
		func() error {
			checks, err := c.requiredChecks(ref)
			d.checks = checks
			return err
		},
		func() error {
			u, err := c.Username(ref.Domain)
			d.username = u
			return err
		},
	}
	if err := runAll(tasks); err != nil {
		return nil, err
	}
	s := derivePull(ref, d, since, fetchedAt, excluded)
	return &s, nil
}

// runAll runs the tasks at the same time and returns the first error.
func runAll(tasks []func() error) error {
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = t()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func derivePull(ref PullRef, d pullData, since, fetchedAt time.Time, excluded []string) PullStatus {
	keep := func(c rawComment) bool {
		return c.User.Login != d.username && c.User.Type == "User" && !slices.Contains(excluded, c.User.Login)
	}
	toComments := func(in []rawComment) []Comment {
		var out []Comment
		for _, c := range in {
			if !keep(c) {
				continue
			}
			// the since filter of the API has second granularity; drop
			// comments at or before the commit point
			if !c.CreatedAt.After(since) {
				continue
			}
			out = append(out, Comment{Author: c.User.Login, Body: c.Body, URL: c.HTMLURL, CreatedAt: c.CreatedAt})
		}
		return out
	}

	s := PullStatus{
		Ref:            ref,
		Since:          since,
		FetchedAt:      fetchedAt,
		Title:          d.pull.Title,
		Author:         d.pull.User.Login,
		Ours:           d.pull.User.Login == d.username,
		State:          d.pull.State,
		Merged:         d.pull.Merged,
		MergeCommit:    d.pull.MergeCommitSHA,
		Draft:          d.pull.Draft,
		Comments:       toComments(d.comments),
		ReviewComments: toComments(d.reviewComments),
		CheckState:     reduceChecks(d.checks),
	}
	for _, u := range d.requested.Users {
		if u.Login == d.username {
			s.ReviewRequested = true
		}
	}

	var approvals []rawReview
	for _, r := range d.reviews {
		if r.State == "APPROVED" {
			approvals = append(approvals, r)
		}
	}
	for _, r := range d.reviews {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		// a later approval from the same reviewer supersedes the request
		superseded := false
		for _, a := range approvals {
			if a.User.Login == r.User.Login && submitted(a).After(submitted(r)) {
				superseded = true
				break
			}
		}
		if superseded {
			continue
		}
		s.ChangesRequested++
		if submitted(r).After(since) {
			s.NewChangesRequested++
		}
	}
	s.Approvals = len(approvals)
	for _, a := range approvals {
		if submitted(a).After(since) {
			s.NewApprovals++
		}
	}
	return s
}

func submitted(r rawReview) time.Time {
	if r.SubmittedAt == nil {
		return time.Time{}
	}
	return *r.SubmittedAt
}

// reduceChecks folds the required check states into one: a failure wins
// over a pending check, and a pending check wins over success.
func reduceChecks(checks []rawCheck) string {
	if len(checks) == 0 {
		return CheckNone
	}
	state := CheckSuccess
	for _, c := range checks {
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			return CheckFailure
		case "PENDING", "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED", "EXPECTED":
			state = CheckPending
		}
	}
	return state
}
