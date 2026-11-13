package tui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// textPosition identifies a cell in the detail pane's content. line is an
// absolute index into detailContent so scrolling does not change a selection.
type textPosition struct {
	line int
	col  int
}

// textSelection retains the content lines and viewport mapping captured at
// drag start so motion and copying stay stable even if new output arrives
// before the mouse button is released.
type textSelection struct {
	active   bool
	dragging bool
	button   tea.MouseButton
	anchor   textPosition
	focus    textPosition
	lines    []string
	view     selectionView
}

// selectionView is the detail viewport mapping frozen when a drag starts.
type selectionView struct {
	x, y       int
	width      int
	start, end int
}

func (v detailViewport) selectionView() selectionView {
	return selectionView{x: v.x, y: v.y, width: v.width, start: v.start, end: v.end}
}

func (v selectionView) viewport() detailViewport {
	return detailViewport{x: v.x, y: v.y, width: v.width, start: v.start, end: v.end}
}

// matches reports whether a live viewport still maps the same cells to the
// same content lines as the frozen one.
func (v selectionView) matches(other detailViewport) bool {
	return v.x == other.x && v.y == other.y && v.width == other.width &&
		v.start == other.start && v.end == other.end
}

type selectionCopiedMsg struct {
	lines int
	err   error
}

// terminalOutput serializes clipboard escape sequences with Bubble Tea's
// renderer. It embeds the file so Bubble Tea still recognises the output as a
// terminal.
type terminalOutput struct {
	*os.File
	mu sync.Mutex
}

func newTerminalOutput(file *os.File) *terminalOutput {
	return &terminalOutput{File: file}
}

func (w *terminalOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.File.Write(p)
}

