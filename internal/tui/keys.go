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
			m.moveTaskSelection(delta)
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
		rows := taskListRows(m.tasks)
		position := 0
		if len(rows) > 0 {
			if bottom {
				position = len(rows) - 1
			}
			if count > 1 {
				position = min(len(rows)-1, count-1)
			}
			m.selected = rows[position].index
		} else {
			m.selected = 0
		}
		m.historyOffset = 0
	}
}

// moveTaskSelection follows the grouped sidebar order rather than the service
// snapshot order, so a child stays next to its parent while navigating.
func (m *model) moveTaskSelection(delta int) {
	rows := taskListRows(m.tasks)
	if len(rows) == 0 {
		m.selected = 0
		m.historyOffset = 0
		return
	}
	position := 0
	found := false
	for i, row := range rows {
		if row.index == m.selected {
			position = i
			found = true
			break
		}
	}
	if !found {
		if m.selected > 0 {
			position = len(rows) - 1
		}
		m.selected = rows[position].index
		m.historyOffset = 0
		return
	}
	position = max(0, min(len(rows)-1, position+delta))
	m.selected = rows[position].index
	m.historyOffset = 0
}
