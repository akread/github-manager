package tui

import (
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github-manager/internal/github"
	"github-manager/internal/store"
)

// ReviewRequest is one review request with its watch state.
type ReviewRequest struct {
	github.ReviewRequest
	Seen     bool // committed as seen
	Watching bool // subscribed under pulls
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
	return &reviewsModel{o: o, showAll: o.Expanded}
}

func (m *reviewsModel) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(m.o.Interval))
}

// refresh loads the review requests of every watched repository in the
// background, then marks the seen and the watched ones from the store.
func (m *reviewsModel) refresh() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	o := m.o
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
			for j := range e.Requests {
				r := &e.Requests[j]
				r.Seen = seen[r.Ref.Number]
				r.Watching = watching[r.Ref.URL]
			}
		}
		return reviewsLoadedMsg{entries: entries, at: time.Now()}
	}
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
	total, newCount := 0, 0
	for _, e := range m.entries {
		for _, r := range e.Requests {
			total++
			if !r.Seen {
				newCount++
			}
		}
	}
	header := fmt.Sprintf("reviews · %s across %s (%d total pending)",
		plural(newCount, "new review request"), plural(len(m.entries), "repo"), total)
	if m.showAll {
		header += " · all shown"
	}
	if !m.refreshedAt.IsZero() {
		header += " · refreshed " + m.refreshedAt.Local().Format("15:04:05")
	}
	return frame{
		width:        m.width,
		height:       m.height,
		header:       header,
		loading:      m.loading,
		errMsg:       m.errMsg,
		status:       m.statusMsg,
		helpExpanded: m.helpOn,
		help:         "j/k move · c commit · C commit all · s subscribe pull · o open · r refresh · a toggle all",
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
	return rows
}
