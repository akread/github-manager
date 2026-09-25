package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github-manager/internal/github"
	"github-manager/internal/hook"
	"github-manager/internal/store"
)

// ReviewRequest is one review request with its watch state.
type ReviewRequest struct {
	github.ReviewRequest
	Seen     bool // committed as seen
	Watching bool // subscribed under pulls
	// Category is the result of the categorize hook. Nil means the hook has
	// not run yet, or is off.
	Category *store.ReviewCategory
	// CategoryErr is the error from the hook run of this session. The hook
	// does not run again for this pull request until the user asks.
	CategoryErr  error
	Categorizing bool // the hook runs now
}

// ReviewEntry is one watched repository with its review requests, or the
// error from the fetch.
type ReviewEntry struct {
	Repo     store.Repo
	Requests []ReviewRequest
	Err      error
}

// ReviewsOptions configures the reviews watch.
type ReviewsOptions struct {
	Store *store.Store
	// Load fetches the review requests of each repository. The result has
	// one entry per input, in the same order. The model fills Seen and
	// Watching from the store.
	Load     func(repos []store.Repo) []ReviewEntry
	Interval time.Duration
	Expanded bool // show every review request, not only the new ones
	// Open opens a URL in the browser. Nil means the system default.
	Open func(url string) error
	// Categorize runs the categorize hook for one review request. Nil turns
	// the hook off. The model runs it once per pull request and keeps the
	// result in the store; the runs of one refresh go in parallel.
	Categorize func(hook.Input) (hook.Result, error)
}

// ReviewLoader returns a Load function that uses the gh client. It fetches
// up to eight repositories at the same time.
func ReviewLoader(c *github.Client) func([]store.Repo) []ReviewEntry {
	return func(repos []store.Repo) []ReviewEntry {
		out := make([]ReviewEntry, len(repos))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, r := range repos {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				reqs, err := c.LoadReviewRequests(github.RepoRef{Domain: r.Domain, Repo: r.Repo})
				e := ReviewEntry{Repo: r, Err: err}
				for _, rq := range reqs {
					e.Requests = append(e.Requests, ReviewRequest{ReviewRequest: rq})
				}
				out[i] = e
			}()
		}
		wg.Wait()
		return out
	}
}

// RunReviews opens the reviews watch.
func RunReviews(o ReviewsOptions) error {
	m := newReviewsModel(o)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

type reviewsLoadedMsg struct {
	entries []ReviewEntry
	at      time.Time
	err     error
}

// reviewCategorizedMsg is the result of one categorize hook run.
type reviewCategorizedMsg struct {
	ref    github.PullRef
	result hook.Result
	err    error
}

// reviewRow is one cursor position: a request of an entry, or the entry
// itself when req is -1 (a repository with nothing to show).
type reviewRow struct {
	entry int
	req   int
}

type reviewsModel struct {
	o       ReviewsOptions
	entries []ReviewEntry
	cursor  int // index into rows()
	vp      viewport

	loading     bool
	refreshedAt time.Time
	showAll     bool
	highOnly    bool // show only the requests with high priority
	summaries   bool // show the hook summary under each request

	// categorizing holds the urls whose hook run is in progress, and
	// failed holds the error of each run that failed in this session.
	categorizing map[string]bool
	failed       map[string]error

	errMsg    string
	statusMsg string
	helpOn    bool // the user pressed ? to expand the help onto more rows

	width, height int
}

func newReviewsModel(o ReviewsOptions) *reviewsModel {
	if o.Open == nil {
		o.Open = openURL
	}
	if o.Interval <= 0 {
		o.Interval = 5 * time.Minute
	}
	return &reviewsModel{o: o, showAll: o.Expanded, categorizing: map[string]bool{}, failed: map[string]error{}}
}

func (m *reviewsModel) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(m.o.Interval))
}

