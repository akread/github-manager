package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github-manager/internal/github"
	"github-manager/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// run sends a message and feeds the messages from the returned command back
// into the model, as the bubbletea runtime does. It stops at a tick or a
// batch: a tick command sleeps for the interval.
func run(m tea.Model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	for cmd != nil {
		out := cmd()
		if out == nil {
			return
		}
		switch out.(type) {
		case tickMsg, tea.BatchMsg:
			return
		}
		_, cmd = m.Update(out)
	}
}

func plain(s string) string { return ansi.Strip(s) }

var fetched = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

// fakePullLoader returns an open pull with one new comment for every input,
// and an error for urls that contain "broken".
func fakePullLoader(pulls []store.Pull) []PullEntry {
	out := make([]PullEntry, len(pulls))
	for i, p := range pulls {
		if strings.Contains(p.URL, "broken") {
			out[i] = PullEntry{Pull: p, Err: errors.New("boom")}
			continue
		}
		ref := github.PullRef{URL: p.URL, Domain: p.Domain, Repo: p.Repo, Number: p.Number}
		st := &github.PullStatus{
			Ref: ref, Since: p.Since, FetchedAt: fetched,
			Title: "Title " + p.URL[len(p.URL)-1:], Author: "alice", State: "open",
			Comments: []github.Comment{{Author: "bob", Body: "hello", URL: p.URL + "#c1", CreatedAt: fetched}},
		}
		if p.Number == 2 {
			st.Comments = nil // quiet
		}
		out[i] = PullEntry{Pull: p, Status: st}
	}
	return out
}

