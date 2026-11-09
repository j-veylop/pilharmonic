package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// DefaultProfileName is the built-in profile used by the local router and by
	// tasks that did not choose one. It reproduces the executor's default
	// behavior: no tool restriction, no model override, automatic integration.
	DefaultProfileName = "implementer"

	// maxProfileInstructions bounds trusted profile text carried with a task.
	maxProfileInstructions = 16 << 10
	// maxProfileTools bounds one profile's tool list.
	maxProfileTools = 64
)

// Profile is a durable, orchestrator-owned role definition. It selects execution
// parameters for one task: tool palette, model, trusted instructions, automatic
// integration and delegation. It grants no authority beyond the task's mandate
// and the orchestrator's policies, and the service records the resolved profile
// with every attempt.
//
// An empty Tools list means the executor's default palette. An empty Model
// means the executor's default model.
type Profile struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Model        string   `json:"model,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	// NoIntegrate keeps a successful isolated attempt's changes out of the
	// source checkout. The worktree is retained for the operator.
	NoIntegrate bool `json:"no_integrate,omitempty"`
	// NoSubagents withholds delegation credentials from the task.
	NoSubagents bool `json:"no_subagents,omitempty"`
}

// ProfileSupport is an optional adapter interface. The service validates a
// profile against the executor that will run it, so parameters the adapter
// cannot translate fail visibly instead of being silently ignored. A runner
// that does not implement it accepts the default execution parameters only.
type ProfileSupport interface {
	SupportsProfile(Profile) error
}

func cloneProfile(p Profile) Profile {
	p.Tools = append([]string(nil), p.Tools...)
	return p
}

// cloneProfileRef copies an optional snapshot so clones cannot share it.
func cloneProfileRef(p *Profile) *Profile {
	if p == nil {
		return nil
	}
	cloned := cloneProfile(*p)
	return &cloned
}

func builtinProfiles() map[string]Profile {
	return map[string]Profile{
		DefaultProfileName: {
			Name:        DefaultProfileName,
			Description: "Unrestricted local executor: inspects, edits and runs commands; prepared isolated work is integrated.",
		},
	}
}

func validProfileName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// validateProfile checks a definition before it can be selected. It is
// adapter-independent so profiles can be registered before executors are
// configured; adapter support is checked against the selected executor at start.
func (p Profile) validate() error {
	if !validProfileName(p.Name) {
		return fmt.Errorf("profile name %q must be 1-64 characters of letters, digits, '.', '-' or '_'", p.Name)
	}
	if p.Description != strings.TrimSpace(p.Description) {
		return fmt.Errorf("profile %q description has surrounding whitespace", p.Name)
	}
	if strings.IndexFunc(p.Description, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("profile %q description contains control characters", p.Name)
	}
	if len(p.Model) > 200 || strings.IndexFunc(p.Model, unicode.IsSpace) >= 0 || strings.IndexFunc(p.Model, unicode.IsControl) >= 0 {
		return fmt.Errorf("profile %q model must be a single token of at most 200 characters", p.Name)
	}
	if len(p.Instructions) > maxProfileInstructions {
		return fmt.Errorf("profile %q instructions exceed 16 KiB", p.Name)
	}
	if len(p.Tools) > maxProfileTools {
		return fmt.Errorf("profile %q declares more than %d tools", p.Name, maxProfileTools)
	}
	seen := make(map[string]bool, len(p.Tools))
	for _, tool := range p.Tools {
		if tool == "" || tool != strings.TrimSpace(tool) {
			return fmt.Errorf("profile %q has an empty or padded tool name", p.Name)
		}
		if strings.IndexFunc(tool, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ',' }) >= 0 {
			return fmt.Errorf("profile %q tool %q contains whitespace, a comma or a control character", p.Name, tool)
		}
		if seen[tool] {
			return fmt.Errorf("profile %q lists tool %q twice", p.Name, tool)
		}
		seen[tool] = true
	}
	return nil
}

// validateProfileForRunner rejects a profile the executor cannot honor. A
// runner without ProfileSupport cannot change tools or model, so only the
// default parameters and trusted instructions are allowed.
func validateProfileForRunner(runner Runner, p Profile) error {
	if support, ok := runner.(ProfileSupport); ok {
		if err := support.SupportsProfile(p); err != nil {
			return fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return nil
	}
	if len(p.Tools) > 0 {
		return fmt.Errorf("profile %q selects tools the executor cannot configure", p.Name)
	}
	if p.Model != "" {
		return fmt.Errorf("profile %q selects a model the executor cannot configure", p.Name)
	}
	return nil
}

// RegisterProfile adds or replaces a profile. Existing tasks keep the snapshot
// they recorded when they were accepted, so a later edit cannot change what was
// authorized for them.
func (s *Service) RegisterProfile(p Profile) error {
	if err := p.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if s.profiles == nil {
		s.profiles = builtinProfiles()
	}
	s.profiles[p.Name] = cloneProfile(p)
	return nil
}

// LoadProfiles loads trusted local profile definitions atomically. The built-in
// default profile may be replaced; it may not be removed.
func (s *Service) LoadProfiles(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 1024*1024 {
		return errors.New("profiles config exceeds 1 MiB")
	}
	var config struct {
		Profiles []Profile `json:"profiles"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("profiles config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("profiles config must contain exactly one JSON object")
	}
	if len(config.Profiles) == 0 {
		return errors.New("profiles config defines no profiles")
	}
	loaded := make(map[string]Profile, len(config.Profiles))
	for _, p := range config.Profiles {
		if err := p.validate(); err != nil {
			return err
		}
		if _, exists := loaded[p.Name]; exists {
			return fmt.Errorf("duplicate profile %q", p.Name)
		}
		loaded[p.Name] = cloneProfile(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	if s.profiles == nil {
		s.profiles = builtinProfiles()
	}
	for name, p := range loaded {
		s.profiles[name] = p
	}
	return nil
}

// ListProfiles returns registered profiles ordered by name.
func (s *Service) ListProfiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		result = append(result, cloneProfile(p))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// GetProfile resolves one registered profile.
func (s *Service) GetProfile(name string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[name]
	if !ok {
		return Profile{}, false
	}
	return cloneProfile(p), true
}

// SetRoutingProfile chooses the profile the deterministic local router assigns
// and the profile used by tasks created without an explicit choice.
func (s *Service) SetRoutingProfile(name string) error {
	resolved, err := s.resolveProfileName(name)
	if err != nil {
		return err
	}
	name = resolved
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.routeProfile = name
	return nil
}

// resolveProfileName validates a name and applies the routing default.
func (s *Service) resolveProfileName(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveProfileNameLocked(name)
}

func (s *Service) resolveProfileNameLocked(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if s.routeProfile == "" {
			return DefaultProfileName, nil
		}
		return s.routeProfile, nil
	}
	if _, ok := s.profiles[name]; !ok {
		return "", fmt.Errorf("profile %q unavailable", name)
	}
	return name, nil
}