// osc52Sequence copies text through the terminal. It is best effort: terminals
// that disable OSC 52 ignore the sequence without changing the UI.
func osc52Sequence(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

func copySelectionCmd(text string, out io.Writer) tea.Cmd {
	return func() tea.Msg {
		if out == nil {
			return selectionCopiedMsg{err: fmt.Errorf("clipboard output unavailable")}
		}
		_, err := io.WriteString(out, osc52Sequence(text))
		return selectionCopiedMsg{lines: strings.Count(text, "\n") + 1, err: err}
	}
}

func (s textSelection) bounds() (textPosition, textPosition) {
	if s.focus.line < s.anchor.line || (s.focus.line == s.anchor.line && s.focus.col < s.anchor.col) {
		return s.focus, s.anchor
	}
	return s.anchor, s.focus
}

func (s textSelection) hasRange() bool {
	return s.anchor != s.focus
}

// selectedText extracts the plain text inside the selection. Columns are
// display cells, so wide characters stay intact through ansi.Cut.
func selectedText(lines []string, selection textSelection) string {
	start, end := selection.bounds()
	if len(lines) == 0 || start.line >= len(lines) {
		return ""
	}
	end.line = min(end.line, len(lines)-1)

	var selected []string
	for line := start.line; line <= end.line; line++ {
		plain := strings.TrimRight(ansi.Strip(lines[line]), " ")
		width := ansi.StringWidth(plain)
		left, right := 0, width
		if line == start.line {
			left = min(max(0, start.col), width)
		}
		if line == end.line {
			right = min(max(left, end.col+1), width)
		}
		selected = append(selected, ansi.Cut(plain, left, right))
	}
	return strings.Join(selected, "\n")
}

// selectionCells returns the display-cell range selected on one content line.
func selectionCells(selection textSelection, line, width int) (int, int, bool) {
	start, end := selection.bounds()
	if line < start.line || line > end.line || width < 1 {
		return 0, 0, false
	}
	left, right := 0, width
	if line == start.line {
		left = min(max(0, start.col), width)
	}
	if line == end.line {
		right = min(max(left, end.col+1), width)
	}
	if right <= left {
		return 0, 0, false
	}
	return left, right, true
}

// highlightSelection redraws selected lines without their Markdown styling so
// the selection background survives nested ANSI resets.
func (m model) highlightSelection(content []string, start, end, width int) []string {
	visible := content[start:end]
	if !m.selection.active {
		return visible
	}
	highlighted := make([]string, len(visible))
	for i, line := range visible {
		left, right, ok := selectionCells(m.selection, start+i, width)
		if !ok {
			highlighted[i] = line
			continue
		}
		plain := ansi.Strip(line)
		highlighted[i] = ansi.Cut(plain, 0, left) +
			selectionHighlightStyle.Render(ansi.Cut(plain, left, right)) +
			ansi.Cut(plain, right, width)
	}
	return highlighted
}

type detailViewport struct {
	x, y       int
	width      int
	start, end int
	content    []string
}

func (v detailViewport) position(x, y int) (textPosition, bool) {
	if x < v.x || x >= v.x+v.width || y < v.y || y >= v.y+(v.end-v.start) {
		return textPosition{}, false
	}
	return textPosition{line: v.start + y - v.y, col: x - v.x}, true
}

func (v detailViewport) clamp(x, y int) textPosition {
	line := min(max(v.start+y-v.y, v.start), max(v.start, v.end-1))
	col := min(max(x-v.x, 0), v.width-1)
	return textPosition{line: line, col: col}
}

// detailViewport maps mouse coordinates onto the visible detail content. It
// deliberately excludes the sidebar, title tabs, footer and modal overlays.
func (m model) detailViewport() (detailViewport, bool) {
	if m.router.open || m.help.open || m.input.kind != "" || m.cleanup.kind != "" ||
		m.quitConfirm || m.agentPicker.target != "" || m.profilePicker.target != "" ||
		len(m.tasks) == 0 || m.selected < 0 || m.selected >= len(m.tasks) {
		return detailViewport{}, false
	}
	paneWidth, paneHeight := m.detailGeometry()
	if paneWidth < 4 || paneHeight < 5 {
		return detailViewport{}, false
	}
	l := m.layout()
	inner := l.panelInner(paneWidth)
	content := m.detailContent(inner)
	available := max(0, l.detailRows(paneHeight))
	if inner < 1 || available < 1 || len(content) == 0 {
		return detailViewport{}, false
	}
	offset := min(max(0, m.historyOffset), max(0, len(content)-available))
	end := len(content) - offset
	start := max(0, end-available)
	x := 0
	if l.sidebar {
		x = l.taskWidth + 1
	}
	return detailViewport{
		x:       x + 2,
		y:       4,
		width:   inner,
		start:   start,
		end:     end,
		content: content,
	}, true
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// The quit confirmation owns the mouse as well as the keyboard: no press,
	// release or wheel may reach a surface it covers.
	if m.quitConfirm {
		return m, nil
	}
	if tea.MouseEvent(msg).IsWheel() {
		if m.help.open {
			if msg.Button == tea.MouseButtonWheelUp {
				m.scrollHelpBody(-3)
			}
			if msg.Button == tea.MouseButtonWheelDown {
				m.scrollHelpBody(3)
			}
			return m, nil
		}
		if m.router.open || m.input.kind != "" || m.cleanup.kind != "" ||
			m.agentPicker.target != "" || m.profilePicker.target != "" {
			return m, nil
		}
		m.selection = textSelection{}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollDetail(3)
		case tea.MouseButtonWheelDown:
			m.scrollDetail(-3)
		}
		return m, nil
	}

	if msg.Action == tea.MouseActionRelease {
		if !m.selection.dragging || m.selection.button != tea.MouseButtonLeft || msg.Button != tea.MouseButtonLeft {
			return m, nil
		}
		if !m.selectionMappingIntact() {
			m.selection = textSelection{}
			return m, nil
		}
		m.selection.focus = m.selection.view.viewport().clamp(msg.X, msg.Y)
		m.selection.dragging = false
		if !m.selection.hasRange() {
			m.selection = textSelection{}
			return m, nil
		}
		text := selectedText(m.selection.lines, m.selection)
		if strings.TrimSpace(text) == "" {
			m.notice.text = "Copy selection: nothing to copy."
			return m, nil
		}
		return m, copySelectionCmd(text, m.clipboard)
	}

	if msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// Help covers the text underneath it; a click must not start a selection
	// in a pane the operator cannot see.
	if m.help.open {
		return m, nil
	}

	switch msg.Action {
	case tea.MouseActionPress:
		viewport, ok := m.detailViewport()
		if !ok {
			m.selection = textSelection{}
			return m, nil
		}
		position, ok := viewport.position(msg.X, msg.Y)
		if !ok {
			m.selection = textSelection{}
			return m, nil
		}
		m.selection = textSelection{
			active:   true,
			dragging: true,
			button:   msg.Button,
			anchor:   position,
			focus:    position,
			lines:    append([]string(nil), viewport.content...),
			view:     viewport.selectionView(),
		}
	case tea.MouseActionMotion:
		if !m.selection.dragging {
			return m, nil
		}
		if !m.selectionMappingIntact() {
			m.selection = textSelection{}
			return m, nil
		}
		m.selection.focus = m.selection.view.viewport().clamp(msg.X, msg.Y)
	}
	return m, nil
}

// selectionMappingIntact reports whether the live detail pane still maps the
// frozen selection cells onto the same content lines.
func (m model) selectionMappingIntact() bool {
	if !m.selection.active {
		return false
	}
	viewport, ok := m.detailViewport()
	return ok && m.selection.view.matches(viewport)
}

// cancelStaleSelection drops a pending selection once output, scrolling or a
// resize changes the mapping between cells and content.
func (m *model) cancelStaleSelection() {
	if m.selection.active && !m.selectionMappingIntact() {
		m.selection = textSelection{}
	}
}
