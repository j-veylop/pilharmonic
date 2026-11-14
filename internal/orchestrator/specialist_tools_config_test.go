package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSpecialistToolsConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "specialist-tools.json")
	must(t, os.WriteFile(path, []byte(body), 0600))
	return path
}

func specialistToolService(t *testing.T) *Service {
	t.Helper()
	s := newTestService(t)
	s.LoadBuiltinSpecialists()
	return s
}

func TestLoadSpecialistToolsReplacesCatalog(t *testing.T) {
	s := specialistToolService(t)
	must(t, s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"reviewer":["read","grep"],"tester":["bash"]}`)))
	if len(s.specialistTools) != 2 || s.specialistTools["reviewer"][1] != "grep" {
		t.Fatalf("bad loaded config: %#v", s.specialistTools)
	}
	must(t, s.LoadSpecialistTools(writeSpecialistToolsConfig(t, `{"explorer":["find"]}`)))
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
	must(t, os.WriteFile(path, make([]byte, maxConfigSize+1), 0600))
	if err := s.LoadSpecialistTools(path); err == nil {
		t.Fatal("accepted oversized config")
	}
}
