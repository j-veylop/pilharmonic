package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/j-veylop/pilharmonic/internal/instance"
)

// languageCheckInfo describes one recognized language toolchain and whether the
// operator has enabled its automatic checks for the current workspace.
type LanguageCheckInfo struct {
	ID       string
	Name     string
	Marker   string
	Commands string
	Detected bool
	Enabled  bool
}

// checkSpec is one recognized stack. Commands are fixed argv templates; the
// model never supplies them. {files} expands to changed files of this stack
// and {packages} to the packages containing them.
type checkSpec struct {
	ID         string
	Name       string
	Marker     string
	Extensions []string
	Format     []string
	Lint       []string
	Fallback   []string
	Test       []string
}

var checkSpecs = []checkSpec{
	{
		ID: "go", Name: "Go", Marker: "go.mod", Extensions: []string{".go"},
		Format:   []string{"gofmt", "-l", "{files}"},
		Lint:     []string{"gopls", "check", "{files}"},
		Fallback: []string{"go", "vet", "{packages}"},
		Test:     []string{"go", "test", "{packages}"},
	},
	{
		ID: "node", Name: "Node/TypeScript", Marker: "package.json", Extensions: []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"},
		Format: []string{"prettier", "--check", "{files}"},
		Lint:   []string{"eslint", "{files}"},
		Test:   []string{"npm", "test", "--silent"},
	},
	{
		ID: "rust", Name: "Rust", Marker: "Cargo.toml", Extensions: []string{".rs"},
		Format: []string{"rustfmt", "--check", "{files}"},
		Lint:   []string{"cargo", "clippy", "--quiet"},
		Test:   []string{"cargo", "test", "--quiet"},
	},
	{
		ID: "python", Name: "Python", Marker: "pyproject.toml", Extensions: []string{".py"},
		Format: []string{"ruff", "format", "--check", "{files}"},
		Lint:   []string{"ruff", "check", "{files}"},
		Test:   []string{"pytest", "-q"},
	},
}

// checkSettings is the durable opt-out list shared by every FantasyRunner.
type checkSettings struct {
	mu       sync.RWMutex
	disabled map[string]bool
}

func newCheckSettings(disabled map[string]bool) *checkSettings {
	return &checkSettings{disabled: disabled}
}

func (c *checkSettings) enabled(id string) bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.disabled[id]
}

func (c *checkSettings) set(id string, enabled bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if enabled {
		delete(c.disabled, id)
		return
	}
	c.disabled[id] = true
}

func (c *checkSettings) disabledIDs() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.disabled))
	for id := range c.disabled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// checkConfig is the durable lsps.json shape beside the state file. Checks are
// enabled unless named here.
type checkConfig struct {
	Disabled []string `json:"disabled,omitempty"`
}

func checkConfigPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "lsps.json")
}

func loadCheckConfig(path string) (*checkSettings, error) {
	var config checkConfig
	if err := decodeConfigFile(path, "lsps", &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newCheckSettings(map[string]bool{}), nil
		}
		return nil, err
	}
	disabled := make(map[string]bool, len(config.Disabled))
	for _, id := range config.Disabled {
		if id = strings.TrimSpace(id); id != "" {
			disabled[id] = true
		}
	}
	return newCheckSettings(disabled), nil
}

func saveCheckConfig(path string, disabled []string) error {
	b, err := json.Marshal(checkConfig{Disabled: disabled})
	if err != nil {
		return err
	}
	return instance.WriteFileAtomic(path, ".lsps-*", b)
}

// ListLanguageChecks reports every recognized stack, whether its marker exists
// in workspace, and whether its automatic checks are enabled.
func (s *Service) ListLanguageChecks(workspace string) []LanguageCheckInfo {
	s.mu.Lock()
	checks := s.checks
	s.mu.Unlock()
	infos := make([]LanguageCheckInfo, 0, len(checkSpecs))
	for _, spec := range checkSpecs {
		detected := markerExists(workspace, spec.Marker)
		infos = append(infos, LanguageCheckInfo{
			ID:       spec.ID,
			Name:     spec.Name,
			Marker:   spec.Marker,
			Commands: strings.Join(commandNames(spec), ", "),
			Detected: detected,
			Enabled:  checks.enabled(spec.ID),
		})
	}
	return infos
}

