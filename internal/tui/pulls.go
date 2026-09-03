package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github-manager/internal/github"
	"github-manager/internal/store"
)

// PullEntry is one watched pull request with its fetched status, or the
// error from the fetch.
type PullEntry struct {
	Pull   store.Pull
	Status *github.PullStatus
	Err    error
}

// PullsOptions configures the pulls watch.
type PullsOptions struct {
	Store *store.Store
	// Load fetches the status of each pull request. The result has one
	// entry per input, in the same order.
	Load     func(pulls []store.Pull) []PullEntry
	Interval time.Duration
	Expanded bool // show every pull request, not only those with updates
	Comments bool // show the text of new comments
	// Open opens a URL in the browser. Nil means the system default.
	Open func(url string) error
}

// PullLoader returns a Load function that uses the gh client. It fetches up
// to eight pull requests at the same time.
func PullLoader(c *github.Client, excluded func(domain string) []string) func([]store.Pull) []PullEntry {
	return func(pulls []store.Pull) []PullEntry {
		out := make([]PullEntry, len(pulls))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, p := range pulls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ref := github.PullRef{URL: p.URL, Domain: p.Domain, Repo: p.Repo, Number: p.Number}
				st, err := c.LoadPull(ref, p.Since, excluded(p.Domain))
				out[i] = PullEntry{Pull: p, Status: st, Err: err}
			}()
		}
		wg.Wait()
		return out
	}
}

// RunPulls opens the pulls watch.
func RunPulls(o PullsOptions) error {
	m := newPullsModel(o)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

type pullsLoadedMsg struct {
	entries []PullEntry
	at      time.Time
	err     error
}

type pullLoadedMsg struct {
	entry PullEntry
}

type pullsModel struct {
	o       PullsOptions
	entries []PullEntry
	cursor  int // index into visible()
	vp      viewport

	loading     bool
	refreshedAt time.Time

	showAll      bool
	showComments bool

	input   textinput.Model
	inputOn bool

	errMsg    string
	statusMsg string
	helpOn    bool // the user pressed ? to expand the help onto more rows

	width, height int
}

func newPullsModel(o PullsOptions) *pullsModel {
	if o.Open == nil {
		o.Open = openURL
	}
	if o.Interval <= 0 {
		o.Interval = 5 * time.Minute
	}
	ti := textinput.New()
	ti.Prompt = "subscribe url: "
	ti.Placeholder = "https://github.com/owner/name/pull/123"
	ti.Cursor.SetMode(cursor.CursorStatic)
	return &pullsModel{o: o, showAll: o.Expanded, showComments: o.Comments, input: ti}
}

func (m *pullsModel) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(m.o.Interval))
}

// refresh loads every watched pull request in the background.
func (m *pullsModel) refresh() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	o := m.o
	return func() tea.Msg {
		pulls, err := o.Store.ListPulls()
		if err != nil {
			return pullsLoadedMsg{err: err, at: time.Now()}
		}
		return pullsLoadedMsg{entries: o.Load(pulls), at: time.Now()}
	}
}

// visible returns the indices of the entries to show.
func (m *pullsModel) visible() []int {
	var out []int
	for i, e := range m.entries {
		if m.showAll || e.Err != nil || (e.Status != nil && e.Status.HasUpdates()) {
			out = append(out, i)
		}
	}
	return out
}

func (m *pullsModel) clampCursor() {
	n := len(m.visible())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// selected returns the index into entries of the cursor item, or -1.
func (m *pullsModel) selected() int {
	vis := m.visible()
	if len(vis) == 0 {
		return -1
	}
	m.clampCursor()
	return vis[m.cursor]
}

func (m *pullsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick(m.o.Interval))
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(msg.Width-len(m.input.Prompt)-2, 10)
		return m, nil
	case pullsLoadedMsg:
		m.loading = false
		m.refreshedAt = msg.at
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.clampCursor()
		return m, nil
	case pullLoadedMsg:
		m.upsert(msg.entry)
		return m, nil
	case tea.KeyMsg:
		if m.inputOn {
			return m.updateInput(msg)
		}
		return m.updateKeys(msg)
	case tea.MouseMsg:
		m.updateMouse(msg)
		return m, nil
	}
	if m.inputOn {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updateMouse scrolls on the wheel and moves the cursor on a left click.
func (m *pullsModel) updateMouse(msg tea.MouseMsg) {
	if m.inputOn {
		return
	}
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

// upsert replaces the entry with the same URL, or appends a new one.
func (m *pullsModel) upsert(e PullEntry) {
	for i := range m.entries {
		if m.entries[i].Pull.URL == e.Pull.URL {
			m.entries[i] = e
			return
		}
	}
	m.entries = append(m.entries, e)
}

func (m *pullsModel) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errMsg = ""
	m.statusMsg = ""
	vis := m.visible()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.helpOn = !m.helpOn
	case "j", "down":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = max(len(vis)-1, 0)
	case "r":
		return m, m.refresh()
	case "a":
		m.showAll = !m.showAll
		m.clampCursor()
	case "m":
		m.showComments = !m.showComments
	case "c":
		if i := m.selected(); i >= 0 {
			m.commit(i)
		}
	case "C":
		m.commitAll()
	case "s":
		m.inputOn = true
		m.input.Reset()
		return m, m.input.Focus()
	case "u":
		if i := m.selected(); i >= 0 {
			e := m.entries[i]
			if err := m.o.Store.UnsubscribePull(e.Pull.URL); err != nil {
				m.errMsg = err.Error()
				break
			}
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			m.clampCursor()
			m.statusMsg = "unsubscribed " + e.Pull.URL
		}
	case "o":
		if i := m.selected(); i >= 0 {
			if err := m.o.Open(m.entries[i].Pull.URL); err != nil {
				m.errMsg = err.Error()
			}
		}
	}
	return m, nil
}

