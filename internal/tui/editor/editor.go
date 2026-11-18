package editor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Editor is the shared multiline draft used by composers and the router. It
// stores a byte cursor and the first visible wrapped line. The zero value is a
// usable empty editor so composer values remain comparable in tests.
type Editor struct {
	Text   string
	Cursor int
	Scroll int
}

// editorLine is one display line after soft wrapping. start and end are byte
// offsets into the original text. newline records a hard line break at end.
type editorLine struct {
	text    string
	start   int
	end     int
	newline bool
}

func (e *Editor) clamp() {
	if e.Cursor < 0 {
		e.Cursor = 0
	}
	if e.Cursor > len(e.Text) {
		e.Cursor = len(e.Text)
	}
	for e.Cursor > 0 && e.Cursor < len(e.Text) && !utf8.RuneStart(e.Text[e.Cursor]) {
		e.Cursor--
	}
	if e.Scroll < 0 {
		e.Scroll = 0
	}
}

func (e *Editor) insert(value string) {
	e.clamp()
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	e.Text = e.Text[:e.Cursor] + value + e.Text[e.Cursor:]
	e.Cursor += len(value)
}

func (e *Editor) backspace() {
	e.clamp()
	if e.Cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(e.Text[:e.Cursor])
	e.Text = e.Text[:e.Cursor-size] + e.Text[e.Cursor:]
	e.Cursor -= size
}

func (e *Editor) delete() {
	e.clamp()
	if e.Cursor >= len(e.Text) {
		return
	}
	_, size := utf8.DecodeRuneInString(e.Text[e.Cursor:])
	e.Text = e.Text[:e.Cursor] + e.Text[e.Cursor+size:]
}

// isEditorWord reports whether r belongs to a word for word-wise editing.
// Underscores count as word characters, so identifiers move as one unit.
func isEditorWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// moveWordBackward moves to the start of the word before the cursor, skipping
// the whitespace between words first.
func (e *Editor) moveWordBackward() {
	e.clamp()
	for e.Cursor > 0 {
		r, size := utf8.DecodeLastRuneInString(e.Text[:e.Cursor])
		if !unicode.IsSpace(r) {
			break
		}
		e.Cursor -= size
	}
	for e.Cursor > 0 {
		r, size := utf8.DecodeLastRuneInString(e.Text[:e.Cursor])
		if !isEditorWord(r) {
			break
		}
		e.Cursor -= size
	}
}

// moveWordForward moves past the next word, skipping leading non-word runes.
func (e *Editor) moveWordForward() {
	e.clamp()
	for e.Cursor < len(e.Text) {
		r, size := utf8.DecodeRuneInString(e.Text[e.Cursor:])
		if isEditorWord(r) {
			break
		}
		e.Cursor += size
	}
	for e.Cursor < len(e.Text) {
		r, size := utf8.DecodeRuneInString(e.Text[e.Cursor:])
		if !isEditorWord(r) {
			break
		}
		e.Cursor += size
	}
}

// deleteWordBackward removes the word before the cursor and the whitespace
// that separates it from earlier text. Like a textarea, it stays on its own
// line so a deletion never swallows a line break.
func (e *Editor) deleteWordBackward() {
	e.clamp()
	if e.Cursor == 0 {
		return
	}
	if r, _ := utf8.DecodeLastRuneInString(e.Text[:e.Cursor]); r == '\n' {
		return
	}
	start := e.Cursor
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(e.Text[:start])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(e.Text[:start])
		if r == '\n' || unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	e.Text = e.Text[:start] + e.Text[e.Cursor:]
	e.Cursor = start
}