// SetLanguageCheckEnabled persists one language check's opt-in state. The
// identifier must be one this build recognizes.
func (s *Service) SetLanguageCheckEnabled(id string, enabled bool) error {
	id = strings.TrimSpace(id)
	if checkSpecByID(id) == nil {
		return errors.New("unknown language check: " + id)
	}
	return s.whileOpen(func() error {
		checks := s.checks
		checks.mu.Lock()
		wasDisabled := checks.disabled[id]
		if enabled {
			delete(checks.disabled, id)
		} else {
			checks.disabled[id] = true
		}
		disabled := make([]string, 0, len(checks.disabled))
		for name := range checks.disabled {
			disabled = append(disabled, name)
		}
		checks.mu.Unlock()
		if err := saveCheckConfig(s.checkPath, disabled); err != nil {
			checks.mu.Lock()
			if wasDisabled {
				checks.disabled[id] = true
			} else {
				delete(checks.disabled, id)
			}
			checks.mu.Unlock()
			return err
		}
		return nil
	})
}

func checkSpecByID(id string) *checkSpec {
	for i := range checkSpecs {
		if checkSpecs[i].ID == id {
			return &checkSpecs[i]
		}
	}
	return nil
}

func markerExists(workspace, marker string) bool {
	if workspace == "" || marker == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(workspace, marker)); err == nil {
		return true
	}
	// Python projects often use setup.py or requirements.txt instead.
	if marker == "pyproject.toml" {
		for _, alternative := range []string{"setup.py", "requirements.txt"} {
			if _, err := os.Stat(filepath.Join(workspace, alternative)); err == nil {
				return true
			}
		}
	}
	return false
}

func commandNames(spec checkSpec) []string {
	var names []string
	for _, argv := range [][]string{spec.Format, spec.Lint, spec.Test} {
		if len(argv) > 0 {
			names = appendUniqueString(names, argv[0])
		}
	}
	return names
}

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// detectedChecks returns the enabled specs whose marker exists in workspace and
// that own at least one changed file.
func detectedChecks(workspace string, files []string, checks *checkSettings) []checkSpec {
	var result []checkSpec
	for _, spec := range checkSpecs {
		if !checks.enabled(spec.ID) || !markerExists(workspace, spec.Marker) {
			continue
		}
		if len(filesForSpec(spec, files)) == 0 {
			continue
		}
		result = append(result, spec)
	}
	return result
}

func filesForSpec(spec checkSpec, files []string) []string {
	var result []string
	for _, file := range files {
		extension := strings.ToLower(filepath.Ext(file))
		for _, wanted := range spec.Extensions {
			if extension == wanted {
				result = appendUniqueString(result, filepath.ToSlash(file))
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

// diagnosticsForWorkspace runs only the lint step of enabled stacks, for the
// diagnostics tool.
func diagnosticsForWorkspace(ctx context.Context, workspace string, files []string, checks *checkSettings) string {
	specs := detectedChecks(workspace, files, checks)
	if len(files) == 0 {
		specs = detectedWorkspaceChecks(workspace, checks)
	}
	var reports []string
	for _, spec := range specs {
		changed := filesForSpec(spec, files)
		if len(changed) == 0 {
			changed = workspaceFilesForSpec(workspace, spec)
		}
		if out := runLint(ctx, workspace, spec, changed); out != "" {
			reports = append(reports, spec.Name+":\n"+out)
		}
	}
	return strings.Join(reports, "\n\n")
}

func detectedWorkspaceChecks(workspace string, checks *checkSettings) []checkSpec {
	var result []checkSpec
	for _, spec := range checkSpecs {
		if checks.enabled(spec.ID) && markerExists(workspace, spec.Marker) {
			result = append(result, spec)
		}
	}
	return result
}

// workspaceFilesForSpec collects a bounded set of files owning a recognized
// extension when the caller did not name any. Diagnostics without explicit
// paths therefore still has something to check.
func workspaceFilesForSpec(workspace string, spec checkSpec) []string {
	var result []string
	_ = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".hg", ".svn", "node_modules", ".cache", "target", "vendor", "dist", "build":
				if path != workspace {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		extension := strings.ToLower(filepath.Ext(rel))
		for _, wanted := range spec.Extensions {
			if extension == wanted {
				result = append(result, rel)
				if len(result) >= 20 {
					return filepath.SkipAll
				}
				return nil
			}
		}
		return nil
	})
	sort.Strings(result)
	return result
}
