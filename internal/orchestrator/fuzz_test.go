package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// FuzzOpenState covers state-file decoding and startup recovery. Arbitrary
// bytes must produce either a usable service or an error, never a panic, and
// recovery must not write a state file it cannot read back.
func FuzzOpenState(f *testing.F) {
	for _, seed := range []string{
		``,
		`{}`,
		`{"tasks":[]}`,
		`{"tasks":[{"id":"a","status":"running","attempts":[{"id":"b","status":"running"}]}]}`,
		`{"tasks":[{"id":"a","status":"queued","schedule_requested":true}]}`,
		`not json`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Skip()
		}
		s, err := Open(path)
		if err != nil {
			return
		}
		_ = s.Shutdown()
		reopened, err := Open(path)
		if err != nil {
			t.Fatalf("state written by recovery could not be reopened: %v", err)
		}
		_ = reopened.Shutdown()
	})
}

// FuzzToolActivitySummary covers untrusted tool arguments. Rendering must not
// panic and a bounded payload stays valid JSON within the size limit.
func FuzzToolActivitySummary(f *testing.F) {
	for _, seed := range []string{
		`{"command":"go test ./..."}`,
		`{"path":"/tmp/x"}`,
		`[]`,
		`"text"`,
		`{"command":12}`,
		`{`,
	} {
		f.Add("bash", []byte(seed))
	}
	f.Fuzz(func(t *testing.T, name string, args []byte) {
		activity := ToolActivity{Name: name, Args: json.RawMessage(args)}
		_ = activity.Summary()
		bounded := boundedPayload(json.RawMessage(args))
		if bounded == nil {
			return
		}
		if !json.Valid(bounded) {
			t.Fatalf("bounded payload is not valid JSON: %q", bounded)
		}
		if len(bounded) > maxActivityPayload {
			t.Fatalf("bounded payload is %d bytes, limit is %d", len(bounded), maxActivityPayload)
		}
	})
}
