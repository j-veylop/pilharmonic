package orchestrator

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"pilharmonic/internal/agenttools"
)

const (
	// DefaultProfileName is the built-in profile used by the local router and by
	// tasks that did not choose one. It reproduces the executor's default
	// behavior: no tool restriction, no model override, automatic integration.
	// The name is retained for compatibility with persisted tasks.
	DefaultProfileName = "implementer"

	// CoordinatorProfileName is the built-in top-level coordinator profile. The
	// shipped profiles file selects it as the routing default and overrides its
	// model; its delegation workflow is compiled in.
	CoordinatorProfileName = "coordinator"

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
	// CoordinatorInstructions guide top-level general runs when shell delegation is available.
	CoordinatorInstructions string `json:"coordinator_instructions,omitempty"`
	// NoIntegrate keeps a successful isolated attempt's changes out of the
	// source checkout. The worktree is retained for the operator.
	NoIntegrate bool `json:"no_integrate,omitempty"`
	// NoSubagents withholds delegation credentials from the task.
	NoSubagents bool `json:"no_subagents,omitempty"`
}

// UsesExecutorDefaults reports whether a built-in profile name carries no
// execution overrides. Check the snapshot because operators can redefine it.
func (p Profile) UsesExecutorDefaults() bool {
	builtin := p.Name == DefaultProfileName || p.Name == CoordinatorProfileName
	return builtin && len(p.Tools) == 0 && p.Model == "" &&
		p.Instructions == "" && p.CoordinatorInstructions == "" && !p.NoIntegrate && !p.NoSubagents
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
		CoordinatorProfileName: {
			Name:        CoordinatorProfileName,
			Description: "Top-level coordinator; canonical delegation guidance is compiled in.",
		},
	}
}

// profileConfig modifies an existing built-in profile, or defines a new one.
// Pointer fields distinguish a JSON field that is absent (keep the built-in
// value) from one that is explicitly empty (clear it).
type profileConfig struct {
	Name                    string    `json:"name"`
	Description             *string   `json:"description,omitempty"`
	Tools                   *[]string `json:"tools,omitempty"`
	Model                   *string   `json:"model,omitempty"`
	Instructions            *string   `json:"instructions,omitempty"`
	CoordinatorInstructions *string   `json:"coordinator_instructions,omitempty"`
	NoIntegrate             *bool     `json:"no_integrate,omitempty"`
	NoSubagents             *bool     `json:"no_subagents,omitempty"`
}

