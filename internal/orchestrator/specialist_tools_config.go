package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// LoadSpecialistTools replaces the specialist tool overrides with the contents
// of a JSON object mapping bundled specialist names to Pi tool lists. It walks
// the tokens rather than decoding into a map so a duplicated name is an error
// instead of a silent overwrite.
func (s *Service) LoadSpecialistTools(path string) error {
	body, err := readConfigFile(path, "specialist tools")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	loaded := make(map[string][]string)
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("specialist tools config: %w", err)
	}
	if token != json.Delim('{') {
		return errors.New("specialist tools config must be a JSON object")
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("specialist tools config: %w", err)
		}
		name, ok := token.(string)
		if !ok || !validProfileName(name) {
			return fmt.Errorf("invalid specialist name %v", token)
		}
		if _, exists := loaded[name]; exists {
			return fmt.Errorf("duplicate specialist %q", name)
		}
		var tools []string
		if err = decoder.Decode(&tools); err != nil {
			return fmt.Errorf("specialist %q tools: %w", name, err)
		}
		if err := validateSpecialistTools(name, tools); err != nil {
			return err
		}
		loaded[name] = tools
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("specialist tools config: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("specialist tools config must contain exactly one JSON object")
	}
	if len(loaded) == 0 {
		return errors.New("specialist tools config defines no specialists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for name := range loaded {
		runner, ok := s.runners[name].(FantasyRunner)
		if !ok || runner.instructions == "" {
			return fmt.Errorf("specialist %q is not a bundled specialist", name)
		}
	}
	s.specialistTools = loaded
	return nil
}

func isSupportedPiTool(tool string) bool {
	for _, supported := range strings.Split(piTools, ",") {
		if tool == supported {
			return true
		}
	}
	return false
}

func validateSpecialistTools(name string, tools []string) error {
	if len(tools) == 0 {
		return fmt.Errorf("specialist %q must have a nonempty tool list", name)
	}
	if err := (Profile{Name: name, Tools: tools}).validate(); err != nil {
		return err
	}
	for _, tool := range tools {
		if !isSupportedPiTool(tool) {
			return fmt.Errorf("specialist %q uses unsupported Pi tool %q", name, tool)
		}
	}
	return nil
}