// refresh loads the review requests of every watched repository in the
// background, then marks the seen and the watched ones from the store, and
// attaches the stored hook results.
func (m *reviewsModel) refresh() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	o := m.o
	categorize := o.Categorize != nil
	return func() tea.Msg {
		repos, err := o.Store.ListRepos()
		if err != nil {
			return reviewsLoadedMsg{err: err, at: time.Now()}
		}
		entries := o.Load(repos)
		pulls, err := o.Store.ListPulls()
		if err != nil {
			return reviewsLoadedMsg{err: err, at: time.Now()}
		}
		watching := map[string]bool{}
		for _, p := range pulls {
			watching[p.URL] = true
		}
		for i := range entries {
			e := &entries[i]
			if e.Err != nil {
				continue
			}
			seen, err := o.Store.SeenReviews(e.Repo.Domain, e.Repo.Repo)
			if err != nil {
				e.Err = err
				continue
			}
			var cats map[int]store.ReviewCategory
			if categorize {
				if cats, err = o.Store.Categories(e.Repo.Domain, e.Repo.Repo); err != nil {
					e.Err = err
					continue
				}
			}
			for j := range e.Requests {
				r := &e.Requests[j]
				r.Seen = seen[r.Ref.Number]
				r.Watching = watching[r.Ref.URL]
				if c, ok := cats[r.Ref.Number]; ok {
					r.Category = &c
				}
			}
		}
		return reviewsLoadedMsg{entries: entries, at: time.Now()}
	}
}

// categorizeMissing starts the hook for every request without a stored
// result, one command per request so the runs go in parallel. A request
// whose run is in progress, or failed in this session, is skipped.
func (m *reviewsModel) categorizeMissing() tea.Cmd {
	if m.o.Categorize == nil {
		return nil
	}
	var cmds []tea.Cmd
	for i := range m.entries {
		e := &m.entries[i]
		if e.Err != nil {
			continue
		}
		for j := range e.Requests {
			r := &e.Requests[j]
			if err, ok := m.failed[r.Ref.URL]; ok {
				r.CategoryErr = err
			}
			if m.categorizing[r.Ref.URL] {
				r.Categorizing = true
			}
			if r.Category != nil || r.Categorizing || r.CategoryErr != nil {
				continue
			}
			m.categorizing[r.Ref.URL] = true
			r.Categorizing = true
			cmds = append(cmds, m.categorize(r.ReviewRequest))
		}
	}
	return tea.Batch(cmds...)
}

// categorize runs the hook for one request in the background.
func (m *reviewsModel) categorize(r github.ReviewRequest) tea.Cmd {
	run := m.o.Categorize
	in := hook.Input{URL: r.Ref.URL, Domain: r.Ref.Domain, Repo: r.Ref.Repo, Number: r.Ref.Number, Title: r.Title, Author: r.Author, Draft: r.Draft}
	return func() tea.Msg {
		res, err := run(in)
		return reviewCategorizedMsg{ref: r.Ref, result: res, err: err}
	}
}

// findRequest returns the request with the url, or nil when the list no
// longer holds it.
func (m *reviewsModel) findRequest(url string) *ReviewRequest {
	for i := range m.entries {
		for j := range m.entries[i].Requests {
			if r := &m.entries[i].Requests[j]; r.Ref.URL == url {
				return r
			}
		}
	}
	return nil
}

// categorized stores the hook result and shows it on the request. A failed
// run is remembered for the session, so the hook does not run again for
// that pull request until the user presses x.
func (m *reviewsModel) categorized(msg reviewCategorizedMsg) {
	delete(m.categorizing, msg.ref.URL)
	r := m.findRequest(msg.ref.URL)
	if r != nil {
		r.Categorizing = false
	}
	if msg.err != nil {
		m.failed[msg.ref.URL] = msg.err
		if r != nil {
			r.CategoryErr = msg.err
		}
		return
	}
	c := store.ReviewCategory{Priority: msg.result.Priority, Category: msg.result.Category, Summary: msg.result.Summary}
	if err := m.o.Store.SetCategory(msg.ref.Domain, msg.ref.Repo, msg.ref.Number, c); err != nil {
		// the repository can be unsubscribed while the hook runs; the
		// result then has no row to live in
		if r != nil {
			m.errMsg = err.Error()
		}
		return
	}
	if r != nil {
		r.Category = &c
		m.clampCursor()
	}
}

