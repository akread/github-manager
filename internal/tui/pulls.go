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
	Load       func(pulls []store.Pull) []PullEntry
	Interval   time.Duration
	Expanded   bool // show every pull request, not only those with updates
	NoComments bool // hide the text of new comments
	Mine       bool // show only the pull requests of the authenticated user
	// Open opens a URL in the browser. Nil means the system default.
	Open func(url string) error
	// Merge merges a pull request with a method: merge, squash, or rebase.
	// Nil disables the merge shortcut.
	Merge func(ref github.PullRef, method string) error
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
	entry      PullEntry
	afterMerge bool // the reload follows a merge started with M
}

// pullMergedMsg reports the result of a merge started with M.
type pullMergedMsg struct {
	pull store.Pull
	err  error
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
	showMine     bool // hide pull requests from other authors

	input   textinput.Model
	inputOn bool

	filter   textinput.Model // the text to match; it stays set after the input closes
	filterOn bool            // the filter input has the focus

	mergeOn     bool       // the merge prompt is open
	mergePull   store.Pull // the pull request the merge prompt is for
	mergeMethod string     // the chosen method; empty while the prompt waits for one

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
	fi := textinput.New()
	fi.Prompt = "filter: "
	fi.Placeholder = "title, repo, number, author, or url"
	fi.Cursor.SetMode(cursor.CursorStatic)
	return &pullsModel{o: o, showAll: o.Expanded, showComments: !o.NoComments, showMine: o.Mine, input: ti, filter: fi}
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

// filterText is the filter the user typed, without the spaces around it.
func (m *pullsModel) filterText() string {
	return strings.TrimSpace(m.filter.Value())
}

// matches reports whether the entry matches the filter text. The match is a
// case-insensitive substring match on the title, the repo and number, the
// author, and the url. An empty filter matches every entry.
func (m *pullsModel) matches(e PullEntry) bool {
	q := strings.ToLower(m.filterText())
	if q == "" {
		return true
	}
	hay := []string{fmt.Sprintf("%s#%d", e.Pull.Repo, e.Pull.Number), e.Pull.URL}
	if e.Status != nil {
		hay = append(hay, e.Status.Title, "@"+e.Status.Author)
	}
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), q) {
			return true
		}
	}
	return false
}

