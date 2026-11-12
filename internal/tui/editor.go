package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// editor is the shared multiline draft used by composers and the router. It
// stores a byte cursor and the first visible wrapped line. The zero value is a
// usable empty editor so composer values remain comparable in tests.
type editor struct {
	text   string
	cursor int
	scroll int
}

// editorLine is one display line after soft wrapping. start and end are byte
// offsets into the original text. newline records a hard line break at end.
type editorLine struct {
	text    string
	start   int
	end     int
	newline bool
}

func (e *editor) clamp() {
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor > len(e.text) {
		e.cursor = len(e.text)
	}
	for e.cursor > 0 && e.cursor < len(e.text) && !utf8.RuneStart(e.text[e.cursor]) {
		e.cursor--
	}
	if e.scroll < 0 {
		e.scroll = 0
	}
}

func (e *editor) insert(value string) {
	e.clamp()
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	e.text = e.text[:e.cursor] + value + e.text[e.cursor:]
	e.cursor += len(value)
}

func (e *editor) backspace() {
	e.clamp()
	if e.cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
	e.text = e.text[:e.cursor-size] + e.text[e.cursor:]
	e.cursor -= size
}

func (e *editor) delete() {
	e.clamp()
	if e.cursor >= len(e.text) {
		return
	}
	_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
	e.text = e.text[:e.cursor] + e.text[e.cursor+size:]
}

// isEditorWord reports whether r belongs to a word for word-wise editing.
// Underscores count as word characters, so identifiers move as one unit.
func isEditorWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// moveWordBackward moves to the start of the word before the cursor, skipping
// the whitespace between words first.
func (e *editor) moveWordBackward() {
	e.clamp()
	for e.cursor > 0 {
		r, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
		if !unicode.IsSpace(r) {
			break
		}
		e.cursor -= size
	}
	for e.cursor > 0 {
		r, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
		if !isEditorWord(r) {
			break
		}
		e.cursor -= size
	}
}

// moveWordForward moves past the next word, skipping leading non-word runes.
func (e *editor) moveWordForward() {
	e.clamp()
	for e.cursor < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[e.cursor:])
		if isEditorWord(r) {
			break
		}
		e.cursor += size
	}
	for e.cursor < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[e.cursor:])
		if !isEditorWord(r) {
			break
		}
		e.cursor += size
	}
}

// deleteWordBackward removes the word before the cursor and the whitespace
// that separates it from earlier text. Like a textarea, it stays on its own
// line so a deletion never swallows a line break.
func (e *editor) deleteWordBackward() {
	e.clamp()
	if e.cursor == 0 {
		return
	}
	if r, _ := utf8.DecodeLastRuneInString(e.text[:e.cursor]); r == '\n' {
		return
	}
	start := e.cursor
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(e.text[:start])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(e.text[:start])
		if r == '\n' || unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	e.text = e.text[:start] + e.text[e.cursor:]
	e.cursor = start
}

// deleteWordForward removes the word after the cursor and the gap that follows
// it, leaving no double space behind. It stays on its own line.
func (e *editor) deleteWordForward() {
	e.clamp()
	end := e.cursor
	for end < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[end:])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	for end < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[end:])
		if r == '\n' || unicode.IsSpace(r) {
			break
		}
		end += size
	}
	if end == e.cursor {
		return
	}
	for end < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[end:])
		if r == '\n' || !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	e.text = e.text[:e.cursor] + e.text[end:]
}

// deleteLineStart removes the draft between the line start and the cursor. At
// a line start it joins the previous line, which is what repeated Ctrl-u does
// in a readline-style editor.
func (e *editor) deleteLineStart() {
	e.clamp()
	start := 0
	if i := strings.LastIndexByte(e.text[:e.cursor], '\n'); i >= 0 {
		start = i + 1
	} else if e.cursor == 0 {
		return
	}
	if start == e.cursor && start > 0 {
		start--
	}
	e.text = e.text[:start] + e.text[e.cursor:]
	e.cursor = start
}

// deleteLineEnd removes the draft between the cursor and the line end. At a
// line end it removes the newline, joining the following line.
func (e *editor) deleteLineEnd() {
	e.clamp()
	if e.cursor >= len(e.text) {
		return
	}
	end := len(e.text)
	if i := strings.IndexByte(e.text[e.cursor:], '\n'); i >= 0 {
		end = e.cursor + i
	}
	if end == e.cursor {
		end++
	}
	e.text = e.text[:e.cursor] + e.text[end:]
}

func (e *editor) moveHorizontal(delta int) {
	e.clamp()
	if delta < 0 {
		for ; delta < 0 && e.cursor > 0; delta++ {
			_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
			e.cursor -= size
		}
		return
	}
	for ; delta > 0 && e.cursor < len(e.text); delta-- {
		_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
		e.cursor += size
	}
}