// recategorize forgets the stored result of the selected request and runs
// the hook again.
func (m *reviewsModel) recategorize(row reviewRow) tea.Cmd {
	e := &m.entries[row.entry]
	r := &e.Requests[row.req]
	if r.Categorizing {
		return nil
	}
	if err := m.o.Store.DeleteCategory(e.Repo.Domain, e.Repo.Repo, r.Ref.Number); err != nil {
		m.errMsg = err.Error()
		return nil
	}
	delete(m.failed, r.Ref.URL)
	r.Category = nil
	r.CategoryErr = nil
	m.statusMsg = fmt.Sprintf("categorizing %s#%d", e.Repo.Repo, r.Ref.Number)
	return m.categorizeMissing()
}

// isHigh reports whether the request has a high priority.
func (r ReviewRequest) isHigh() bool {
	return r.Category != nil && r.Category.Priority == "high"
}

// rows returns the cursor positions to show.
func (m *reviewsModel) rows() []reviewRow {
	var out []reviewRow
	for ei, e := range m.entries {
		if e.Err != nil {
			out = append(out, reviewRow{ei, -1})
			continue
		}
		n := len(out)
		for ri, r := range e.Requests {
			if m.highOnly && !r.isHigh() {
				continue
			}
			if m.showAll || !r.Seen {
				out = append(out, reviewRow{ei, ri})
			}
		}
		if len(out) == n && m.showAll {
			out = append(out, reviewRow{ei, -1})
		}
	}
	return out
}

func (m *reviewsModel) clampCursor() {
	n := len(m.rows())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// selected returns the cursor row, or false when there is none.
func (m *reviewsModel) selected() (reviewRow, bool) {
	rows := m.rows()
	if len(rows) == 0 {
		return reviewRow{}, false
	}
	m.clampCursor()
	return rows[m.cursor], true
}

func (m *reviewsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick(m.o.Interval))
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case reviewsLoadedMsg:
		m.loading = false
		m.refreshedAt = msg.at
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.clampCursor()
		return m, m.categorizeMissing()
	case reviewCategorizedMsg:
		m.categorized(msg)
		return m, nil
	case tea.KeyMsg:
		return m.updateKeys(msg)
	case tea.MouseMsg:
		m.updateMouse(msg)
		return m, nil
	}
	return m, nil
}

// updateMouse scrolls on the wheel and moves the cursor on a left click.
func (m *reviewsModel) updateMouse(msg tea.MouseMsg) {
	items := m.items()
	h := m.frame().bodyHeight()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.cursor = m.vp.scrollBy(items, m.cursor, -wheelStep, h)
	case tea.MouseButtonWheelDown:
		m.cursor = m.vp.scrollBy(items, m.cursor, wheelStep, h)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return
		}
		if i := m.vp.itemAt(items, msg.Y-bodyTop, h); i >= 0 {
			m.cursor = i
		}
	}
}

func (m *reviewsModel) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errMsg = ""
	m.statusMsg = ""
	rows := m.rows()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.helpOn = !m.helpOn
	case "j", "down":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = max(len(rows)-1, 0)
	case "r":
		return m, m.refresh()
	case "a":
		m.showAll = !m.showAll
		m.clampCursor()
	case "p":
		if m.o.Categorize != nil {
			m.highOnly = !m.highOnly
			m.clampCursor()
		}
	case "m":
		if m.o.Categorize != nil {
			m.summaries = !m.summaries
		}
	case "x":
		if row, ok := m.selected(); ok && row.req >= 0 && m.o.Categorize != nil {
			return m, m.recategorize(row)
		}
	case "c":
		if row, ok := m.selected(); ok && row.req >= 0 {
			m.markSeen(row)
		}
	case "C":
		m.markAllSeen()
	case "s":
		if row, ok := m.selected(); ok && row.req >= 0 {
			m.subscribe(row)
		}
	case "o":
		if row, ok := m.selected(); ok {
			e := m.entries[row.entry]
			u := github.RepoRef{Domain: e.Repo.Domain, Repo: e.Repo.Repo}.URL()
			if row.req >= 0 {
				u = e.Requests[row.req].Ref.URL
			}
			if err := m.o.Open(u); err != nil {
				m.errMsg = err.Error()
			}
		}
	}
	return m, nil
}