func newTestPulls(t *testing.T) (*pullsModel, *store.Store) {
	t.Helper()
	st := testStore(t)
	for i := 1; i <= 3; i++ {
		url := "https://github.com/o/r/pull/" + string(rune('0'+i))
		if _, err := st.SubscribePull(store.Pull{URL: url, Domain: "github.com", Repo: "o/r", Number: i}); err != nil {
			t.Fatal(err)
		}
	}
	m := newPullsModel(PullsOptions{Store: st, Load: fakePullLoader, Interval: time.Hour, Open: func(string) error { return nil }})
	run(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	run(m, m.refresh()())
	return m, st
}

func TestPullsViewAndToggle(t *testing.T) {
	m, _ := newTestPulls(t)
	v := plain(m.View())
	if !strings.Contains(v, "2 of 3 with updates") {
		t.Fatalf("header: %s", v)
	}
	if strings.Contains(v, "Title 2") {
		t.Fatalf("quiet pull must be hidden: %s", v)
	}
	run(m, key("a"))
	v = plain(m.View())
	if !strings.Contains(v, "Title 2") || !strings.Contains(v, "all shown") {
		t.Fatalf("all shown: %s", v)
	}
}

func TestPullsCommitSelected(t *testing.T) {
	m, st := newTestPulls(t)
	run(m, key("j")) // cursor on Title 3
	run(m, key("c"))
	p, err := st.GetPull("https://github.com/o/r/pull/3")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Since.Equal(fetched) {
		t.Fatalf("since not committed: %v", p.Since)
	}
	other, _ := st.GetPull("https://github.com/o/r/pull/1")
	if other.Since.Equal(fetched) {
		t.Fatal("commit must be per pull request")
	}
	v := plain(m.View())
	if !strings.Contains(v, "1 of 3 with updates") || strings.Contains(v, "Title 3") {
		t.Fatalf("committed pull still shown: %s", v)
	}
	if !strings.Contains(v, "committed o/r#3") {
		t.Fatalf("status: %s", v)
	}
}

func TestPullsCommitAllUnsubscribesClosed(t *testing.T) {
	m, st := newTestPulls(t)
	m.entries[0].Status.State = "closed"
	run(m, key("C"))
	if _, err := st.GetPull("https://github.com/o/r/pull/1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("closed pull must be unsubscribed: %v", err)
	}
	pulls, _ := st.ListPulls()
	for _, p := range pulls {
		if !p.Since.Equal(fetched) {
			t.Fatalf("since not committed for %s", p.URL)
		}
	}
	if len(m.entries) != 2 {
		t.Fatalf("entries: %d", len(m.entries))
	}
}

func TestPullsSubscribeInput(t *testing.T) {
	m, st := newTestPulls(t)
	run(m, key("s"))
	if !m.inputOn {
		t.Fatal("input must open")
	}
	for _, r := range "https://github.com/o/r/pull/9" {
		run(m, key(string(r)))
	}
	run(m, key("enter"))
	if m.inputOn {
		t.Fatalf("input must close: %q", m.errMsg)
	}
	if _, err := st.GetPull("https://github.com/o/r/pull/9"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain(m.View()), "Title 9") {
		t.Fatalf("new pull must show: %s", plain(m.View()))
	}

	run(m, key("s"))
	for _, r := range "nope" {
		run(m, key(string(r)))
	}
	run(m, key("enter"))
	if !m.inputOn || m.errMsg == "" {
		t.Fatal("bad url must keep the input open with an error")
	}
	run(m, key("esc"))
	if m.inputOn {
		t.Fatal("esc must close the input")
	}
}

func TestPullsUnsubscribeAndError(t *testing.T) {
	m, st := newTestPulls(t)
	st.SubscribePull(store.Pull{URL: "https://github.com/o/r/pull/broken", Domain: "github.com", Repo: "o/r", Number: 7})
	run(m, key("r"))
	v := plain(m.View())
	if !strings.Contains(v, "[ERROR]") || !strings.Contains(v, "boom") {
		t.Fatalf("error entry: %s", v)
	}
	run(m, key("G"))
	run(m, key("c"))
	if m.errMsg == "" {
		t.Fatal("commit on a failed entry must set an error")
	}
	run(m, key("u"))
	if _, err := st.GetPull("https://github.com/o/r/pull/broken"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsubscribe: %v", err)
	}
}

func fakeReviewLoader(repos []store.Repo) []ReviewEntry {
	out := make([]ReviewEntry, len(repos))
	for i, r := range repos {
		if strings.Contains(r.Repo, "broken") {
			out[i] = ReviewEntry{Repo: r, Err: errors.New("boom")}
			continue
		}
		e := ReviewEntry{Repo: r}
		for n := 1; n <= 2; n++ {
			ref := github.PullRef{URL: "https://github.com/" + r.Repo + "/pull/" + string(rune('0'+n)), Domain: r.Domain, Repo: r.Repo, Number: n}
			e.Requests = append(e.Requests, ReviewRequest{ReviewRequest: github.ReviewRequest{Ref: ref, Title: r.Repo + " PR " + string(rune('0'+n)), Author: "carol"}})
		}
		out[i] = e
	}
	return out
}

func newTestReviews(t *testing.T) (*reviewsModel, *store.Store) {
	t.Helper()
	st := testStore(t)
	for _, r := range []string{"o/a", "o/b"} {
		if _, err := st.SubscribeRepo(store.Repo{Domain: "github.com", Repo: r}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MarkSeen("github.com", "o/a", 1); err != nil {
		t.Fatal(err)
	}
	m := newReviewsModel(ReviewsOptions{Store: st, Load: fakeReviewLoader, Interval: time.Hour, Open: func(string) error { return nil }})
	run(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	run(m, m.refresh()())
	return m, st
}

func TestReviewsViewAndSeen(t *testing.T) {
	m, st := newTestReviews(t)
	v := plain(m.View())
	if !strings.Contains(v, "3 new review requests across 2 repos (4 total pending)") {
		t.Fatalf("header: %s", v)
	}
	if strings.Contains(v, "o/a PR 1") {
		t.Fatalf("seen request must be hidden: %s", v)
	}
	// cursor on o/a PR 2; mark seen
	run(m, key("c"))
	seen, _ := st.SeenReviews("github.com", "o/a")
	if !seen[2] {
		t.Fatalf("seen: %v", seen)
	}
	v = plain(m.View())
	if !strings.Contains(v, "2 new review requests") || strings.Contains(v, "o/a PR 2") {
		t.Fatalf("after seen: %s", v)
	}
	run(m, key("a"))
	v = plain(m.View())
	if !strings.Contains(v, "o/a PR 1") || !strings.Contains(v, "o/a PR 2") {
		t.Fatalf("all shown: %s", v)
	}
}

func TestReviewsMarkAllAndSubscribe(t *testing.T) {
	m, st := newTestReviews(t)
	st.MarkSeen("github.com", "o/b", 99) // a closed one, pruned by C
	run(m, key("s"))                     // subscribe o/a PR 2
	if _, err := st.GetPull("https://github.com/o/a/pull/2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain(m.View()), "(watching)") {
		t.Fatalf("watching marker: %s", plain(m.View()))
	}
	run(m, key("C"))
	seen, _ := st.SeenReviews("github.com", "o/b")
	if !seen[1] || !seen[2] || seen[99] {
		t.Fatalf("seen after C: %v", seen)
	}
	v := plain(m.View())
	if !strings.Contains(v, "0 new review requests") || !strings.Contains(v, "no new review requests") {
		t.Fatalf("after C: %s", v)
	}
}

func TestReviewsError(t *testing.T) {
	m, st := newTestReviews(t)
	st.SubscribeRepo(store.Repo{Domain: "github.com", Repo: "o/broken"})
	run(m, key("r"))
	v := plain(m.View())
	if !strings.Contains(v, "boom") {
		t.Fatalf("error row: %s", v)
	}
	// the error row is a cursor position, and u does nothing on this screen
	run(m, key("G"))
	run(m, key("u"))
	if _, err := st.GetRepo("github.com", "o/broken"); err != nil {
		t.Fatalf("repo must stay subscribed: %v", err)
	}
}

func TestViewport(t *testing.T) {
	items := [][]string{{"a1", "a2"}, {"b1", "b2"}, {"c1", "c2"}, {"d1", "d2"}}
	var vp viewport
	got := vp.render(items, 0, 3)
	if strings.Join(got, ",") != "a1,a2,b1" {
		t.Fatalf("top: %v", got)
	}
	got = vp.render(items, 3, 3)
	if strings.Join(got, ",") != "d1,d2" || vp.offset != 3 {
		t.Fatalf("bottom: %v offset %d", got, vp.offset)
	}
	got = vp.render(items, 1, 3)
	if strings.Join(got, ",") != "b1,b2,c1" {
		t.Fatalf("back up: %v", got)
	}
}

func TestWrapHelp(t *testing.T) {
	help := "j/k move · c commit · C commit all · q quit"
	if got := wrapHelp(help, 0); len(got) != 1 || got[0] != help {
		t.Fatalf("unknown width: %v", got)
	}
	if got := wrapHelp(help, 100); len(got) != 1 {
		t.Fatalf("wide: %v", got)
	}
	got := wrapHelp(help, 22)
	want := []string{"j/k move · c commit", "C commit all · q quit"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("narrow: got %v, want %v", got, want)
	}
	// an item longer than the width gets its own row and is clipped later
	got = wrapHelp(help, 5)
	if len(got) != 4 {
		t.Fatalf("very narrow: %v", got)
	}
}

// helpBlock returns the help rows at the bottom of a view: the trailing
// lines down to the empty status row above them.
func helpBlock(v string) []string {
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && lines[i] != ""; i-- {
		out = append([]string{lines[i]}, out...)
	}
	return out
}

func lastLine(v string) string {
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestHelpRow(t *testing.T) {
	m, _ := newTestPulls(t)
	// wide: one row, every item, only "q quit" at the right edge
	run(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	last := lastLine(plain(m.View()))
	if !strings.Contains(last, "m toggle comments") || !strings.HasSuffix(last, quitTail) || strings.Contains(last, "? help") || strings.Contains(last, "…") {
		t.Fatalf("wide: %q", last)
	}
	if ansi.StringWidth(last) != 160 {
		t.Fatalf("wide: the tail must sit at the right edge, width %d", ansi.StringWidth(last))
	}
	// ? does nothing visible when nothing is cut
	run(m, key("?"))
	if lastLine(plain(m.View())) != last {
		t.Fatalf("wide expanded: %q", lastLine(plain(m.View())))
	}
	run(m, key("?"))

	// narrow: still one row, the items cut, "? help · q quit" whole
	run(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	v := plain(m.View())
	last = lastLine(v)
	if !strings.Contains(last, "…") || !strings.HasSuffix(last, helpTail) || ansi.StringWidth(last) != 60 {
		t.Fatalf("narrow: %q", last)
	}
	if n := strings.Count(v, "\n"); n != 29 {
		t.Fatalf("view must fill the height exactly: %d newlines", n)
	}

	// ? expands the items onto more rows; the tail sits bottom right
	run(m, key("?"))
	v = plain(m.View())
	if strings.Contains(v, "…") {
		t.Fatalf("expanded help must not be cut: %s", v)
	}
	block := helpBlock(v)
	if len(block) < 2 {
		t.Fatalf("expanded block: %q", block)
	}
	bottom := block[len(block)-1]
	if !strings.HasSuffix(bottom, helpTail) || ansi.StringWidth(bottom) != 60 {
		t.Fatalf("expanded bottom row: %q", bottom)
	}
	above := strings.Join(block[:len(block)-1], "\n")
	if strings.Contains(above, "? help") || strings.Contains(above, "q quit") || !strings.Contains(above, "j/k move") {
		t.Fatalf("expanded rows above: %q", above)
	}
	if n := strings.Count(v, "\n"); n != 29 {
		t.Fatalf("expanded view must fill the height exactly: %d newlines", n)
	}
	run(m, key("?"))
	if strings.Count(plain(m.View()), "\n") != 29 || !strings.Contains(lastLine(plain(m.View())), "…") {
		t.Fatalf("collapsed again: %s", plain(m.View()))
	}

	// the input help has no tail and is never expanded
	run(m, key("s"))
	last = lastLine(plain(m.View()))
	if last != "enter subscribe · esc cancel" {
		t.Fatalf("input help: %q", last)
	}
	run(m, key("esc"))

	r, _ := newTestReviews(t)
	run(r, tea.WindowSizeMsg{Width: 40, Height: 30})
	last = lastLine(plain(r.View()))
	if !strings.HasSuffix(last, helpTail) || !strings.Contains(last, "…") {
		t.Fatalf("reviews narrow: %q", last)
	}
	run(r, key("?"))
	helpLines := helpBlock(plain(r.View()))
	joined := strings.Join(helpLines, "\n")
	if len(helpLines) < 2 || strings.Contains(joined, "…") || !strings.HasSuffix(helpLines[len(helpLines)-1], helpTail) || strings.Count(joined, "q quit") != 1 {
		t.Fatalf("reviews expanded help: %s", joined)
	}
}

func wheel(down bool) tea.MouseMsg {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return tea.MouseMsg{Button: b, Action: tea.MouseActionPress}
}

func click(y int) tea.MouseMsg {
	return tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: y}
}

func TestViewportScrollAndClick(t *testing.T) {
	items := [][]string{{"a1", "a2"}, {"b1", "b2"}, {"c1", "c2"}, {"d1", "d2"}, {"e1", "e2"}}
	var vp viewport
	if got := vp.maxOffset(items, 4); got != 3 {
		t.Fatalf("maxOffset: %d", got)
	}
	cursor := vp.scrollBy(items, 0, 3, 4) // two whole items down
	if vp.offset != 2 || cursor != 2 {
		t.Fatalf("down: offset %d cursor %d", vp.offset, cursor)
	}
	cursor = vp.scrollBy(items, cursor, 30, 4) // stops at the end
	if vp.offset != 3 || cursor != 3 {
		t.Fatalf("past end: offset %d cursor %d", vp.offset, cursor)
	}
	cursor = vp.scrollBy(items, 4, -30, 4) // back to the top, cursor into view
	if vp.offset != 0 || cursor != 1 {
		t.Fatalf("up: offset %d cursor %d", vp.offset, cursor)
	}
	if got := vp.itemAt(items, 3, 4); got != 1 {
		t.Fatalf("itemAt row 3: %d", got)
	}
	if got := vp.itemAt(items, 9, 4); got != -1 {
		t.Fatalf("itemAt below the body: %d", got)
	}
	vp.offset = 4
	if got := vp.itemAt(items, 2, 4); got != -1 {
		t.Fatalf("itemAt past the last item: %d", got)
	}
}

func TestPullsMouse(t *testing.T) {
	m, _ := newTestPulls(t)
	run(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	// the second visible pull starts at body row 4: rows 0-3 are the first
	run(m, click(bodyTop+4))
	if m.cursor != 1 {
		t.Fatalf("click: cursor %d", m.cursor)
	}
	run(m, click(bodyTop+0))
	if m.cursor != 0 {
		t.Fatalf("click first: cursor %d", m.cursor)
	}
	run(m, click(bodyTop+25)) // empty row: no change
	if m.cursor != 0 {
		t.Fatalf("click empty: cursor %d", m.cursor)
	}
	// a small screen: the wheel scrolls and moves the cursor into view
	run(m, tea.WindowSizeMsg{Width: 160, Height: 9})
	run(m, wheel(true))
	if m.vp.offset != 1 || m.cursor != 1 {
		t.Fatalf("wheel down: offset %d cursor %d", m.vp.offset, m.cursor)
	}
	run(m, wheel(false))
	// only the first pull fits in full, so the cursor comes back to it
	if m.vp.offset != 0 || m.cursor != 0 {
		t.Fatalf("wheel up: offset %d cursor %d", m.vp.offset, m.cursor)
	}
	// no mouse action while the input is open
	run(m, key("s"))
	run(m, click(bodyTop+4))
	if m.cursor != 0 {
		t.Fatalf("click during input: cursor %d", m.cursor)
	}
}

func TestReviewsMouse(t *testing.T) {
	m, _ := newTestReviews(t)
	run(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	// rows: [o/a header, o/a PR 2, url] [blank, o/b header, o/b PR 1, url] [o/b PR 2, url]
	run(m, click(bodyTop+5))
	if m.cursor != 1 {
		t.Fatalf("click o/b PR 1: cursor %d", m.cursor)
	}
	run(m, click(bodyTop+7))
	if m.cursor != 2 {
		t.Fatalf("click o/b PR 2: cursor %d", m.cursor)
	}
	run(m, tea.WindowSizeMsg{Width: 160, Height: 9})
	run(m, wheel(true))
	// the body has 5 rows: from offset 1 only o/b PR 1 fits in full
	if m.vp.offset != 1 || m.cursor != 1 {
		t.Fatalf("wheel down: offset %d cursor %d", m.vp.offset, m.cursor)
	}
}
