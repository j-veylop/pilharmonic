package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditedPathsExtractsChangedFiles(t *testing.T) {
	if got := editedPaths("edit", `{"path":"a.go","old_text":"x","new_text":"y"}`); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("edit paths = %v", got)
	}
	if got := editedPaths("write", `{"path":"b.go","content":"x"}`); len(got) != 1 || got[0] != "b.go" {
		t.Fatalf("write paths = %v", got)
	}
	got := editedPaths("multiedit", `{"edits":[{"path":"a.go"},{"path":"b.go"}]}`)
	if strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("multiedit paths = %v", got)
	}
	if got := editedPaths("read", `{"path":"a.go"}`); len(got) != 0 {
		t.Fatalf("read should not verify: %v", got)
	}
}

func TestVerificationSkipsNonGoWorkspace(t *testing.T) {
	if report, _ := verificationReport(context.Background(), t.TempDir(), []string{"a.go"}, nil); report != "" {
		t.Fatalf("non-Go workspace produced a report: %q", report)
	}
}

func TestVerificationReportsFormattingAndFailingTests(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/verify\n\ngo 1.22\n"), 0o644))
	// Misformatted source catches the gofmt check before tests run.
	must(t, os.WriteFile(filepath.Join(workspace, "lib.go"), []byte("package verify\nfunc Add(a int) int{ return a }\n"), 0o644))
	report, _ := verificationReport(context.Background(), workspace, []string{"lib.go"}, nil)
	if !strings.Contains(report, "Go format") || !strings.Contains(report, "lib.go") {
		t.Fatalf("gofmt failure not reported: %q", report)
	}

	// Formatted source with a failing test reaches the test check.
	must(t, os.WriteFile(filepath.Join(workspace, "lib.go"), []byte("package verify\n\nfunc Add(a int) int { return a }\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(workspace, "lib_test.go"), []byte("package verify\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1) != 2 { t.Fatal(\"bad\") } }\n"), 0o644))
	report, _ = verificationReport(context.Background(), workspace, []string{"lib.go"}, nil)
	if !strings.Contains(report, "Go tests") || !strings.Contains(report, "bad") {
		t.Fatalf("test failure not reported: %q", report)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
}

func TestSkippedChecksNamesMissingToolsAndCoversFallback(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"))
	t.Setenv("PATH", bin)

	missing := checkSpec{
		Name:   "Missing",
		Format: []string{"missing-format-xyz"},
		Lint:   []string{"missing-lint-xyz"},
		Test:   []string{"missing-test-xyz"},
	}
	got := skippedChecks([]checkSpec{missing})
	want := "missing-format-xyz,missing-lint-xyz,missing-test-xyz"
	if strings.Join(got, ",") != want {
		t.Fatalf("skipped checks = %v, want %s", got, want)
	}

	covered := checkSpec{Name: "Covered", Lint: []string{"missing-lint-xyz"}, Fallback: []string{"go"}}
	if got := skippedChecks([]checkSpec{covered}); len(got) != 0 {
		t.Fatalf("installed fallback still reported missing lint: %v", got)
	}
}

func TestVerificationReportExplainsSkippedTools(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/skip\n\ngo 1.22\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(workspace, "lib.go"), []byte("package skip\n"), 0o600))
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"))
	t.Setenv("PATH", bin)

	report, skipped := verificationReport(context.Background(), workspace, []string{"lib.go"}, nil)
	if report != "" {
		t.Fatalf("missing tools produced a failure report: %q", report)
	}
	if !strings.Contains(skipped, "gofmt") || !strings.Contains(skipped, "not a pass") {
		t.Fatalf("missing gofmt not explained: %q", skipped)
	}

	writeExecutable(t, filepath.Join(bin, "gofmt"))
	report, skipped = verificationReport(context.Background(), workspace, []string{"lib.go"}, nil)
	if report != "" || skipped != "" {
		t.Fatalf("installed tools still reported: report=%q skipped=%q", report, skipped)
	}
}
