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
		requested: rawRequestedReviewers{Users: []rawUser{{Login: "me"}, {Login: "dave"}}},
		reviews: []rawReview{
			{User: rawUser{Login: "bob"}, State: "CHANGES_REQUESTED", SubmittedAt: ts(8)},
			{User: rawUser{Login: "bob"}, State: "APPROVED", SubmittedAt: ts(11)},
			{User: rawUser{Login: "erin"}, State: "CHANGES_REQUESTED", SubmittedAt: ts(11)},
			{User: rawUser{Login: "frank"}, State: "APPROVED", SubmittedAt: ts(9)},
			{User: rawUser{Login: "gina"}, State: "COMMENTED", SubmittedAt: ts(11)},
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
	if !s.ReviewRequested {
		t.Fatal("review requested must be true")
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
	if !s.Since.Equal(fetched) || len(s.Comments) != 0 || s.NewApprovals != 0 || s.NewChangesRequested != 0 {
		t.Fatalf("after mark seen: %+v", s)
	}
	// the review request flag stays, so the pull still shows
	if !s.HasUpdates() {
		t.Fatal("review request keeps the pull in the update list")
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
