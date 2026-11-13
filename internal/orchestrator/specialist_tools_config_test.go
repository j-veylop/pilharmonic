package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSpecialistToolsConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "specialist-tools.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func specialistToolService(t *testing.T) *Service {
	t.Helper()
	s := openTestService(t, filepath.Join(t.TempDir(), "state.json"))
	s.LoadBuiltinSpecialists()
	return s
}

func TestLoadSpecialistToolsReplacesCatalog(t *testing.T) {
	s := specialistToolService(t)
	if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read","grep"],"tester":["bash"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.specialistTools) != 2 || s.specialistTools["reviewer"][1] != "grep" {
		t.Fatalf("bad loaded config: %#v", s.specialistTools)
	}
	if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"explorer":["find"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.specialistTools) != 1 || s.specialistTools["explorer"][0] != "find" {
		t.Fatalf("catalog was not replaced atomically: %#v", s.specialistTools)
	}
}

func TestLoadSpecialistToolsRejectsInvalidConfigsWithoutMutation(t *testing.T) {
	s := specialistToolService(t)
	s.specialistTools = map[string][]string{"reviewer": {"read"}}
	for _, body := range []string{
		`{"reviewer":["read"],"reviewer":["bash"]}`,
		`{"reviewer":["read","read"]}`,
		`{"reviewer":[]}`,
		`{"reviewer":["unknown"]}`,
		`{"general":["read"]}`,
		`{"reviewer":["read"]} {}`,
		`[]`,
	} {
		if err := s.LoadSpecialistTools(writeSpecialistToolsConfig(t, body)); err == nil {
			t.Errorf("accepted %s", body)
		}
		if len(s.specialistTools) != 1 || s.specialistTools["reviewer"][0] != "read" {
			t.Fatalf("invalid config mutated live settings: %#v", s.specialistTools)
		}
	}
}

func TestLoadSpecialistToolsRejectsOversize(t *testing.T) {
	s := specialistToolService(t)
	path := writeSpecialistToolsConfig(t, `{"reviewer":["read"]}`)
	if err := os.WriteFile(path, make([]byte, maxConfigSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadSpecialistTools(path); err == nil {
		t.Fatal("accepted oversized config")
	}
}