// resolveProfileLocked returns the snapshot to record for a new task.
func (s *Service) resolveProfileLocked(name string) (string, *Profile, error) {
	resolved, err := s.resolveProfileNameLocked(name)
	if err != nil {
		return "", nil, err
	}
	profile := cloneProfile(s.profiles[resolved])
	return resolved, &profile, nil
}

// profileForTaskLocked resolves the profile a task must run with. A recorded
// snapshot wins, so editing the catalog cannot change an accepted task.
func (s *Service) profileForTaskLocked(t Task) (Profile, error) {
	if t.ProfileSpec != nil {
		return s.limitTaskProfileLocked(t, cloneProfile(*t.ProfileSpec))
	}
	name, err := s.resolveProfileNameLocked(t.ProfileName)
	if err != nil {
		return Profile{}, err
	}
	return s.limitTaskProfileLocked(t, cloneProfile(s.profiles[name]))
}

// SetTaskProfile selects a profile for a queued task without an outstanding
// start request. It snapshots the profile so later catalog edits do not apply.
func (s *Service) SetTaskProfile(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	resolved, spec, err := s.resolveProfileLocked(name)
	if err != nil {
		return err
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if t.Status != Queued || t.ScheduleRequested {
			return errors.New("profile can only be changed on an unscheduled queued task")
		}
		runner, ok := s.runners[t.Agent]
		if !ok {
			return fmt.Errorf("agent %q unavailable", t.Agent)
		}
		effective, err := s.limitTaskProfileLocked(*t, *spec)
		if err != nil {
			return err
		}
		if err = validateProfileForRunner(runner, effective); err != nil {
			return err
		}
		if t.ProfileName == resolved {
			return nil
		}
		old := clone(*t)
		t.ProfileName, t.ProfileSpec = resolved, spec
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "profile", "Profile selected: " + resolved})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

// noAutoIntegrate reports whether the recorded profile withholds automatic
// integration of prepared isolated work.
func (t Task) noAutoIntegrate() bool {
	return t.ProfileSpec != nil && t.ProfileSpec.NoIntegrate
}

// profileToolSet returns the profile's tool list as a set. An empty result means
// the executor's default palette, which is unrestricted from the orchestrator's
// point of view.
func profileToolSet(p Profile) map[string]bool {
	set := make(map[string]bool, len(p.Tools))
	for _, tool := range p.Tools {
		set[tool] = true
	}
	return set
}

// checkDelegatedProfile prevents a child task from widening what its parent may
// do. An unrestricted parent (default palette) may delegate anything; a
// restricted parent may only delegate an equal or narrower profile.
func checkDelegatedProfile(parent, child Profile) error {
	parentTools := profileToolSet(parent)
	childTools := profileToolSet(child)
	if len(parentTools) > 0 {
		if len(childTools) == 0 {
			return fmt.Errorf("delegated profile %q selects the executor default tools, which the parent profile %q does not allow", child.Name, parent.Name)
		}
		for tool := range childTools {
			if !parentTools[tool] {
				return fmt.Errorf("delegated profile %q grants tool %q that parent profile %q does not have", child.Name, tool, parent.Name)
			}
		}
	}
	if parent.NoIntegrate && !child.NoIntegrate {
		return fmt.Errorf("delegated profile %q may integrate work that parent profile %q may not", child.Name, parent.Name)
	}
	if parent.NoSubagents && !child.NoSubagents {
		return fmt.Errorf("delegated profile %q may delegate work that parent profile %q may not", child.Name, parent.Name)
	}
	return nil
}