func (e *editor) moveLineStart() {
	e.clamp()
	if i := strings.LastIndexByte(e.text[:e.cursor], '\n'); i >= 0 {
		e.cursor = i + 1
	} else {
		e.cursor = 0
	}
}

func (e *editor) moveLineEnd() {
	e.clamp()
	if i := strings.IndexByte(e.text[e.cursor:], '\n'); i >= 0 {
		e.cursor += i
	} else {
		e.cursor = len(e.text)
	}
}

func (e *editor) moveStart() {
	e.cursor = 0
}

func (e *editor) moveEnd() {
	e.cursor = len(e.text)
}

// moveVertical keeps the display column while moving between visual lines so
// wrapped paragraphs and hard line breaks both behave like a textarea.
func (e *editor) moveVertical(delta, width int) {
	e.clamp()
	lines := wrapEditor(e.text, width)
	current := editorCursorLine(lines, e.cursor)
	column := editorCursorColumn(e.text, lines[current], e.cursor)
	target := min(max(0, current+delta), len(lines)-1)
	if target == current {
		return
	}
	offset := editorOffsetAtColumn(e.text, lines[target], column)
	e.cursor = editorOffsetWithinLine(e.text, lines, target, offset)
}

func (e *editor) ensureVisible(width, height int) {
	e.clamp()
	lines := wrapEditor(e.text, width)
	height = max(1, height)
	current := editorCursorLine(lines, e.cursor)
	if current < e.scroll {
		e.scroll = current
	}
	if current >= e.scroll+height {
		e.scroll = current - height + 1
	}
	e.scroll = min(max(0, e.scroll), max(0, len(lines)-height))
}

// handleKey applies editing and cursor keys. Enter and Esc stay with the
// surrounding screen because they submit or cancel the draft.
func (e *editor) handleKey(msg tea.KeyMsg, width, height int) bool {
	switch msg.String() {
	case "ctrl+j", "alt+enter", "shift+enter":
		e.insert("\n")
	case "backspace", "ctrl+h":
		e.backspace()
	case "delete", "ctrl+d":
		e.delete()
	case "ctrl+w", "alt+backspace":
		e.deleteWordBackward()
	case "alt+d", "alt+delete":
		e.deleteWordForward()
	case "ctrl+u":
		e.deleteLineStart()
	case "ctrl+k":
		e.deleteLineEnd()
	case "left", "ctrl+b":
		e.moveHorizontal(-1)
	case "right", "ctrl+f":
		e.moveHorizontal(1)
	case "ctrl+left", "alt+left", "alt+b":
		e.moveWordBackward()
	case "ctrl+right", "alt+right", "alt+f":
		e.moveWordForward()
	case "up", "ctrl+p":
		e.moveVertical(-1, width)
	case "down", "ctrl+n":
		e.moveVertical(1, width)
	case "pgup":
		e.moveVertical(-max(1, height), width)
	case "pgdown":
		e.moveVertical(max(1, height), width)
	case "home", "ctrl+a":
		e.moveLineStart()
	case "end", "ctrl+e":
		e.moveLineEnd()
	case "ctrl+home":
		e.moveStart()
	case "ctrl+end":
		e.moveEnd()
	default:
		switch msg.Type {
		case tea.KeyRunes:
			e.insert(string(msg.Runes))
		case tea.KeySpace:
			e.insert(" ")
		default:
			return false
		}
	}
	e.ensureVisible(width, height)
	return true
}

// viewLines returns at most height wrapped lines, scrolled so the cursor is
// visible. The cursor is drawn at its byte position within the visible text.
func (e editor) viewLines(width, height int) []string {
	return e.viewLinesCursor(width, height, true)
}

// viewLinesCursor is viewLines with an optional cursor, so a draft that does
// not hold focus keeps its scroll position without showing a caret.
func (e editor) viewLinesCursor(width, height int, drawCursor bool) []string {
	width = max(1, width)
	height = max(1, height)
	e.clamp()
	lines := wrapEditor(e.text, width)
	current := editorCursorLine(lines, e.cursor)
	start := min(max(0, e.scroll), max(0, len(lines)-height))
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
			line = insertEditorCursor(line, editorCursorColumn(e.text, lines[i], e.cursor))
		}
		result = append(result, line)
	}
	return result
}

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

func insertEditorCursor(text string, column int) string {
	const cursor = "▏"
	width := lipgloss.Width(text)
	if column >= width {
		return text + cursor
	}
	column = max(0, column)
	return ansi.Cut(text, 0, column) + cursor + ansi.Cut(text, column, width)
}