// deleteWordForward removes the word after the cursor and the gap that follows
// it, leaving no double space behind. It stays on its own line.
func (e *Editor) deleteWordForward() {
	e.clamp()
	end := e.Cursor
	for end < len(e.Text) {
		r, size := utf8.DecodeRuneInString(e.Text[end:])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	for end < len(e.Text) {
		r, size := utf8.DecodeRuneInString(e.Text[end:])
		if r == '\n' || unicode.IsSpace(r) {
			break
		}
		end += size
	}
	if end == e.Cursor {
		return
	}
	for end < len(e.Text) {
		r, size := utf8.DecodeRuneInString(e.Text[end:])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	e.Text = e.Text[:e.Cursor] + e.Text[end:]
}

// deleteLineStart removes the draft between the line start and the cursor. At
// a line start it joins the previous line, which is what repeated Ctrl-u does
// in a readline-style editor.
func (e *Editor) deleteLineStart() {
	e.clamp()
	start := 0
	if i := strings.LastIndexByte(e.Text[:e.Cursor], '\n'); i >= 0 {
		start = i + 1
	} else if e.Cursor == 0 {
		return
	}
	if start == e.Cursor && start > 0 {
		start--
	}
	e.Text = e.Text[:start] + e.Text[e.Cursor:]
	e.Cursor = start
}

// deleteLineEnd removes the draft between the cursor and the line end. At a
// line end it removes the newline, joining the following line.
func (e *Editor) deleteLineEnd() {
	e.clamp()
	if e.Cursor >= len(e.Text) {
		return
	}
	end := len(e.Text)
	if i := strings.IndexByte(e.Text[e.Cursor:], '\n'); i >= 0 {
		end = e.Cursor + i
	}
	if end == e.Cursor {
		end++
	}
	e.Text = e.Text[:e.Cursor] + e.Text[end:]
}

func (e *Editor) moveHorizontal(delta int) {
	e.clamp()
	if delta < 0 {
		for ; delta < 0 && e.Cursor > 0; delta++ {
			_, size := utf8.DecodeLastRuneInString(e.Text[:e.Cursor])
			e.Cursor -= size
		}
		return
	}
	for ; delta > 0 && e.Cursor < len(e.Text); delta-- {
		_, size := utf8.DecodeRuneInString(e.Text[e.Cursor:])
		e.Cursor += size
	}
}

func (e *Editor) moveLineStart() {
	e.clamp()
	if i := strings.LastIndexByte(e.Text[:e.Cursor], '\n'); i >= 0 {
		e.Cursor = i + 1
	} else {
		e.Cursor = 0
	}
}

func (e *Editor) moveLineEnd() {
	e.clamp()
	if i := strings.IndexByte(e.Text[e.Cursor:], '\n'); i >= 0 {
		e.Cursor += i
	} else {
		e.Cursor = len(e.Text)
	}
}

// moveVertical keeps the display column while moving between visual lines so
// wrapped paragraphs and hard line breaks both behave like a textarea.
func (e *Editor) moveVertical(delta, width int) {
	e.clamp()
	lines := wrapEditor(e.Text, width)
	current := editorCursorLine(lines, e.Cursor)
	column := editorCursorColumn(e.Text, lines[current], e.Cursor)
	target := min(max(0, current+delta), len(lines)-1)
	if target == current {
		return
	}
	offset := editorOffsetAtColumn(e.Text, lines[target], column)
	e.Cursor = editorOffsetWithinLine(e.Text, lines, target, offset)
}

// OnFirstLine and OnLastLine report whether the cursor sits on the first or
// last wrapped line, where a vertical move has nowhere further to go.
func (e Editor) OnFirstLine(width int) bool {
	e.clamp()
	return editorCursorLine(wrapEditor(e.Text, width), e.Cursor) == 0
}

func (e Editor) OnLastLine(width int) bool {
	e.clamp()
	lines := wrapEditor(e.Text, width)
	return editorCursorLine(lines, e.Cursor) == len(lines)-1
}

func (e *Editor) ensureVisible(width, height int) {
	e.clamp()
	lines := wrapEditor(e.Text, width)
	height = max(1, height)
	current := editorCursorLine(lines, e.Cursor)
	if current < e.Scroll {
		e.Scroll = current
	}
	if current >= e.Scroll+height {
		e.Scroll = current - height + 1
	}
	e.Scroll = min(max(0, e.Scroll), max(0, len(lines)-height))
}

// editorKeys are the editing and cursor keys. Enter and Esc stay with the
// surrounding screen because they submit or cancel the draft.
var editorKeys = func() map[string]func(e *Editor, width, height int) {
	keys := map[string]func(*Editor, int, int){}
	bind := func(action func(*Editor, int, int), names ...string) {
		for _, name := range names {
			keys[name] = action
		}
	}
	plain := func(action func(*Editor)) func(*Editor, int, int) {
		return func(e *Editor, _, _ int) { action(e) }
	}
	vertical := func(lines int) func(*Editor, int, int) {
		return func(e *Editor, width, height int) { e.moveVertical(lines*max(1, height), width) }
	}
	bind(func(e *Editor, _, _ int) { e.insert("\n") }, "ctrl+j", "alt+enter", "shift+enter")
	bind(plain((*Editor).backspace), "backspace", "ctrl+h")
	bind(plain((*Editor).delete), "delete", "ctrl+d")
	bind(plain((*Editor).deleteWordBackward), "ctrl+w", "alt+backspace")
	bind(plain((*Editor).deleteWordForward), "alt+d", "alt+delete")
	bind(plain((*Editor).deleteLineStart), "ctrl+u")
	bind(plain((*Editor).deleteLineEnd), "ctrl+k")
	bind(func(e *Editor, _, _ int) { e.moveHorizontal(-1) }, "left", "ctrl+b")
	bind(func(e *Editor, _, _ int) { e.moveHorizontal(1) }, "right", "ctrl+f")
	bind(plain((*Editor).moveWordBackward), "ctrl+left", "alt+left", "alt+b")
	bind(plain((*Editor).moveWordForward), "ctrl+right", "alt+right", "alt+f")
	bind(func(e *Editor, width, _ int) { e.moveVertical(-1, width) }, "up", "ctrl+p")
	bind(func(e *Editor, width, _ int) { e.moveVertical(1, width) }, "down", "ctrl+n")
	bind(vertical(-1), "pgup")
	bind(vertical(1), "pgdown")
	bind(plain((*Editor).moveLineStart), "home", "ctrl+a")
	bind(plain((*Editor).moveLineEnd), "end", "ctrl+e")
	bind(func(e *Editor, _, _ int) { e.Cursor = 0 }, "ctrl+home")
	bind(func(e *Editor, _, _ int) { e.Cursor = len(e.Text) }, "ctrl+end")
	return keys
}()

// HandleKey applies an editing key or inserts typed text.
func (e *Editor) HandleKey(msg tea.KeyMsg, width, height int) {
	if action, ok := editorKeys[msg.String()]; ok {
		action(e, width, height)
	} else if msg.Type == tea.KeyRunes {
		e.insert(string(msg.Runes))
	} else if msg.Type == tea.KeySpace {
		e.insert(" ")
	} else {
		return
	}
	e.ensureVisible(width, height)
}

// View returns at most height wrapped lines, scrolled so the cursor
// is visible, and optionally draws the cursor at its byte position within the
// visible text. A draft that does not hold focus keeps its scroll position
// without showing a caret.
func (e Editor) View(width, height int, drawCursor bool) []string {
	width = max(1, width)
	height = max(1, height)
	e.clamp()
	lines := wrapEditor(e.Text, width)
	current := editorCursorLine(lines, e.Cursor)
	start := min(max(0, e.Scroll), max(0, len(lines)-height))
	if current < start {
		start = current
	}
	if current >= start+height {
		start = current - height + 1
	}
	start = min(max(0, start), max(0, len(lines)-height))
	end := min(len(lines), start+height)
	result := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		line := lines[i].text
		if i == current && drawCursor {
			line = insertEditorCursor(line, editorCursorColumn(e.Text, lines[i], e.Cursor))
		}
		result = append(result, line)
	}
	return result
}

// LineCount is how many wrapped lines the draft fills at width.
func (e Editor) LineCount(width int) int { return len(wrapEditor(e.Text, width)) }

func wrapEditor(text string, width int) []editorLine {
	width = max(1, width)
	lines := make([]editorLine, 0, 8)
	var line strings.Builder
	lineStart, column := 0, 0
	flush := func(end int, newline bool) {
		lines = append(lines, editorLine{text: line.String(), start: lineStart, end: end, newline: newline})
		line.Reset()
		column = 0
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		next := i + size
		switch {
		case r == '\r':
			if next < len(text) && text[next] == '\n' {
				next++
			}
			flush(i, true)
			lineStart = next
			i = next
		case r == '\n':
			flush(i, true)
			lineStart = next
			i = next
		case r == '\t':
			spaces := 4 - column%4
			if column > 0 && column+spaces > width {
				flush(i, false)
				lineStart = i
			}
			if column == 0 && spaces > width {
				spaces = width
			}
			line.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			i = next
		case unicode.IsControl(r):
			i = next
		default:
			cellWidth := lipgloss.Width(string(r))
			if column > 0 && column+cellWidth > width {
				flush(i, false)
				lineStart = i
			}
			line.WriteRune(r)
			column += cellWidth
			i = next
		}
	}
	flush(len(text), false)
	return lines
}

func editorCursorLine(lines []editorLine, cursor int) int {
	for i, line := range lines {
		if cursor < line.start || cursor > line.end {
			continue
		}
		if cursor == line.end && !line.newline && i+1 < len(lines) && lines[i+1].start == line.end {
			continue
		}
		return i
	}
	if cursor <= 0 {
		return 0
	}
	return len(lines) - 1
}

func editorCursorColumn(text string, line editorLine, cursor int) int {
	column := 0
	end := min(max(cursor, line.start), line.end)
	for i := line.start; i < end; {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == '\t':
			column += 4 - column%4
		case unicode.IsControl(r):
		default:
			column += lipgloss.Width(string(r))
		}
		i += size
	}
	return column
}

