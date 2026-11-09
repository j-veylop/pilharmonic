package tui

import "strconv"

// vimNavigation applies normal-mode motions only; composers handle text first.
func (m *model) vimNavigation(key string) bool {
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && (key != "0" || m.vimCount > 0) {
		digit, _ := strconv.Atoi(key)
		m.vimCount = min(999, m.vimCount*10+digit)
		m.vimG = false
		return true
	}
	count := max(1, m.vimCount)
	if key == "g" {
		if !m.vimG {
			m.vimG = true
			return true
		}
		m.vimG = false
		m.vimCount = 0
		m.jumpVim(false, count)
		return true
	}
	m.vimG = false
	m.vimCount = 0
	switch key {
	case "h", "ctrl+h":
		m.detailFocus = false
		m.fullscreen = false
		m.clampScroll()
	case "l", "ctrl+l":
		m.detailFocus = true
		m.clampScroll()
	case "j", "down", "k", "up":
		delta := count
		if key == "k" || key == "up" {
			delta = -count
		}
		if m.detailFocus || m.fullscreen {
			m.scrollDetail(-delta)
		} else {
			m.selected = max(0, min(max(0, len(m.tasks)-1), m.selected+delta))
			m.historyOffset = 0
		}
	case "G":
		m.jumpVim(true, count)
	case "ctrl+u":
		m.scrollDetail(max(1, m.detailPage()/2) * count)
	case "ctrl+d":
		m.scrollDetail(-max(1, m.detailPage()/2) * count)
	case "ctrl+b", "pgup":
		m.scrollDetail(m.detailPage() * count)
	case "ctrl+f", "pgdown":
		m.scrollDetail(-m.detailPage() * count)
	case "ctrl+y":
		m.scrollDetail(count)
	case "ctrl+e":
		m.scrollDetail(-count)
	default:
		return false
	}
	return true
}

func (m *model) jumpVim(bottom bool, count int) {
	if m.detailFocus || m.fullscreen {
		if bottom {
			m.historyOffset = 0
		} else {
			m.historyOffset = m.maxScroll()
		}
	} else {
		m.selected = 0
		if bottom {
			m.selected = max(0, len(m.tasks)-1)
		}
		if count > 1 {
			m.selected = min(max(0, len(m.tasks)-1), count-1)
		}
		m.historyOffset = 0
	}
}
