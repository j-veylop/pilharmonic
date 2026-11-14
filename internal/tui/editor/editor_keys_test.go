package editor

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// draft splits "text|more" into its text and the cursor at the bar.
func draft(s string) Editor {
	at := strings.Index(s, "|")
	return Editor{Text: s[:at] + s[at+1:], Cursor: at}
}

func TestEditorKeys(t *testing.T) {
	ctrl := func(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }
	alt := func(k tea.KeyType, r ...rune) tea.KeyMsg { return tea.KeyMsg{Type: k, Runes: r, Alt: true} }
	cases := []struct {
		name     string
		keys     []tea.KeyMsg
		from, to string
	}{
		{"ctrl+w", []tea.KeyMsg{ctrl(tea.KeyCtrlW)}, "hello big world|", "hello big |"},
		{"ctrl+w trailing space", []tea.KeyMsg{ctrl(tea.KeyCtrlW)}, "hello big   |", "hello |"},
		{"ctrl+w tabs", []tea.KeyMsg{ctrl(tea.KeyCtrlW)}, "tab\tsep\tinput|", "tab\tsep\t|"},
		{"ctrl+w unicode", []tea.KeyMsg{ctrl(tea.KeyCtrlW)}, "café naïve|", "café |"},
		{"ctrl+w stops at newline", []tea.KeyMsg{ctrl(tea.KeyCtrlW), ctrl(tea.KeyCtrlW)}, "first line\nsecond|", "first line\n|"},
		{"alt+backspace", []tea.KeyMsg{alt(tea.KeyBackspace)}, "punctuation, stays|", "punctuation, |"},
		{"alt+d", []tea.KeyMsg{alt(tea.KeyRunes, 'd')}, "keep |drop keep", "keep |keep"},
		{"alt+delete", []tea.KeyMsg{alt(tea.KeyDelete)}, "keep |drop keep", "keep |keep"},
		{"ctrl+k tail then join", []tea.KeyMsg{ctrl(tea.KeyCtrlK), ctrl(tea.KeyCtrlK)}, "first\nsec|ond\nthird", "first\nsec|third"},
		{"ctrl+u head then join", []tea.KeyMsg{ctrl(tea.KeyCtrlU), ctrl(tea.KeyCtrlU)}, "first\nsecond|", "first|"},
		{"ctrl+d wide rune", []tea.KeyMsg{ctrl(tea.KeyCtrlD)}, "ab|世cd", "ab|cd"},
		{"backspace runes", []tea.KeyMsg{ctrl(tea.KeyBackspace), ctrl(tea.KeyBackspace), ctrl(tea.KeyBackspace), ctrl(tea.KeyBackspace)}, "aé世|", "|"},
		{"delete runes", []tea.KeyMsg{ctrl(tea.KeyDelete), ctrl(tea.KeyDelete)}, "|aé世", "|世"},
		{"ctrl+left", []tea.KeyMsg{ctrl(tea.KeyCtrlLeft), ctrl(tea.KeyCtrlLeft)}, "alpha beta gamma|", "alpha |beta gamma"},
		{"alt+left", []tea.KeyMsg{alt(tea.KeyLeft)}, "alpha beta gamma|", "alpha beta |gamma"},
		{"alt+b", []tea.KeyMsg{alt(tea.KeyRunes, 'b')}, "alpha beta gamma|", "alpha beta |gamma"},
		{"ctrl+right", []tea.KeyMsg{ctrl(tea.KeyCtrlRight), ctrl(tea.KeyCtrlRight)}, "|alpha beta gamma", "alpha beta| gamma"},
		{"alt+right", []tea.KeyMsg{alt(tea.KeyRight)}, "|alpha beta", "alpha| beta"},
		{"alt+f", []tea.KeyMsg{alt(tea.KeyRunes, 'f')}, "|alpha beta", "alpha| beta"},
		{"ctrl+a / home", []tea.KeyMsg{ctrl(tea.KeyCtrlA)}, "one\ntw|o", "one\n|two"},
		{"ctrl+e / end", []tea.KeyMsg{ctrl(tea.KeyCtrlE)}, "|one\ntwo", "one|\ntwo"},
		{"ctrl+home", []tea.KeyMsg{ctrl(tea.KeyCtrlHome)}, "one\ntwo|", "|one\ntwo"},
		{"ctrl+p keeps column", []tea.KeyMsg{ctrl(tea.KeyCtrlP)}, "first\nsecon|d", "first|\nsecond"},
		{"ctrl+n keeps column", []tea.KeyMsg{ctrl(tea.KeyCtrlN)}, "fir|st\nsecond", "first\nsec|ond"},
		{"ctrl+j newline", []tea.KeyMsg{ctrl(tea.KeyCtrlJ)}, "a|b", "a\n|b"},
		{"typing", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeySpace}}, "a|b", "ax |b"},
	}
	for _, tc := range cases {
		e := draft(tc.from)
		for _, msg := range tc.keys {
			e.HandleKey(msg, 30, 4)
		}
		if want := draft(tc.to); e.Text != want.Text || e.Cursor != want.Cursor {
			t.Fatalf("%s: %q at %d, want %q at %d", tc.name, e.Text, e.Cursor, want.Text, want.Cursor)
		}
	}
}
