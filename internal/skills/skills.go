// Package skills loads operator-provided instruction packs from local
// directories. A skill is a directory containing SKILL.md with YAML
// frontmatter (name and description) followed by its instructions. The
// orchestrator lists the metadata in the system prompt and exposes the
// instructions through the skill tool, so a task pays for a skill only when it
// loads one. Skills add no authority beyond the task mandate.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// maxFileBytes bounds one SKILL.md so an accidentally huge file cannot
	// exhaust memory or the model context.
	maxFileBytes = 128 << 10
	// maxBodyBytes bounds the instructions returned for one skill.
	maxBodyBytes = 64 << 10
	// maxDescription bounds the one-line description shown in the prompt.
	maxDescription = 1024
)

// Skill is one loaded instruction pack.
type Skill struct {
	Name        string
	Description string
	Body        string
	// Path is the SKILL.md this skill was loaded from.
	Path string
}

// Catalog is an immutable set of loaded skills. The zero value is empty.
type Catalog struct {
	skills []Skill
}

// Len reports how many skills the catalog holds.
func (c Catalog) Len() int { return len(c.skills) }

// List returns the loaded skills ordered by name.
func (c Catalog) List() []Skill { return append([]Skill(nil), c.skills...) }

// Lookup returns the named skill. Names are matched exactly.
func (c Catalog) Lookup(name string) (Skill, bool) {
	for _, skill := range c.skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

// Without returns a catalog with the named skills removed. The receiver is
// unchanged, so a disabled view can be rebuilt without reloading files.
func (c Catalog) Without(names map[string]bool) Catalog {
	if len(names) == 0 {
		return c
	}
	active := make([]Skill, 0, len(c.skills))
	for _, skill := range c.skills {
		if !names[skill.Name] {
			active = append(active, skill)
		}
	}
	return Catalog{skills: active}
}

// Prompt renders the catalog index appended to the system prompt. An empty
// catalog renders nothing.
func (c Catalog) Prompt() string {
	if len(c.skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Orchestrator installed skills (operator text that adds no authority beyond the mandate). Load one with the skill tool before following it:\n")
	for _, skill := range c.skills {
		b.WriteString("- ")
		b.WriteString(skill.Name)
		if skill.Description != "" {
			b.WriteString(": ")
			b.WriteString(skill.Description)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Load reads every skill directory under dir. A missing directory reports
// os.ErrNotExist so the caller can treat an optional default as empty. Entries
// without SKILL.md are skipped; malformed skills fail the load.
func Load(dir string) (Catalog, error) {
	if dir == "" {
		return Catalog{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Catalog{}, err
	}
	loaded := make([]Skill, 0, len(entries))
	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), "SKILL.md")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Catalog{}, err
		}
		if !info.Mode().IsRegular() {
			return Catalog{}, fmt.Errorf("%s: SKILL.md is not a regular file", path)
		}
		if info.Size() > maxFileBytes {
			return Catalog{}, fmt.Errorf("%s: SKILL.md exceeds %d bytes", path, maxFileBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Catalog{}, err
		}
		skill, err := Parse(data)
		if err != nil {
			return Catalog{}, fmt.Errorf("%s: %w", path, err)
		}
		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		if err := validate(skill); err != nil {
			return Catalog{}, fmt.Errorf("%s: %w", path, err)
		}
		skill.Path = path
		if previous, exists := seen[skill.Name]; exists {
			return Catalog{}, fmt.Errorf("duplicate skill %q in %s and %s", skill.Name, previous, path)
		}
		seen[skill.Name] = path
		loaded = append(loaded, skill)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })
	return Catalog{skills: loaded}, nil
}

// Parse decodes one SKILL.md: YAML frontmatter with name and description,
// followed by the instructions body.
func Parse(data []byte) (Skill, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, errors.New("missing frontmatter")
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			close = i
			break
		}
	}
	if close < 0 {
		return Skill{}, errors.New("unterminated frontmatter")
	}
	values, err := parseFrontmatter(lines[1:close])
	if err != nil {
		return Skill{}, err
	}
	body := strings.TrimSpace(strings.Join(lines[close+1:], "\n"))
	if len(body) > maxBodyBytes {
		return Skill{}, fmt.Errorf("instructions exceed %d bytes", maxBodyBytes)
	}
	return Skill{Name: values["name"], Description: values["description"], Body: body}, nil
}

// parseFrontmatter reads the small YAML subset skills need: scalar values,
// quoted scalars, and folded (>) or literal (|) block scalars. Unknown keys
// are ignored so a skill can carry metadata this package does not use.
func parseFrontmatter(lines []string) (map[string]string, error) {
	values := make(map[string]string)
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid frontmatter line %q", trimmed)
		}
		value = strings.TrimSpace(value)
		if value == ">" || value == ">-" || value == "|" || value == "|-" {
			folded := value[0] == '>'
			var block []string
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) == "" {
					block = append(block, "")
					i++
					continue
				}
				if next[0] != ' ' && next[0] != '\t' {
					break
				}
				block = append(block, strings.TrimSpace(next))
				i++
			}
			value = joinBlock(block, folded)
		}
		values[key] = unquote(value)
	}
	return values, nil
}

// joinBlock folds or preserves the lines of a YAML block scalar.
func joinBlock(lines []string, folded bool) string {
	if !folded {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	var b strings.Builder
	previousEmpty := true
	for _, line := range lines {
		if line == "" {
			b.WriteByte('\n')
			previousEmpty = true
			continue
		}
		if !previousEmpty {
			b.WriteByte(' ')
		}
		b.WriteString(line)
		previousEmpty = false
	}
	return strings.TrimSpace(b.String())
}

// unquote removes one layer of YAML quotes from a scalar.
func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	if value[0] == '"' && value[len(value)-1] == '"' {
		return strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func validate(skill Skill) error {
	if skill.Name == "" || len(skill.Name) > 64 {
		return fmt.Errorf("skill name %q must be 1-64 characters", skill.Name)
	}
	for _, r := range skill.Name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("skill name %q must use letters, digits, '.', '-' or '_'", skill.Name)
	}
	if skill.Description == "" {
		return fmt.Errorf("skill %q has no description", skill.Name)
	}
	if len(skill.Description) > maxDescription {
		return fmt.Errorf("skill %q description exceeds %d bytes", skill.Name, maxDescription)
	}
	if strings.ContainsAny(skill.Description, "\r\n") {
		return fmt.Errorf("skill %q description must be one line", skill.Name)
	}
	if strings.IndexFunc(skill.Description, unicode.IsControl) >= 0 {
		return fmt.Errorf("skill %q description contains control characters", skill.Name)
	}
	if strings.TrimSpace(skill.Body) == "" {
		return fmt.Errorf("skill %q has no instructions", skill.Name)
	}
	return nil
}
