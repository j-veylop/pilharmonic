package main

import "testing"

func TestVersionStringIncludesCommit(t *testing.T) {
	oldVersion, oldCommit := version, commit
	defer func() { version, commit = oldVersion, oldCommit }()
	version, commit = "v1.2.3", "abc123"
	if got := versionString(); got != "v1.2.3 (abc123)" {
		t.Fatalf("version string %q", got)
	}
	commit = ""
	if got := versionString(); got != "v1.2.3" {
		t.Fatalf("version string %q", got)
	}
}
