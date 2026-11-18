package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/j-veylop/pilharmonic/internal/instance"
)

// SkillInfo is one installed skill and whether the operator has enabled it.
type SkillInfo struct {
	Name           string
	Description    string
	Enabled        bool
	GlobalDisabled bool
}

// skillConfig is the durable opt-out list beside the state file. Skills are
// enabled unless named here, matching the loader's all-installed default.
type skillConfig struct {
	Disabled []string            `json:"disabled"`
	Agents   map[string][]string `json:"agents,omitempty"`
}

func saveAgentSkillConfig(path string, agents map[string]map[string]bool) error {
	var config skillConfig
	if err := decodeConfigFile(path, "skills", &config); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config.Agents = make(map[string][]string, len(agents))
	for agent, disabled := range agents {
		for name := range disabled {
			config.Agents[agent] = append(config.Agents[agent], name)
		}
		sort.Strings(config.Agents[agent])
	}
	b, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return instance.WriteFileAtomic(path, ".skills-*", b)
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
	var config skillConfig
	if err := decodeConfigFile(path, "skills", &config); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config.Disabled = names
	b, err := json.Marshal(config)
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
	for name, runner := range s.runners {
		if fantasyRunner, ok := runner.(FantasyRunner); ok {
			disabled := make(map[string]bool, len(s.disabledSkills)+len(s.agentDisabledSkills[name]))
			for skill := range s.disabledSkills {
				disabled[skill] = true
			}
			for skill := range s.agentDisabledSkills[name] {
				disabled[skill] = true
			}
			fantasyRunner.skills = s.skills.Without(disabled)
			s.runners[name] = fantasyRunner
		}
	}
}

// ListAgentSkills returns per-agent settings and indicates global disables separately.
func (s *Service) ListAgentSkills(agent string) []SkillInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listSkillsLocked(agent)
}

func (s *Service) listSkillsLocked(agent string) []SkillInfo {
	installed := s.skills.List()
	infos := make([]SkillInfo, len(installed))
	for i, skill := range installed {
		infos[i] = SkillInfo{Name: skill.Name, Description: skill.Description,
			Enabled: !s.agentDisabledSkills[agent][skill.Name], GlobalDisabled: s.disabledSkills[skill.Name]}
	}
	return infos
}

// SetAgentSkillEnabled durably sets one agent's skill opt-out without changing global state.
func (s *Service) SetAgentSkillEnabled(agent, name string, enabled bool) error {
	name = strings.TrimSpace(name)
	return s.whileOpen(func() error {
		if _, ok := s.runners[agent]; !ok {
			return fmt.Errorf("agent %q unavailable", agent)
		}
		if _, ok := s.skills.Lookup(name); !ok {
			return fmt.Errorf("unknown skill %q", name)
		}
		next := make(map[string]map[string]bool, len(s.agentDisabledSkills)+1)
		for key, values := range s.agentDisabledSkills {
			copy := make(map[string]bool, len(values))
			for skill := range values {
				copy[skill] = true
			}
			next[key] = copy
		}
		if next[agent] == nil {
			next[agent] = map[string]bool{}
		}
		if enabled {
			delete(next[agent], name)
		} else {
			next[agent][name] = true
		}
		if len(next[agent]) == 0 {
			delete(next, agent)
		}
		if err := saveAgentSkillConfig(s.skillConfigPath, next); err != nil {
			return err
		}
		s.agentDisabledSkills = next
		s.applySkillsLocked()
		return nil
	})
}

// loadSkillConfigLocked reads the opt-out list beside the state file. Caller
// holds s.mu.
func (s *Service) loadSkillConfigLocked() error {
	disabled, err := loadSkillConfig(s.skillConfigPath)
	if err != nil {
		return err
	}
	s.disabledSkills = disabled
	var config skillConfig
	if err := decodeConfigFile(s.skillConfigPath, "skills", &config); err == nil {
		s.agentDisabledSkills = make(map[string]map[string]bool, len(config.Agents))
		for agent, names := range config.Agents {
			s.agentDisabledSkills[agent] = make(map[string]bool, len(names))
			for _, name := range names {
				s.agentDisabledSkills[agent][name] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.applySkillsLocked()
	return nil
}
