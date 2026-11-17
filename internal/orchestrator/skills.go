package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pilharmonic/internal/instance"
)

// SkillInfo is one installed skill and whether the operator has enabled it.
type SkillInfo struct {
	Name        string
	Description string
	Enabled     bool
}

// skillConfig is the durable opt-out list beside the state file. Skills are
// enabled unless named here, matching the loader's all-installed default.
type skillConfig struct {
	Disabled []string `json:"disabled"`
}

func skillConfigPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "skills.json")
}

func loadSkillConfig(path string) (map[string]bool, error) {
	var config skillConfig
	if err := decodeConfigFile(path, "skills", &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	disabled := make(map[string]bool, len(config.Disabled))
	for _, name := range config.Disabled {
		if name = strings.TrimSpace(name); name != "" {
			disabled[name] = true
		}
	}
	return disabled, nil
}

func saveSkillConfig(path string, disabled map[string]bool) error {
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	b, err := json.Marshal(skillConfig{Disabled: names})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return instance.WriteFileAtomic(path, ".skills-*", b)
}

// ListSkills reports every installed skill, including disabled ones, so the
// operator can turn a skill back on without editing files.
func (s *Service) ListSkills() []SkillInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	installed := s.skills.List()
	infos := make([]SkillInfo, len(installed))
	for i, skill := range installed {
		infos[i] = SkillInfo{
			Name:        skill.Name,
			Description: skill.Description,
			Enabled:     !s.disabledSkills[skill.Name],
		}
	}
	return infos
}

// SetSkillEnabled persists an operator's opt-out or opt-in and rebuilds the
// catalog every Fantasy runner will use for its next attempt. Running attempts
// keep the prompt they started with; the change applies from the next message.
func (s *Service) SetSkillEnabled(name string, enabled bool) error {
	name = strings.TrimSpace(name)
	return s.whileOpen(func() error {
		if _, ok := s.skills.Lookup(name); !ok {
			return fmt.Errorf("unknown skill %q", name)
		}
		next := make(map[string]bool, len(s.disabledSkills)+1)
		for disabled := range s.disabledSkills {
			next[disabled] = true
		}
		if enabled {
			delete(next, name)
		} else {
			next[name] = true
		}
		if err := saveSkillConfig(s.skillConfigPath, next); err != nil {
			return err
		}
		s.disabledSkills = next
		s.applySkillsLocked()
		return nil
	})
}

// applySkillsLocked gives every Fantasy runner the active catalog. Caller
// holds s.mu.
func (s *Service) applySkillsLocked() {
	active := s.skills.Without(s.disabledSkills)
	for name, runner := range s.runners {
		if fantasyRunner, ok := runner.(FantasyRunner); ok {
			fantasyRunner.skills = active
			s.runners[name] = fantasyRunner
		}
	}
}

// loadSkillConfigLocked reads the opt-out list beside the state file. Caller
// holds s.mu.
func (s *Service) loadSkillConfigLocked() error {
	disabled, err := loadSkillConfig(s.skillConfigPath)
	if err != nil {
		return err
	}
	s.disabledSkills = disabled
	s.applySkillsLocked()
	return nil
}
