package tui

import tea "github.com/charmbracelet/bubbletea"

// Help is the one surface an operator can reach from every screen. F1 opens it
// wherever they are, including inside a text field; ? opens it on every surface
// where the character is not part of the draft being typed.
//
// The overlay never clears the state underneath it, so closing help returns to
// the board, request form, composer, picker or menu exactly as it was left.

// textEntryFocused reports whether the active surface is a text field that
// owns printable characters. Help must not steal ? from a draft, from an API
// key prompt or from a custom model ID.
func (m model) textEntryFocused() bool {
	if m.loginProvider != "" {
		return true
	}
	if m.input.kind != "" {
		return true
	}
	if m.router && m.routerFocus == routerFocusComposer {
		return true
	}
	if m.modelMenu && m.modelStep == modelStepCustom {
		return true
	}
	return false
}

// helpShortcut reports whether msg opens the help overlay on the current
// surface. Pasted text is never a shortcut, even when it is exactly "?".
func (m model) helpShortcut(msg tea.KeyMsg) bool {
	if msg.Paste {
		return false
	}
	switch msg.String() {
	case "f1":
		return true
	case "?":
		return !m.textEntryFocused()
	}
	return false
}

// openHelp shows the overlay above whatever is on screen. Navigation starts
// over so the shortcut always lands on the first topic.
func (m model) openHelp() model {
	m.help = true
	m.helpPage, m.helpScroll = 0, 0
	m.quitConfirm = false
	m.vimG, m.vimCount = false, 0
	return m
}
