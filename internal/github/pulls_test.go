package github

import (
	"testing"
	"time"
)

func ts(h int) *time.Time {
	t := time.Date(2026, 9, 3, h, 0, 0, 0, time.UTC)
	return &t
}

func TestDerivePull(t *testing.T) {
	since := *ts(10)
	fetched := *ts(12)
	d := pullData{
		username: "me",
		pull:     rawPull{Title: "Add thing", User: rawUser{Login: "alice"}, State: "open"},
		comments: []rawComment{
			{User: rawUser{Login: "bob", Type: "User"}, Body: "hi", CreatedAt: *ts(11)},
			{User: rawUser{Login: "me", Type: "User"}, Body: "mine", CreatedAt: *ts(11)},
			{User: rawUser{Login: "some-bot", Type: "Bot"}, Body: "beep", CreatedAt: *ts(11)},
			{User: rawUser{Login: "svc-user", Type: "User"}, Body: "excluded", CreatedAt: *ts(11)},
			{User: rawUser{Login: "carol", Type: "User"}, Body: "old", CreatedAt: *ts(9)},
		},
		reviewComments: []rawComment{
			{User: rawUser{Login: "bob", Type: "User"}, Body: "nit", CreatedAt: *ts(11)},
		},
		timeline: []rawEvent{
			// the timeline gives review states in lowercase
			{Event: "reviewed", User: rawUser{Login: "bob"}, State: "changes_requested", SubmittedAt: ts(8)},
			{Event: "reviewed", User: rawUser{Login: "frank"}, State: "approved", SubmittedAt: ts(9)},
			// a team request has no reviewer
			{Event: "review_requested", CreatedAt: *ts(9)},
			{Event: "review_requested", RequestedReviewer: &rawUser{Login: "dave"}, CreatedAt: *ts(9)},
			{Event: "review_requested", RequestedReviewer: &rawUser{Login: "me"}, CreatedAt: *ts(11)},
			{Event: "reviewed", User: rawUser{Login: "bob"}, State: "approved", SubmittedAt: ts(11)},
			{Event: "reviewed", User: rawUser{Login: "erin"}, State: "changes_requested", SubmittedAt: ts(11)},
			{Event: "reviewed", User: rawUser{Login: "gina"}, State: "commented", SubmittedAt: ts(11)},
			{Event: "labeled", CreatedAt: *ts(11)}, // an unknown kind is skipped
		},
		checks: []rawCheck{{State: "SUCCESS"}, {State: "PENDING"}},
	}
	s := derivePull(PullRef{Number: 1}, d, since, fetched, []string{"svc-user"})

	if len(s.Comments) != 1 || s.Comments[0].Author != "bob" {
		t.Fatalf("comments: %+v", s.Comments)
	}
	if len(s.ReviewComments) != 1 {
		t.Fatalf("review comments: %+v", s.ReviewComments)
	}
	if !s.ReviewRequested || !s.NewReviewRequest {
		t.Fatalf("review requested %v new %v, want both true", s.ReviewRequested, s.NewReviewRequest)
	}
	if s.Ours {
		t.Fatal("ours must be false")
	}
	if s.Approvals != 2 || s.NewApprovals != 1 {
		t.Fatalf("approvals: %d new %d", s.Approvals, s.NewApprovals)
	}
	// bob's request is superseded by a later approval; erin's stands
	if s.ChangesRequested != 1 || s.NewChangesRequested != 1 {
		t.Fatalf("changes requested: %d new %d", s.ChangesRequested, s.NewChangesRequested)
	}
	if s.CheckState != CheckPending {
		t.Fatalf("check state: %q", s.CheckState)
	}
	if !s.HasUpdates() {
		t.Fatal("must have updates")
	}

	s.MarkSeen()
	if !s.Since.Equal(fetched) || len(s.Comments) != 0 || s.NewApprovals != 0 || s.NewChangesRequested != 0 || s.NewReviewRequest {
		t.Fatalf("after mark seen: %+v", s)
	}
	// the request stands, but it is no longer new, so the pull hides
	if !s.ReviewRequested {
		t.Fatal("the review request flag must stay after a commit")
	}
	if s.HasUpdates() {
		t.Fatal("a committed review request must not keep the pull in the update list")
	}
}

func TestDerivePullReviewRequest(t *testing.T) {
	since := *ts(10)
	me := &rawUser{Login: "me"}
	cases := []struct {
		name      string
		timeline  []rawEvent
		requested bool
		isNew     bool
	}{
		{"old request", []rawEvent{
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(9)},
		}, true, false},
		{"new request", []rawEvent{
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(11)},
		}, true, true},
		{"request then removal", []rawEvent{
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(11)},
			{Event: "review_request_removed", RequestedReviewer: me, CreatedAt: *ts(11)},
		}, false, false},
		{"removal then request again", []rawEvent{
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(8)},
			{Event: "review_request_removed", RequestedReviewer: me, CreatedAt: *ts(9)},
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(11)},
		}, true, true},
		{"request then my review", []rawEvent{
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(11)},
			{Event: "reviewed", User: *me, State: "commented", SubmittedAt: ts(11)},
		}, false, false},
		{"my review then request", []rawEvent{
			{Event: "reviewed", User: *me, State: "changes_requested", SubmittedAt: ts(9)},
			{Event: "review_requested", RequestedReviewer: me, CreatedAt: *ts(11)},
		}, true, true},
		{"request for someone else", []rawEvent{
			{Event: "review_requested", RequestedReviewer: &rawUser{Login: "dave"}, CreatedAt: *ts(11)},
		}, false, false},
	}
	for _, c := range cases {
		d := pullData{username: "me", pull: rawPull{User: rawUser{Login: "alice"}, State: "open"}, timeline: c.timeline}
		s := derivePull(PullRef{}, d, since, *ts(12), nil)
		if s.ReviewRequested != c.requested || s.NewReviewRequest != c.isNew {
			t.Errorf("%s: requested %v new %v, want %v %v", c.name, s.ReviewRequested, s.NewReviewRequest, c.requested, c.isNew)
		}
		if s.HasUpdates() != c.isNew {
			t.Errorf("%s: has updates %v, want %v", c.name, s.HasUpdates(), c.isNew)
		}
	}
}

func TestDerivePullQuiet(t *testing.T) {
	d := pullData{username: "me", pull: rawPull{User: rawUser{Login: "me"}, State: "open"}}
	s := derivePull(PullRef{}, d, *ts(10), *ts(11), nil)
	if s.HasUpdates() {
		t.Fatalf("quiet pull must have no updates: %+v", s)
	}
	if !s.Ours {
		t.Fatal("ours must be true")
	}
	d.pull.State = "closed"
	d.pull.Merged = true
	s = derivePull(PullRef{}, d, *ts(10), *ts(11), nil)
	if !s.HasUpdates() {
		t.Fatal("a closed pull is an update")
	}
}

func TestReduceChecks(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, CheckNone},
		{[]string{"SUCCESS", "SKIPPED"}, CheckSuccess},
		{[]string{"SUCCESS", "PENDING"}, CheckPending},
		{[]string{"PENDING", "FAILURE"}, CheckFailure},
		{[]string{"FAILURE", "PENDING"}, CheckFailure},
		{[]string{"IN_PROGRESS"}, CheckPending},
	}
	for _, c := range cases {
		var checks []rawCheck
		for _, s := range c.in {
			checks = append(checks, rawCheck{State: s})
		}
		if got := reduceChecks(checks); got != c.want {
			t.Errorf("%v: got %q, want %q", c.in, got, c.want)
		}
	}
}
