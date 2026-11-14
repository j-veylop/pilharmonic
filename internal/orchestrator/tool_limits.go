package orchestrator

import (
	"fmt"
	"slices"
)

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
