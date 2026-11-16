package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	maxAttachments     = 5
	maxAttachmentBytes = 64 << 10
)

// attach adds a local file to the next request. Only the path is kept; content
// is read once, at submission, so the request reflects the file it was typed
// against.
func (m *model) attach(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("usage: /attach <path>")
	}
	if len(m.attachments) >= maxAttachments {
		return fmt.Errorf("at most %d files can be attached", maxAttachments)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("attach %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxAttachmentBytes {
		return fmt.Errorf("attach %s: regular file of at most %d KiB required", path, maxAttachmentBytes>>10)
	}
	clean := filepath.Clean(path)
	for _, existing := range m.attachments {
		if existing == clean {
			return fmt.Errorf("%s is already attached", clean)
		}
	}
	m.attachments = append(m.attachments, clean)
	return nil
}

// detach removes one attachment, or all of them when path is "all".
func (m *model) detach(path string) {
	path = strings.TrimSpace(path)
	if path == "all" {
		m.attachments = nil
		return
	}
	clean := filepath.Clean(path)
	kept := m.attachments[:0]
	for _, existing := range m.attachments {
		if existing != clean {
			kept = append(kept, existing)
		}
	}
	m.attachments = kept
}

// composeRequest appends attached file content to the original request. The
// mandate is what the operator typed plus exactly these files; nothing is
// executed and each file is bounded.
func composeRequest(text string, paths []string) (string, error) {
	if len(paths) == 0 {
		return text, nil
	}
	var b strings.Builder
	b.WriteString(text)
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("attach %s: %w", path, err)
		}
		if len(content) > maxAttachmentBytes {
			return "", fmt.Errorf("attach %s: file grew past %d KiB", path, maxAttachmentBytes>>10)
		}
		b.WriteString("\n\nAttached file: " + path + "\n```\n")
		b.Write(content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("```")
	}
	return b.String(), nil
}

// attachmentChips summarizes the attached files above the request box.
func (m model) attachmentChips(width int) []string {
	if len(m.attachments) == 0 {
		return nil
	}
	names := make([]string, len(m.attachments))
	for i, path := range m.attachments {
		names[i] = filepath.Base(path)
	}
	row := lipgloss.NewStyle().Foreground(cyan).Bold(true).Render("attach") +
		mutedStyle.Render("  "+strings.Join(names, "  ·  "))
	return []string{fit(row, width, 1)}
}
