package github

import (
	"errors"
	"strings"
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
		rules: []string{"lint", "unit"},
	}
	d.graph.Commits.Nodes = append(d.graph.Commits.Nodes, rollup(
		rawContext{Type: "StatusContext", Context: "lint", State: "SUCCESS"},
		rawContext{Type: "CheckRun", Name: "unit", Status: "IN_PROGRESS"},
	))
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

// rollup builds one commit node whose rollup holds the given contexts.
func rollup(contexts ...rawContext) (n struct {
	Commit struct {
		Rollup *struct {
			Contexts struct {
				Nodes []rawContext `json:"nodes"`
			} `json:"contexts"`
		} `json:"statusCheckRollup"`
	} `json:"commit"`
}) {
	n.Commit.Rollup = new(struct {
		Contexts struct {
			Nodes []rawContext `json:"nodes"`
		} `json:"contexts"`
	})
	n.Commit.Rollup.Contexts.Nodes = contexts
	return n
}

func TestReduceChecks(t *testing.T) {
	status := func(name, state string, required bool) rawContext {
		return rawContext{Type: "StatusContext", Context: name, State: state, Required: required}
	}
	run := func(name, status, conclusion string, started int) rawContext {
		return rawContext{Type: "CheckRun", Name: name, Status: status, Conclusion: conclusion, StartedAt: *ts(started)}
	}
	cases := []struct {
		name     string
		rules    []string
		legacy   []string
		contexts []rawContext
		want     string
	}{
		{name: "nothing", want: CheckNone},
		{name: "no rules and no required flag", contexts: []rawContext{status("lint", "PENDING", false)}, want: CheckNone},
		{name: "all pass", rules: []string{"lint"}, contexts: []rawContext{status("lint", "SUCCESS", true), status("extra", "FAILURE", false)}, want: CheckSuccess},
		{name: "skipped counts as pass", rules: []string{"lint"}, contexts: []rawContext{run("lint", "COMPLETED", "SKIPPED", 1)}, want: CheckSuccess},
		{name: "one pending", rules: []string{"lint", "unit"}, contexts: []rawContext{status("lint", "SUCCESS", true), status("unit", "PENDING", true)}, want: CheckPending},
		{name: "check run in progress", rules: []string{"unit"}, contexts: []rawContext{run("unit", "IN_PROGRESS", "", 1)}, want: CheckPending},
		{name: "failure wins", rules: []string{"lint", "unit"}, contexts: []rawContext{status("lint", "PENDING", true), run("unit", "COMPLETED", "FAILURE", 1)}, want: CheckFailure},
		{name: "required flag without rules", contexts: []rawContext{status("lint", "SUCCESS", true)}, want: CheckSuccess},
		{name: "legacy protection", legacy: []string{"lint"}, contexts: []rawContext{status("lint", "FAILURE", false)}, want: CheckFailure},
		// the required context never reported, so the rollup has no entry
		{name: "expected context is pending", rules: []string{"lint", "merge"}, contexts: []rawContext{status("lint", "SUCCESS", true)}, want: CheckPending},
		{name: "expected context with empty rollup", rules: []string{"lint"}, want: CheckPending},
		// a check run that ran again: the latest run gives the state
		{name: "latest run wins", rules: []string{"unit"}, contexts: []rawContext{run("unit", "COMPLETED", "FAILURE", 1), run("unit", "COMPLETED", "SUCCESS", 2)}, want: CheckSuccess},
		{name: "latest run wins in any order", rules: []string{"unit"}, contexts: []rawContext{run("unit", "COMPLETED", "FAILURE", 2), run("unit", "COMPLETED", "SUCCESS", 1)}, want: CheckFailure},
	}
	for _, c := range cases {
		var g rawGraph
		if c.legacy != nil {
			g.BaseRef.Protection = &struct {
				Contexts []string `json:"requiredStatusCheckContexts"`
			}{Contexts: c.legacy}
		}
		if c.contexts != nil {
			g.Commits.Nodes = append(g.Commits.Nodes, rollup(c.contexts...))
		}
		if got := reduceChecks(c.rules, g); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMergePull(t *testing.T) {
	var got [][]string
	c := &Client{run: func(args ...string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}, users: map[string]string{}}
	ref := PullRef{URL: "https://github.com/o/r/pull/12", Domain: "github.com", Repo: "o/r", Number: 12}
	if err := c.MergePull(ref, "squash"); err != nil {
		t.Fatal(err)
	}
	want := "pr merge 12 --repo github.com/o/r --squash"
	if len(got) != 1 || strings.Join(got[0], " ") != want {
		t.Fatalf("args: %v, want %q", got, want)
	}
	if err := c.MergePull(ref, "delete-branch"); err == nil || len(got) != 1 {
		t.Fatalf("unknown method must fail before gh runs: err=%v calls=%d", err, len(got))
	}
}

func TestPullGraph(t *testing.T) {
	var got [][]string
	out := `{"data":{"repository":{"pullRequest":{
		"isInMergeQueue":true,"isMergeQueueEnabled":true,"mergeQueueEntry":{"state":"AWAITING_CHECKS","position":3},
		"baseRefName":"main","baseRef":{"branchProtectionRule":{"requiredStatusCheckContexts":["legacy"]}},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
			{"__typename":"StatusContext","context":"lint","state":"SUCCESS","createdAt":"2026-09-03T10:00:00Z","isRequired":true},
			{"__typename":"CheckRun","name":"unit","status":"COMPLETED","conclusion":"FAILURE","startedAt":"2026-09-03T11:00:00Z","isRequired":false}
		]}}}}]}}}}}`
	c := &Client{run: func(args ...string) ([]byte, []byte, error) {
		got = append(got, args)
		return []byte(out), nil, nil
	}, users: map[string]string{}}
	ref := PullRef{Domain: "github.com", Repo: "o/r", Number: 12}
	g, err := c.pullGraph(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !g.InQueue || !g.QueueEnabled || g.Entry == nil || g.Entry.State != "AWAITING_CHECKS" || g.Entry.Position != 3 {
		t.Fatalf("queue: %+v", g.rawQueue)
	}
	if g.BaseRefName != "main" || g.BaseRef.Protection == nil || len(g.BaseRef.Protection.Contexts) != 1 {
		t.Fatalf("base: %+v", g.BaseRef)
	}
	nodes := g.Commits.Nodes[0].Commit.Rollup.Contexts.Nodes
	if len(nodes) != 2 || nodes[0].name() != "lint" || !nodes[0].Required || nodes[1].name() != "unit" || nodes[1].state() != "FAILURE" {
		t.Fatalf("contexts: %+v", nodes)
	}
	want := []string{"api", "graphql", "--hostname", "github.com", "-f"}
	if len(got) != 1 || len(got[0]) != 6 || strings.Join(got[0][:5], " ") != strings.Join(want, " ") {
		t.Fatalf("args: %v", got)
	}
	q := got[0][5]
	for _, part := range []string{`repository(owner:"o", name:"r")`, "pullRequest(number:12)", "isInMergeQueue", "isRequired(pullRequestNumber:12)", "requiredStatusCheckContexts"} {
		if !strings.Contains(q, part) {
			t.Fatalf("query lacks %q: %s", part, q)
		}
	}

	// a host without merge queue fields is queried again without them
	got = nil
	c.run = func(args ...string) ([]byte, []byte, error) {
		got = append(got, args)
		if strings.Contains(args[5], "isInMergeQueue") {
			return nil, []byte(`Field 'isInMergeQueue' doesn't exist on type 'PullRequest'`), errors.New("gh api graphql: exit status 1")
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"baseRefName":"main"}}}}`), nil, nil
	}
	g, err = c.pullGraph(ref)
	if err != nil || g.InQueue || g.BaseRefName != "main" || len(got) != 2 {
		t.Fatalf("missing schema: g=%+v err=%v calls=%d", g, err, len(got))
	}
	c.run = func(args ...string) ([]byte, []byte, error) { return nil, nil, errors.New("network down") }
	if _, err := c.pullGraph(ref); err == nil {
		t.Fatal("other errors must surface")
	}

	var d pullData
	d.pull.State = "open"
	d.graph.Entry = &rawQueueEntry{State: "AWAITING_CHECKS", Position: 3}
	d.graph.InQueue = true
	s := derivePull(ref, d, time.Time{}, time.Time{}, nil)
	if !s.InMergeQueue || s.QueueState != "AWAITING_CHECKS" || s.QueuePosition != 3 {
		t.Fatalf("status: %+v", s)
	}
}

func TestBranchRules(t *testing.T) {
	var got []string
	out := `[
		{"type":"deletion"},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"lint"},{"context":"unit"}]}},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"e2e"}]}}
	]`
	c := &Client{run: func(args ...string) ([]byte, []byte, error) {
		got = args
		return []byte(out), nil, nil
	}, users: map[string]string{}}
	ref := PullRef{Domain: "github.com", Repo: "o/r", Number: 12}
	rules, err := c.branchRules(ref, "release/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rules, ",") != "lint,unit,e2e" {
		t.Fatalf("rules: %v", rules)
	}
	want := "api --hostname github.com repos/o/r/rules/branches/release%2F1.0"
	if strings.Join(got, " ") != want {
		t.Fatalf("args: %v, want %q", got, want)
	}

	// a host without rulesets gives an empty list
	c.run = func(args ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("gh api: HTTP 404: Not Found (https://api.github.com/repos/o/r/rules/branches/main)")
	}
	if rules, err := c.branchRules(ref, "main"); err != nil || rules != nil {
		t.Fatalf("404: rules=%v err=%v", rules, err)
	}
	c.run = func(args ...string) ([]byte, []byte, error) { return nil, nil, errors.New("network down") }
	if _, err := c.branchRules(ref, "main"); err == nil {
		t.Fatal("other errors must surface")
	}
}