// resolve overlays the configured fields on a built-in definition. New names
// start from an empty profile, so JSON can extend the catalog as well as modify
// compiled defaults.
func (c profileConfig) resolve(base map[string]Profile) Profile {
	p := cloneProfile(base[c.Name])
	p.Name = c.Name
	if c.Description != nil {
		p.Description = *c.Description
	}
	if c.Tools != nil {
		p.Tools = append([]string(nil), (*c.Tools)...)
	}
	if c.Model != nil {
		p.Model = *c.Model
	}
	if c.Instructions != nil {
		p.Instructions = *c.Instructions
	}
	if c.CoordinatorInstructions != nil {
		p.CoordinatorInstructions = *c.CoordinatorInstructions
	}
	if c.NoIntegrate != nil {
		p.NoIntegrate = *c.NoIntegrate
	}
	if c.NoSubagents != nil {
		p.NoSubagents = *c.NoSubagents
	}
	return p
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
	if len(p.Instructions)+len(p.CoordinatorInstructions) > maxProfileInstructions {
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

type specialistConfig struct {
	Name  string   `json:"name"`
	Model string   `json:"model,omitempty"`
	Tools []string `json:"tools,omitempty"`
}

// LoadProfiles loads trusted local profile definitions atomically. A JSON entry
// overlays the matching built-in profile field by field, so operators can change
// only what they need; a new name extends the catalog. Built-in profiles may be
// redefined but not removed.
func (s *Service) LoadProfiles(path string) error {
	var config struct {
		Profiles       []profileConfig    `json:"profiles"`
		DefaultProfile string             `json:"default_profile"`
		Specialists    []specialistConfig `json:"specialists"`
	}
	if err := decodeConfigFile(path, "profiles", &config); err != nil {
		return err
	}
	if len(config.Profiles) == 0 && len(config.Specialists) == 0 && config.DefaultProfile == "" {
		return errors.New("profiles config defines no profiles")
	}
	builtins := builtinProfiles()
	loaded := make(map[string]Profile, len(config.Profiles))
	for _, entry := range config.Profiles {
		p := entry.resolve(builtins)
		if err := p.validate(); err != nil {
			return err
		}
		if _, exists := loaded[p.Name]; exists {
			return fmt.Errorf("duplicate profile %q", p.Name)
		}
		loaded[p.Name] = p
	}
	return s.whileOpen(func() error {
		if name := config.DefaultProfile; name != "" {
			_, defined := loaded[name]
			_, registered := s.profiles[name]
			_, builtin := builtins[name]
			if !defined && !registered && !builtin {
				return fmt.Errorf("default profile %q unavailable", name)
			}
		}
		if err := s.checkSpecialistsLocked(config.Specialists); err != nil {
			return err
		}
		if config.DefaultProfile != "" {
			s.routeProfile = config.DefaultProfile
		}
		for _, specialist := range config.Specialists {
			if specialist.Model != "" {
				s.agentModels[specialist.Name] = specialist.Model
			}
			if specialist.Tools != nil {
				if s.specialistTools == nil {
					s.specialistTools = make(map[string][]string)
				}
				s.specialistTools[specialist.Name] = specialist.Tools
			}
		}
		maps.Copy(s.profiles, loaded)
		return nil
	})
}

// checkSpecialistsLocked validates specialist overrides before any is applied.
func (s *Service) checkSpecialistsLocked(specialists []specialistConfig) error {
	seen := make(map[string]bool)
	for _, specialist := range specialists {
		name := specialist.Name
		if runner, ok := s.runners[name].(FantasyRunner); !ok || runner.instructions == "" {
			return fmt.Errorf("%q is not a bundled specialist", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate specialist %q", name)
		}
		seen[name] = true
		if specialist.Model == "" && len(specialist.Tools) == 0 {
			return fmt.Errorf("specialist %q needs a model or tools", name)
		}
		if err := (Profile{Name: name, Model: specialist.Model, Tools: specialist.Tools}).validate(); err != nil {
			return err
		}
		if specialist.Tools != nil {
			if err := validateSpecialistTools(name, specialist.Tools); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSpecialistTools(name string, tools []string) error {
	if len(tools) == 0 {
		return fmt.Errorf("specialist %q must have a nonempty tool list", name)
	}
	if err := (Profile{Name: name, Tools: tools}).validate(); err != nil {
		return err
	}
	for _, tool := range tools {
		if !slices.Contains(agenttools.Names(), tool) {
			return fmt.Errorf("specialist %q uses unsupported tool %q", name, tool)
		}
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

// SetRoutingProfile chooses the profile the deterministic local router assigns
// and the profile used by tasks created without an explicit choice.
func (s *Service) SetRoutingProfile(name string) error {
	resolved, err := s.resolveProfileName(name)
	if err != nil {
		return err
	}
	return s.whileOpen(func() error { s.routeProfile = resolved; return nil })
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
	return s.updateTask(id, func(t *Task) error {
		resolved, spec, err := s.resolveProfileLocked(name)
		if err != nil {
			return err
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
		t.ProfileName, t.ProfileSpec = resolved, spec
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "profile", "Profile selected: " + resolved})
		return nil
	})
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

// An unspecified list leaves the other list in effect. Reject disjoint lists
// because an empty tool list means the executor defaults.
func intersectTools(tools, limit []string) ([]string, error) {
	if len(limit) == 0 {
		return tools, nil
	}
	if len(tools) == 0 {
		return slices.Clone(limit), nil
	}
	var allowed []string
	for _, tool := range tools {
		if slices.Contains(limit, tool) {
			allowed = append(allowed, tool)
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("no tools in common between %v and %v", tools, limit)
	}
	return allowed, nil
}

func (s *Service) limitTaskProfileLocked(t Task, profile Profile) (Profile, error) {
	for _, limit := range [][]string{t.InheritedTools, t.SpecialistTools, s.specialistTools[t.Agent]} {
		var err error
		profile.Tools, err = intersectTools(profile.Tools, limit)
		if err != nil {
			return Profile{}, fmt.Errorf("executor %q profile %q: %w", t.Agent, profile.Name, err)
		}
	}
	return profile, nil
}