func editorOffsetAtColumn(text string, line editorLine, target int) int {
	column := 0
	for i := line.start; i < line.end; {
		r, size := utf8.DecodeRuneInString(text[i:])
		cellWidth := 0
		switch {
		case r == '\t':
			cellWidth = 4 - column%4
		case unicode.IsControl(r):
		default:
			cellWidth = lipgloss.Width(string(r))
		}
		if cellWidth > 0 && column+cellWidth > target {
			return i
		}
		column += cellWidth
		i += size
	}
	return line.end
}

// editorOffsetWithinLine keeps a vertical move on its target line. The end of
// a full wrapped line is also the start of the next one, so editorCursorLine
// maps that offset forward. Moving to the previous rune keeps the cursor on
// the line the move selected and lets repeated Up/Down keys progress.
func editorOffsetWithinLine(text string, lines []editorLine, index, offset int) int {
	if index < 0 || index >= len(lines) {
		return offset
	}
	line := lines[index]
	if offset != line.end || line.newline || line.end <= line.start {
		return offset
	}
	if index+1 >= len(lines) || lines[index+1].start != line.end {
		return offset
	}
	_, size := utf8.DecodeLastRuneInString(text[line.start:line.end])
	return line.end - size
}

// CursorStyle paints the block cursor. The terminal package overrides it with
// the theme's secondary colour; a reverse-video block keeps the plain editor
// package legible on its own.
var CursorStyle = lipgloss.NewStyle().Reverse(true)

func insertEditorCursor(text string, column int) string {
	width := lipgloss.Width(text)
	column = max(0, min(column, width))
	if column >= width {
		return text + CursorStyle.Render(" ")
	}
	rest := ansi.Cut(text, column, width)
	_, size := utf8.DecodeRuneInString(rest)
	if size == 0 {
		return text + CursorStyle.Render(" ")
	}
	// Style the rune under the cursor instead of replacing it, so the letter
	// stays readable while the block paints its cell.
	return ansi.Cut(text, 0, column) + CursorStyle.Render(rest[:size]) + ansi.Cut(text, column+lipgloss.Width(rest[:size]), width)
}