// commit records the activity of one pull request as seen. A closed pull
// request is unsubscribed instead, because it has no more activity to
// watch.
func (m *pullsModel) commit(i int) {
	e := &m.entries[i]
	if e.Err != nil || e.Status == nil {
		m.errMsg = "cannot commit: the last refresh of this pull request failed"
		return
	}
	label := fmt.Sprintf("%s#%d", e.Pull.Repo, e.Pull.Number)
	if e.Status.State != "open" {
		if err := m.o.Store.UnsubscribePull(e.Pull.URL); err != nil {
			m.errMsg = err.Error()
			return
		}
		m.entries = append(m.entries[:i], m.entries[i+1:]...)
		m.clampCursor()
		m.statusMsg = "unsubscribed " + label + " (closed)"
		return
	}
	if err := m.o.Store.SetPullSince(e.Pull.URL, e.Status.FetchedAt); err != nil {
		m.errMsg = err.Error()
		return
	}
	e.Pull.Since = e.Status.FetchedAt
	e.Status.MarkSeen()
	m.clampCursor()
	m.statusMsg = "committed " + label
}

// commitAll commits every pull request whose last fetch succeeded.
func (m *pullsModel) commitAll() {
	committed, closed := 0, 0
	kept := make([]PullEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.Err != nil || e.Status == nil {
			kept = append(kept, e)
			continue
		}
		if e.Status.State != "open" {
			if err := m.o.Store.UnsubscribePull(e.Pull.URL); err != nil {
				m.errMsg = err.Error()
				kept = append(kept, e)
				continue
			}
			closed++
			continue
		}
		if err := m.o.Store.SetPullSince(e.Pull.URL, e.Status.FetchedAt); err != nil {
			m.errMsg = err.Error()
			kept = append(kept, e)
			continue
		}
		e.Pull.Since = e.Status.FetchedAt
		e.Status.MarkSeen()
		committed++
		kept = append(kept, e)
	}
	m.entries = kept
	m.clampCursor()
	m.statusMsg = fmt.Sprintf("committed %s, unsubscribed %s", plural(committed, "pull request"), plural(closed, "closed pull request"))
}