// markSeen commits one review request as seen.
func (m *reviewsModel) markSeen(row reviewRow) {
	e := &m.entries[row.entry]
	r := &e.Requests[row.req]
	if err := m.o.Store.MarkSeen(e.Repo.Domain, e.Repo.Repo, r.Ref.Number); err != nil {
		m.errMsg = err.Error()
		return
	}
	r.Seen = true
	m.clampCursor()
	m.statusMsg = fmt.Sprintf("committed %s#%d", e.Repo.Repo, r.Ref.Number)
}

// markAllSeen commits every review request of every repository whose fetch
// succeeded, and drops seen numbers that are no longer open.
func (m *reviewsModel) markAllSeen() {
	count := 0
	for i := range m.entries {
		e := &m.entries[i]
		if e.Err != nil {
			continue
		}
		numbers := make([]int, 0, len(e.Requests))
		for _, r := range e.Requests {
			numbers = append(numbers, r.Ref.Number)
		}
		if err := m.o.Store.MarkSeen(e.Repo.Domain, e.Repo.Repo, numbers...); err != nil {
			m.errMsg = err.Error()
			return
		}
		if err := m.o.Store.PruneSeen(e.Repo.Domain, e.Repo.Repo, numbers); err != nil {
			m.errMsg = err.Error()
			return
		}
		if err := m.o.Store.PruneCategories(e.Repo.Domain, e.Repo.Repo, numbers); err != nil {
			m.errMsg = err.Error()
			return
		}
		for j := range e.Requests {
			if !e.Requests[j].Seen {
				count++
			}
			e.Requests[j].Seen = true
		}
	}
	m.clampCursor()
	m.statusMsg = fmt.Sprintf("committed %s", plural(count, "review request"))
}

// subscribe adds the review request to the pulls watch.
func (m *reviewsModel) subscribe(row reviewRow) {
	e := &m.entries[row.entry]
	r := &e.Requests[row.req]
	_, err := m.o.Store.SubscribePull(store.Pull{URL: r.Ref.URL, Domain: r.Ref.Domain, Repo: r.Ref.Repo, Number: r.Ref.Number})
	if err != nil {
		m.errMsg = err.Error()
		return
	}
	r.Watching = true
	m.statusMsg = "subscribed " + r.Ref.URL
}

// items draws every cursor row as a block of rows.
func (m *reviewsModel) items() [][]string {
	rows := m.rows()
	items := make([][]string, len(rows))
	for i, row := range rows {
		first := i == 0 || rows[i-1].entry != row.entry
		items[i] = m.renderRow(row, first, i == m.cursor)
	}
	return items
}

// frame builds the screen layout for the current state.
func (m *reviewsModel) frame() frame {
	total, newCount, high := 0, 0, 0
	for _, e := range m.entries {
		for _, r := range e.Requests {
			total++
			if !r.Seen {
				newCount++
			}
			if r.isHigh() {
				high++
			}
		}
	}
	header := fmt.Sprintf("reviews · %s across %s (%d total pending)",
		plural(newCount, "new review request"), plural(len(m.entries), "repo"), total)
	if m.o.Categorize != nil {
		header += fmt.Sprintf(" · %d high", high)
		if n := len(m.categorizing); n > 0 {
			header += " · " + plural(n, "hook running")
		}
	}
	if m.showAll {
		header += " · all shown"
	}
	if m.highOnly {
		header += " · high only"
	}
	if m.summaries {
		header += " · summaries shown"
	}
	if !m.refreshedAt.IsZero() {
		header += " · refreshed " + m.refreshedAt.Local().Format("15:04:05")
	}
	help := "c commit · s subscribe pull · o open · r refresh · a toggle all"
	moreHelp := "j/k move · C commit all"
	if m.o.Categorize != nil {
		help += " · p high only · m summaries"
		moreHelp += " · x categorize again"
	}
	return frame{
		width:        m.width,
		height:       m.height,
		header:       header,
		loading:      m.loading,
		errMsg:       m.errMsg,
		status:       m.statusMsg,
		helpExpanded: m.helpOn,
		help:         help,
		moreHelp:     moreHelp,
	}
}