// visible returns the indices of the entries to show. An entry whose fetch
// failed always shows: its author is unknown, and the error needs attention.
// The filter text applies to every entry, also to the failed ones.
func (m *pullsModel) visible() []int {
	var out []int
	for i, e := range m.entries {
		if !m.matches(e) {
			continue
		}
		if e.Err != nil {
			out = append(out, i)
			continue
		}
		if e.Status == nil || (m.showMine && !e.Status.Ours) {
			continue
		}
		if m.showAll || e.Status.HasUpdates() {
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
		m.filter.Width = max(msg.Width-len(m.filter.Prompt)-2, 10)
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
		if msg.afterMerge {
			m.reportMerge(msg.entry)
		}
		return m, nil
	case pullMergedMsg:
		return m, m.merged(msg)
	case tea.KeyMsg:
		if m.inputOn {
			return m.updateInput(msg)
		}
		if m.mergeOn {
			return m.updateMerge(msg)
		}
		if m.filterOn {
			return m.updateFilter(msg)
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
	if m.filterOn {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updateMouse scrolls on the wheel and moves the cursor on a left click.
func (m *pullsModel) updateMouse(msg tea.MouseMsg) {
	if m.inputOn || m.mergeOn {
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
	case "A":
		m.showMine = !m.showMine
		m.clampCursor()
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
	case "M":
		if i := m.selected(); i >= 0 {
			m.openMerge(i)
		}
	case "/":
		m.filterOn = true
		m.filter.CursorEnd()
		return m, m.filter.Focus()
	case "esc":
		if m.filterText() != "" {
			m.filter.Reset()
			m.clampCursor()
			m.statusMsg = "filter cleared"
		}
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

// openMerge opens the merge prompt for one pull request. Only an open pull
// request whose last refresh succeeded can be merged.
func (m *pullsModel) openMerge(i int) {
	e := m.entries[i]
	switch {
	case m.o.Merge == nil:
		m.errMsg = "merge is not available"
	case e.Err != nil || e.Status == nil:
		m.errMsg = "cannot merge: the last refresh of this pull request failed"
	case e.Status.Merged:
		m.errMsg = "cannot merge: the pull request is already merged"
	case e.Status.InMergeQueue:
		m.errMsg = "cannot merge: the pull request is already in the merge queue"
	case e.Status.State != "open":
		m.errMsg = "cannot merge: the pull request is not open"
	default:
		m.mergeOn = true
		m.mergePull = e.Pull
		m.mergeMethod = ""
	}
}

// updateMerge handles the keys while the merge prompt is open. The prompt
// has two steps: a method key chooses the method, then y confirms and
// starts the merge in the background. Esc closes the prompt in both steps.
func (m *pullsModel) updateMerge(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q", "n":
		m.mergeOn = false
		return m, nil
	}
	if m.mergeMethod == "" {
		switch msg.String() {
		case "m":
			m.mergeMethod = "merge"
		case "s":
			m.mergeMethod = "squash"
		case "r":
			m.mergeMethod = "rebase"
		}
		return m, nil
	}
	if msg.String() != "y" {
		return m, nil
	}
	m.mergeOn = false
	p, method := m.mergePull, m.mergeMethod
	m.statusMsg = fmt.Sprintf("merging %s#%d (%s)…", p.Repo, p.Number, method)
	merge := m.o.Merge
	return m, func() tea.Msg {
		ref := github.PullRef{URL: p.URL, Domain: p.Domain, Repo: p.Repo, Number: p.Number}
		return pullMergedMsg{pull: p, err: merge(ref, method)}
	}
}

// merged shows the result of a merge. After a success it reloads the pull
// request, so the list shows its new state: merged, or queued when the base
// branch has a merge queue.
func (m *pullsModel) merged(msg pullMergedMsg) tea.Cmd {
	label := fmt.Sprintf("%s#%d", msg.pull.Repo, msg.pull.Number)
	if msg.err != nil {
		m.statusMsg = ""
		m.errMsg = "merge " + label + ": " + msg.err.Error()
		return nil
	}
	m.errMsg = ""
	m.statusMsg = "merge accepted for " + label + " · refreshing…"
	load := m.o.Load
	p := msg.pull
	return func() tea.Msg {
		return pullLoadedMsg{entry: load([]store.Pull{p})[0], afterMerge: true}
	}
}

// reportMerge sets the status row from the state of a pull request after a
// merge. gh adds the pull request to the merge queue when the base branch
// has one, so the pull request is then open and queued, not merged.
func (m *pullsModel) reportMerge(e PullEntry) {
	label := fmt.Sprintf("%s#%d", e.Pull.Repo, e.Pull.Number)
	switch {
	case e.Err != nil || e.Status == nil:
		m.statusMsg = "merge accepted for " + label
		m.errMsg = "refresh after merge failed"
		if e.Err != nil {
			m.errMsg += ": " + e.Err.Error()
		}
	case e.Status.Merged:
		m.statusMsg = "merged " + label
	case e.Status.InMergeQueue:
		m.statusMsg = "added " + label + " to the merge queue"
	default:
		m.statusMsg = "merge accepted for " + label + " · not merged yet"
	}
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

// updateFilter handles the keys while the filter input has the focus. The
// list filters as the user types. Enter keeps the filter and returns to the
// list. Esc clears the filter and returns to the list.
func (m *pullsModel) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errMsg = ""
	m.statusMsg = ""
	switch msg.String() {
	case "esc", "ctrl+c":
		m.filterOn = false
		m.filter.Blur()
		m.filter.Reset()
		m.clampCursor()
		return m, nil
	case "enter":
		m.filterOn = false
		m.filter.Blur()
		m.clampCursor()
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.clampCursor()
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
	if m.showMine {
		header += " · mine only"
	}
	if !m.showComments {
		header += " · comments hidden"
	}
	if q := m.filterText(); q != "" && !m.filterOn {
		header += fmt.Sprintf(" · filter %q", q)
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
		help:         "c commit · s subscribe · o open · M merge · r refresh · / filter · a toggle all · A toggle mine · m toggle comments",
		moreHelp:     "j/k move · C commit all · u unsubscribe",
	}
	if m.filterText() != "" {
		f.help += " · esc clear filter"
	}
	switch {
	case m.inputOn:
		f.input = m.input.View()
		f.help = "enter subscribe · esc cancel"
		f.moreHelp = ""
		f.helpExpanded = false
		f.noTail = true
	case m.filterOn:
		f.input = m.filter.View()
		f.help = "enter keep filter · esc clear filter"
		f.moreHelp = ""
		f.helpExpanded = false
		f.noTail = true
	case m.mergeOn && m.mergeMethod == "":
		f.input = fmt.Sprintf("merge %s#%d with:", m.mergePull.Repo, m.mergePull.Number)
		f.help = "m merge · s squash · r rebase · esc cancel"
		f.moreHelp = ""
		f.helpExpanded = false
		f.noTail = true
	case m.mergeOn:
		f.input = fmt.Sprintf("are you sure you want to %s %s#%d?", m.mergeMethod, m.mergePull.Repo, m.mergePull.Number)
		f.help = "y merge · n cancel"
		f.moreHelp = ""
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
	case len(items) == 0 && m.filterText() != "":
		body = []string{dimStyle.Render("no pull requests match the filter · press esc to clear it")}
	case len(items) == 0 && m.showMine:
		body = []string{dimStyle.Render("no pull requests of yours to show · press A to show every author")}
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
	case s.InMergeQueue:
		state = cyanStyle.Render("[QUEUED]")
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
	if s.InMergeQueue {
		text := "In the merge queue"
		if s.QueuePosition > 0 {
			text += fmt.Sprintf(" · position %d", s.QueuePosition)
		}
		if s.QueueState != "" {
			text += " · " + strings.ToLower(strings.ReplaceAll(s.QueueState, "_", " "))
		}
		rows = append(rows, indent+cyanStyle.Render(text))
	}

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
		if s.NewReviewRequest {
			rows = append(rows, indent+newStyle.Render("Review requested"))
		} else {
			rows = append(rows, indent+dimItalic.Render("Review requested"))
		}
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
