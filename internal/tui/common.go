// Package tui is the terminal UI for the watch commands. One model shows
// the watched pull requests, the other shows the review requests of the
// watched repositories. Both refresh on a timer and let the user commit
// activity as seen per item.
package tui

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimUnderline  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Underline(true)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	newStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Italic(true)
	redStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Italic(true)
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Italic(true)
	magentaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Italic(true)
	cyanStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Italic(true)
	orangeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Italic(true)
	dimItalic     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	bulletStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
)

// tickMsg starts a scheduled refresh.
type tickMsg struct{}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// openURL opens a URL in the default browser.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

// fit cuts a line to the width. A width of zero means unknown, and the line
// stays as it is.
func fit(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// plural returns "N word" or "N words".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// bodyTop is the screen row where the body starts: the header and the blank
// row under it come first.
const bodyTop = 2

// wheelStep is the row count one mouse wheel notch scrolls.
const wheelStep = 3

// viewport keeps the cursor item visible in a list of multi-row items and
// returns the rows to draw.
type viewport struct {
	offset int
}

func rowCount(items [][]string, from, to int) int {
	n := 0
	for i := from; i <= to && i < len(items); i++ {
		n += len(items[i])
	}
	return n
}

// maxOffset is the largest offset that still fills the height, so the view
// cannot scroll past the end of the list.
func (v *viewport) maxOffset(items [][]string, height int) int {
	rows := 0
	for i := len(items) - 1; i >= 0; i-- {
		rows += len(items[i])
		if rows > height {
			return min(i+1, len(items)-1)
		}
	}
	return 0
}

// lastVisible is the last item that fits in full from the offset. It is at
// least the offset item.
func (v *viewport) lastVisible(items [][]string, height int) int {
	last := v.offset
	rows := 0
	for i := v.offset; i < len(items); i++ {
		rows += len(items[i])
		if rows > height && i > v.offset {
			break
		}
		last = i
	}
	return last
}

// scrollBy moves the view by delta rows and returns the cursor moved into
// the visible window.
func (v *viewport) scrollBy(items [][]string, cursor, delta, height int) int {
	if len(items) == 0 {
		return 0
	}
	// walk whole items so an item does not split at the top
	if delta < 0 {
		for delta < 0 && v.offset > 0 {
			v.offset--
			delta += len(items[v.offset])
		}
	} else {
		limit := v.maxOffset(items, height)
		for delta > 0 && v.offset < limit {
			delta -= len(items[v.offset])
			v.offset++
		}
	}
	v.offset = min(max(v.offset, 0), len(items)-1)
	if cursor < v.offset {
		cursor = v.offset
	}
	if last := v.lastVisible(items, height); cursor > last {
		cursor = last
	}
	return cursor
}

// itemAt returns the item drawn at a body row, counted from the top of the
// body, or -1 when the row is empty.
func (v *viewport) itemAt(items [][]string, row, height int) int {
	if row < 0 || row >= height {
		return -1
	}
	rows := 0
	for i := v.offset; i < len(items); i++ {
		rows += len(items[i])
		if row < rows {
			return i
		}
	}
	return -1
}

func (v *viewport) render(items [][]string, cursor, height int) []string {
	if len(items) == 0 || height <= 0 {
		v.offset = 0
		return nil
	}
	cursor = min(max(cursor, 0), len(items)-1)
	v.offset = min(max(v.offset, 0), cursor)
	rows := 0
	for i := v.offset; i <= cursor; i++ {
		rows += len(items[i])
	}
	for rows > height && v.offset < cursor {
		rows -= len(items[v.offset])
		v.offset++
	}
	out := make([]string, 0, height)
	for i := v.offset; i < len(items) && len(out) < height; i++ {
		for _, line := range items[i] {
			if len(out) >= height {
				break
			}
			out = append(out, line)
		}
	}
	return out
}

// frame lays out the screen: a header, the body rows, an optional input
// row, a status or error row, and the help row.
type frame struct {
	width, height int
	header        string
	loading       bool
	input         string // empty when no input is open
	errMsg        string
	status        string
	help          string // row 1: the shortcuts that act on the selected item
	viewHelp      string // row 2: the shortcuts that act on the whole view; empty for one row
	helpExpanded  bool   // the user pressed ? to wrap every item onto more rows
	noTail        bool   // leave out the "? help · q quit" tail
}

// helpSep separates the items of a help text.
const helpSep = " · "

// toggleHelp formats a help item for a shortcut that turns a view option
// on or off. A dot after the label shows the state: filled is on, empty
// is off. An action item has no dot, so the dot alone tells the two apart.
func toggleHelp(key, label string, on bool) string {
	dot := "○"
	if on {
		dot = "●"
	}
	return key + " " + label + " " + dot
}

// wrapHelp splits a help text into rows that fit the width. It breaks only
// between items, so a shortcut and its label stay on one row. A width of
// zero means unknown, and the text stays on one row.
func wrapHelp(help string, width int) []string {
	if width <= 0 || ansi.StringWidth(help) <= width {
		return []string{help}
	}
	var rows []string
	cur := ""
	for _, item := range strings.Split(help, helpSep) {
		switch {
		case cur == "":
			cur = item
		case ansi.StringWidth(cur+helpSep+item) <= width:
			cur += helpSep + item
		default:
			rows = append(rows, cur)
			cur = item
		}
	}
	return append(rows, cur)
}

// quitTail is the end of the help row when every item fits. helpTail
// replaces it when an item is cut, or when the help is expanded, so the
// user sees the key that toggles the help only when it does something.
const (
	quitTail = "q quit"
	helpTail = "? help · q quit"
)

// helpGroups returns the help rows before any wrap or cut: the item
// shortcuts, then the view shortcuts when the frame has them.
func (f frame) helpGroups() []string {
	if f.viewHelp == "" {
		return []string{f.help}
	}
	return []string{f.help, f.viewHelp}
}

// helpRows returns the help rows. The item shortcuts take the first row
// and the view shortcuts the second. The tail sits at the right edge of the
// last row. By default each group takes one row and is cut with an ellipsis
// when it does not fit. When expanded, each group wraps onto as many rows
// as it needs; the last group wraps in the space left of the tail.
func (f frame) helpRows() []string {
	groups := f.helpGroups()
	last := len(groups) - 1
	if f.noTail {
		if f.width <= 0 {
			return groups
		}
		for i, g := range groups {
			groups[i] = ansi.Truncate(g, f.width, "…")
		}
		return groups
	}
	if f.width <= 0 {
		groups[last] += "  " + quitTail
		return groups
	}
	// limit is the width a group can use on one row next to the tail.
	limit := func(i int, tail string) int {
		if i < last {
			return f.width
		}
		return max(f.width-ansi.StringWidth(tail)-2, 1)
	}
	tail := quitTail
	for i, g := range groups {
		if ansi.StringWidth(g) > limit(i, tail) {
			tail = helpTail
			break
		}
	}
	var rows []string
	for i, g := range groups {
		lim := limit(i, tail)
		switch {
		case f.helpExpanded:
			rows = append(rows, wrapHelp(g, lim)...)
		case ansi.StringWidth(g) <= lim:
			rows = append(rows, g)
		default:
			rows = append(rows, ansi.Truncate(g, lim, "…"))
		}
	}
	end := len(rows) - 1
	pad := f.width - ansi.StringWidth(rows[end]) - ansi.StringWidth(tail)
	rows[end] += strings.Repeat(" ", max(pad, 1)) + tail
	return rows
}

// bodyHeight is the row count left for the body.
func (f frame) bodyHeight() int {
	h := f.height - 3 - len(f.helpRows()) // header, blank, status, help rows
	if f.input != "" {
		h--
	}
	return max(h, 3)
}

func (f frame) render(body []string) string {
	var b strings.Builder
	head := f.header
	if f.loading {
		head += "  " + dimStyle.Render("refreshing…")
	}
	b.WriteString(fit(headerStyle.Render(head), f.width))
	b.WriteString("\n\n")
	h := f.bodyHeight()
	for i := 0; i < h; i++ {
		if i < len(body) {
			b.WriteString(fit(body[i], f.width))
		}
		b.WriteString("\n")
	}
	if f.input != "" {
		b.WriteString(fit(f.input, f.width) + "\n")
	}
	switch {
	case f.errMsg != "":
		b.WriteString(fit(errStyle.Render(f.errMsg), f.width))
	case f.status != "":
		b.WriteString(fit(dimStyle.Render(f.status), f.width))
	}
	b.WriteString("\n")
	rows := f.helpRows()
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fit(helpStyle.Render(row), f.width))
	}
	return b.String()
}
