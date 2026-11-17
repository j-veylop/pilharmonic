package tui

// vimScrolls scroll details by a count of lines derived from the page height.
var vimScrolls = map[string]func(page int) int{
	"ctrl+u": func(page int) int { return max(1, page/2) },
	"ctrl+d": func(page int) int { return -max(1, page/2) },
	"pgup":   func(page int) int { return page },
	"pgdown": func(page int) int { return -page },
}

// vimNavigation applies normal-mode motions only; composers handle text first.
func (m *model) vimNavigation(key string) bool {
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && (key != "0" || m.vimCount > 0) {
		m.vimCount = min(999, m.vimCount*10+int(key[0]-'0'))
		m.vimG = false
		return true
	}
	if key == "g" && !m.vimG {
		m.vimG = true
		return true
	}
	count := max(1, m.vimCount)
	m.vimG, m.vimCount = false, 0
	if scroll, ok := vimScrolls[key]; ok {
		m.scrollDetail(scroll(m.detailPage()) * count)
		return true
	}
	detail := m.detailFocus || m.fullscreen
	switch key {
	case "g":
		m.jumpVim(false, count)
	case "G":
		m.jumpVim(true, count)
	case "h":
		m.detailFocus, m.fullscreen = false, false
		m.clampScroll()
	case "l":
		m.detailFocus = true
		m.clampScroll()
	case "j", "down":
		if detail {
			m.scrollDetail(-count)
		} else {
			m.moveTaskSelection(count)
		}
	case "k", "up":
		if detail {
			m.scrollDetail(count)
		} else {
			m.moveTaskSelection(-count)
		}
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
		rows := m.mainRows()
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

// jumpAttention moves selection to the next task that needs attention in
// grouped sidebar order, wrapping at the end. It reports whether any task needs
// attention; when none do, the selection is left unchanged.
func (m *model) jumpAttention() bool {
	rows := m.mainRows()
	any := false
	for _, row := range rows {
		if needsAttention(row.task) {
			any = true
			break
		}
	}
	if !any {
		return false
	}
	position := -1
	current := rootIndex(m.tasks, m.selected)
	for i, row := range rows {
		if row.index == current {
			position = i
			break
		}
	}
	for step := 1; step <= len(rows); step++ {
		candidate := (position + step) % len(rows)
		if needsAttention(rows[candidate].task) {
			m.selected = rows[candidate].index
			m.historyOffset = 0
			m.resetActivitySelection()
			return true
		}
	}
	return false
}

// cycleSubtask moves the detail selection through the selected main task and
// its descendants, so a subagent can be inspected without cluttering the list.
func (m *model) cycleSubtask(delta int) {
	root := m.rootTask(m.selectedTaskOrZero())
	rows := []taskListRow{{index: rootIndex(m.tasks, m.selected), task: root}}
	rows = append(rows, subtaskRows(m.tasks, root.ID)...)
	position := 0
	for i, row := range rows {
		if row.index == m.selected {
			position = i
			break
		}
	}
	position = max(0, min(len(rows)-1, position+delta))
	m.selected = rows[position].index
	m.historyOffset = 0
	m.resetActivitySelection()
}

// moveTaskSelection follows the main view order. Selection of a subtask moves
// from its main task's position.
func (m *model) moveTaskSelection(delta int) {
	rows := m.mainRows()
	if len(rows) == 0 {
		m.selected = 0
		m.historyOffset = 0
		return
	}
	position := 0
	found := false
	current := rootIndex(m.tasks, m.selected)
	for i, row := range rows {
		if row.index == current {
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
