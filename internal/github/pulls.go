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

// rawEvent is one issue timeline event. The watch reads three kinds:
// reviewed, review_requested, and review_request_removed. The fields that
// a kind does not have stay empty.
type rawEvent struct {
	Event             string     `json:"event"`
	User              rawUser    `json:"user"`               // reviewed
	State             string     `json:"state"`              // reviewed, lowercase
	SubmittedAt       *time.Time `json:"submitted_at"`       // reviewed
	RequestedReviewer *rawUser   `json:"requested_reviewer"` // review_requested, review_request_removed; nil for a team
	CreatedAt         time.Time  `json:"created_at"`         // review_requested, review_request_removed
}

// rawReview is a submitted review, taken from a reviewed event.
type rawReview struct {
	User        rawUser
	State       string // uppercase
	SubmittedAt *time.Time
}

type rawCheck struct {
	State string `json:"state"`
}

// rawQueue is the merge queue view of a pull request, from GraphQL. The
// REST API does not report it.
type rawQueue struct {
	InQueue      bool           `json:"isInMergeQueue"`
	QueueEnabled bool           `json:"isMergeQueueEnabled"` // the base branch has a merge queue
	Entry        *rawQueueEntry `json:"mergeQueueEntry"`     // nil when not in the queue
}

type rawQueueEntry struct {
	State    string `json:"state"` // AWAITING_CHECKS, LOCKED, MERGEABLE, QUEUED, UNMERGEABLE
	Position int    `json:"position"`
}

// pullData is everything fetched for one pull request.
type pullData struct {
	pull           rawPull
	comments       []rawComment
	reviewComments []rawComment
	timeline       []rawEvent
	checks         []rawCheck
	queue          rawQueue
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

	Comments         []Comment // new since Since, after filters
	ReviewComments   []Comment // new since Since, after filters
	ReviewRequested  bool      // the authenticated user is a requested reviewer
	NewReviewRequest bool      // the request was made after Since

	Approvals           int
	NewApprovals        int
	ChangesRequested    int
	NewChangesRequested int

	CheckState string // one of the Check constants

	InMergeQueue  bool   // the pull request waits in the merge queue of its base branch
	QueueEnabled  bool   // the base branch has a merge queue; the queue sets the merge method
	QueueState    string // the entry state, such as AWAITING_CHECKS; empty when not in the queue
	QueuePosition int    // 1-based position in the queue; 0 when unknown
}

// HasUpdates reports whether the pull request has activity to show.
func (p *PullStatus) HasUpdates() bool {
	return p.State != "open" ||
		len(p.Comments) > 0 ||
		len(p.ReviewComments) > 0 ||
		p.NewReviewRequest ||
		p.NewApprovals > 0 ||
		p.NewChangesRequested > 0
}

// MarkSeen moves Since to FetchedAt and clears the new activity, as a
// commit does. ReviewRequested stays: it reflects the current state. Only
// the new flag clears.
func (p *PullStatus) MarkSeen() {
	p.Since = p.FetchedAt
	p.Comments = nil
	p.ReviewComments = nil
	p.NewReviewRequest = false
	p.NewApprovals = 0
	p.NewChangesRequested = 0
}

// timelineExclude lists every known timeline event type except the three
// the watch reads. The parameter is a deny list, so a type that GitHub adds
// later comes through until it is added here; derivePull skips events it
// does not know. The last row holds types seen in responses but absent from
// the documentation. The API rejects a name it does not know with a 400, so
// check a new name against the API before it goes in.
var timelineExclude = strings.Join([]string{
	"assigned", "closed", "commented", "committed", "commit-commented", "connected",
	"convert_to_draft", "converted_to_discussion", "cross-referenced", "demilestoned",
	"deployed", "disconnected", "head_ref_deleted", "head_ref_restored",
	"head_ref_force_pushed", "labeled", "line-commented", "locked", "mentioned",
	"marked_as_duplicate", "merged", "milestoned", "pinned", "ready_for_review",
	"referenced", "renamed", "reopened", "review_dismissed", "subscribed", "transferred",
	"unassigned", "unlabeled", "unlocked", "unmarked_as_duplicate", "unpinned",
	"unsubscribed", "user_blocked",
	"base_ref_changed", "copilot_work_started", "copilot_work_finished",
}, ",")

// timelinePageSize is the largest page the timeline endpoint allows.
const timelinePageSize = 100

// loadTimeline reads the review events of a pull request, oldest first. It
// fetches pages until one comes back short.
func (c *Client) loadTimeline(ref PullRef) ([]rawEvent, error) {
	var out []rawEvent
	for page := 1; ; page++ {
		var events []rawEvent
		path := fmt.Sprintf("repos/%s/issues/%d/timeline?per_page=%d&page=%d&exclude=%s",
			ref.Repo, ref.Number, timelinePageSize, page, timelineExclude)
		if err := c.api(ref.Domain, path, &events); err != nil {
			return nil, err
		}
		out = append(out, events...)
		if len(events) < timelinePageSize {
			return out, nil
		}
	}
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
		func() error {
			events, err := c.loadTimeline(ref)
			d.timeline = events
			return err
		},
		func() error {
			checks, err := c.requiredChecks(ref)
			d.checks = checks
			return err
		},
		func() error {
			q, err := c.mergeQueue(ref)
			d.queue = q
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
		InMergeQueue:   d.queue.InQueue,
		QueueEnabled:   d.queue.QueueEnabled,
	}
	if d.queue.Entry != nil {
		s.QueueState = d.queue.Entry.State
		s.QueuePosition = d.queue.Entry.Position
	}
	// the events are oldest first, so the last request, removal, or review
	// by the user gives the current state, and the time of the request says
	// whether it is new. A review from the user ends the request: GitHub
	// drops the reviewer from the requested list without a removal event.
	var reviews []rawReview
	for _, e := range d.timeline {
		switch e.Event {
		case "reviewed":
			reviews = append(reviews, rawReview{User: e.User, State: strings.ToUpper(e.State), SubmittedAt: e.SubmittedAt})
			if e.User.Login == d.username {
				s.ReviewRequested = false
				s.NewReviewRequest = false
			}
		case "review_requested", "review_request_removed":
			if e.RequestedReviewer == nil || e.RequestedReviewer.Login != d.username {
				continue
			}
			s.ReviewRequested = e.Event == "review_requested"
			s.NewReviewRequest = s.ReviewRequested && e.CreatedAt.After(since)
		}
	}

	var approvals []rawReview
	for _, r := range reviews {
		if r.State == "APPROVED" {
			approvals = append(approvals, r)
		}
	}
	for _, r := range reviews {
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