func (m *pullsModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.inputOn = false
		m.input.Blur()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.input.Value())
		if raw == "" {
			m.inputOn = false
			m.input.Blur()
			return m, nil
		}
		ref, err := github.ParsePullURL(raw)
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		p, err := m.o.Store.SubscribePull(store.Pull{URL: ref.URL, Domain: ref.Domain, Repo: ref.Repo, Number: ref.Number})
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.inputOn = false
		m.input.Blur()
		m.statusMsg = "subscribed " + p.URL
		load := m.o.Load
		return m, func() tea.Msg {
			return pullLoadedMsg{entry: load([]store.Pull{p})[0]}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// items draws every visible pull request as a block of rows.
func (m *pullsModel) items() [][]string {
	vis := m.visible()
	items := make([][]string, len(vis))
	for i, idx := range vis {
		items[i] = m.renderPull(m.entries[idx], i == m.cursor)
	}
	return items
}

// frame builds the screen layout for the current state.
func (m *pullsModel) frame() frame {
	updates := 0
	for _, e := range m.entries {
		if e.Err != nil || (e.Status != nil && e.Status.HasUpdates()) {
			updates++
		}
	}
	header := fmt.Sprintf("pulls · %d of %d with updates", updates, len(m.entries))
	if m.showAll {
		header += " · all shown"
	}
	if !m.refreshedAt.IsZero() {
		header += " · refreshed " + m.refreshedAt.Local().Format("15:04:05")
	}
	f := frame{
		width:        m.width,
		height:       m.height,
		header:       header,
		loading:      m.loading,
		errMsg:       m.errMsg,
		status:       m.statusMsg,
		helpExpanded: m.helpOn,
		help:         "j/k move · c commit · C commit all · s subscribe · u unsubscribe · o open · r refresh · a toggle all · m toggle comments",
	}
	if m.inputOn {
		f.input = m.input.View()
		f.help = "enter subscribe · esc cancel"
		f.helpExpanded = false
		f.noTail = true
	}
	return f
}

func (m *pullsModel) View() string {
	m.clampCursor()
	f := m.frame()
	items := m.items()
	var body []string
	switch {
	case len(m.entries) == 0 && m.loading && m.refreshedAt.IsZero():
		body = []string{dimStyle.Render("loading…")}
	case len(m.entries) == 0:
		body = []string{dimStyle.Render("no watched pull requests · press s to subscribe")}
	case len(items) == 0:
		body = []string{dimStyle.Render("no updates · press a to show every pull request")}
	default:
		body = m.vp.render(items, m.cursor, f.bodyHeight())
	}
	return f.render(body)
}

// renderPull draws one pull request as a block of rows.
func (m *pullsModel) renderPull(e PullEntry, selected bool) []string {
	prefix := "  "
	if selected {
		prefix = selectedStyle.Render("▸ ")
	}
	const indent = "    "
	var rows []string

	if e.Err != nil {
		title := fmt.Sprintf("%s#%d", e.Pull.Repo, e.Pull.Number)
		rows = append(rows, prefix+errStyle.Render("[ERROR]")+" "+title)
		rows = append(rows, indent+dimStyle.Render(e.Pull.URL))
		rows = append(rows, indent+errStyle.Render(e.Err.Error()))
		return append(rows, "")
	}
	s := e.Status
	var state string
	switch {
	case s.Merged:
		state = magentaStyle.Render("[MERGED]")
	case s.State == "closed":
		state = redStyle.Render("[CLOSED]")
	case s.Draft:
		state = dimItalic.Render("[DRAFT]")
	default:
		state = greenStyle.Render("[OPEN]")
	}
	author := "@" + s.Author
	if s.Ours {
		author = dimUnderline.Render(author)
	} else {
		author = dimStyle.Render(author)
	}
	title := s.Title
	if selected {
		title = selectedStyle.Render(title)
	}
	rows = append(rows, fmt.Sprintf("%s%s %s %s", prefix, state, title, author))
	if s.Merged && s.MergeCommit != "" {
		rows = append(rows, indent+magentaStyle.Render(s.MergeCommit))
	}
	rows = append(rows, indent+dimStyle.Render(e.Pull.URL))

	comment := func(c github.Comment) {
		body := strings.Join(strings.Fields(c.Body), " ")
		if len([]rune(body)) > 100 {
			body = string([]rune(body)[:100]) + "…"
		}
		rows = append(rows, indent+"@"+c.Author+": "+body)
		rows = append(rows, indent+dimStyle.Render(c.URL))
	}
	if n := len(s.Comments); n > 0 {
		rows = append(rows, indent+newStyle.Render(plural(n, "new comment")))
		if m.showComments {
			for _, c := range s.Comments {
				comment(c)
			}
		}
	}
	if n := len(s.ReviewComments); n > 0 {
		rows = append(rows, indent+newStyle.Render(plural(n, "new review comment")))
		if m.showComments {
			for _, c := range s.ReviewComments {
				comment(c)
			}
		}
	}
	if s.ReviewRequested {
		rows = append(rows, indent+newStyle.Render("Review requested"))
	}
	if s.Approvals > 0 {
		text := plural(s.Approvals, "approval")
		if s.NewApprovals > 0 {
			rows = append(rows, indent+newStyle.Render(text))
		} else {
			rows = append(rows, indent+dimItalic.Render(text))
		}
	}
	if s.ChangesRequested > 0 {
		text := fmt.Sprintf("%d changes requested", s.ChangesRequested)
		if s.NewChangesRequested > 0 {
			rows = append(rows, indent+redStyle.Render(text))
		} else {
			rows = append(rows, indent+dimItalic.Render(text))
		}
	}
	if s.CheckState != github.CheckNone && !s.Merged {
		switch s.CheckState {
		case github.CheckFailure:
			rows = append(rows, indent+orangeStyle.Render("Required checks failed"))
		case github.CheckPending:
			rows = append(rows, indent+greenStyle.Render("Required checks running"))
		default:
			rows = append(rows, indent+dimItalic.Render("Required checks passed"))
		}
	}
	return append(rows, "")
}