func (m *reviewsModel) View() string {
	m.clampCursor()
	f := m.frame()
	items := m.items()
	var body []string
	switch {
	case len(m.entries) == 0 && m.loading && m.refreshedAt.IsZero():
		body = []string{dimStyle.Render("loading…")}
	case len(m.entries) == 0:
		body = []string{dimStyle.Render("no watched repositories · run: ghw reviews subscribe <repo>")}
	case len(items) == 0 && m.highOnly:
		body = []string{dimStyle.Render("no high priority review requests · press p to show every priority")}
	case len(items) == 0:
		body = []string{dimStyle.Render("no new review requests · press a to show every request")}
	default:
		body = m.vp.render(items, m.cursor, f.bodyHeight())
	}
	return f.render(body)
}

// renderRow draws one cursor row. The first row of a repository starts with
// the repository header.
func (m *reviewsModel) renderRow(row reviewRow, first, selected bool) []string {
	e := m.entries[row.entry]
	var rows []string
	if first {
		if row.entry > 0 {
			rows = append(rows, "")
		}
		head := boldStyle.Render(e.Repo.Repo)
		if e.Repo.Domain != "github.com" {
			head += " " + dimStyle.Render(e.Repo.Domain)
		}
		rows = append(rows, head)
	}
	prefix := "  "
	if selected {
		prefix = selectedStyle.Render("▸ ")
	}
	if row.req < 0 {
		if e.Err != nil {
			rows = append(rows, prefix+errStyle.Render(e.Err.Error()))
		} else {
			rows = append(rows, prefix+dimStyle.Render("No review requests"))
		}
		return rows
	}
	r := e.Requests[row.req]
	bullet := " "
	if !r.Seen {
		bullet = bulletStyle.Render("•")
	}
	var parts []string
	if r.Draft {
		parts = append(parts, dimItalic.Render("[DRAFT]"))
	}
	if tag := categoryTag(r); tag != "" {
		parts = append(parts, tag)
	}
	title := r.Title
	if selected {
		title = selectedStyle.Render(title)
	}
	parts = append(parts, title, dimStyle.Render("@"+r.Author))
	if r.Watching {
		parts = append(parts, greenStyle.Render("(watching)"))
	}
	line := prefix + bullet + " "
	for i, p := range parts {
		if i > 0 {
			line += " "
		}
		line += p
	}
	rows = append(rows, line)
	rows = append(rows, "    "+dimStyle.Render(r.Ref.URL))
	switch {
	case r.CategoryErr != nil:
		rows = append(rows, "    "+errStyle.Render(r.CategoryErr.Error()))
	case m.summaries && r.Category != nil && r.Category.Summary != "":
		for _, line := range wrapIndent(r.Category.Summary, 4, m.width) {
			rows = append(rows, "    "+dimStyle.Render(line))
		}
	}
	return rows
}

// wrapIndent wraps plain text onto rows that fit the width after an indent
// of the given size. It breaks between words, and inside a word only when
// the word alone is wider than the row. A width of zero means unknown, and
// the text stays on one row.
func wrapIndent(text string, indent, width int) []string {
	limit := width - indent
	if width <= 0 || limit < 1 {
		return []string{text}
	}
	return strings.Split(ansi.Wrap(text, limit, ""), "\n")
}

// categoryTag draws the hook result of a request: a three-bar meter for the
// priority with the category label, such as [▮▮▮ security]. A high priority
// fills three bars in red, a normal one two bars in cyan, and a low one one
// bar in the dim style. There is nothing when there is no result. A run in
// progress shows [categorizing…].
func categoryTag(r ReviewRequest) string {
	if r.Categorizing {
		return dimItalic.Render("[categorizing…]")
	}
	if r.Category == nil {
		return ""
	}
	c := r.Category
	switch c.Priority {
	case "high":
		return redStyle.Bold(true).Render("[" + join("▮▮▮", c.Category) + "]")
	case "low":
		return dimItalic.Render("[" + join("▮▯▯", c.Category) + "]")
	}
	return cyanStyle.Render("[" + join("▮▮▯", c.Category) + "]")
}

// join puts a space between two words and leaves out an empty one.
func join(a, b string) string {
	if b == "" {
		return a
	}
	return a + " " + b
}
