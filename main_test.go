package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestSubscriptionLoginCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"openai"}, []string{"codex", "-c", `cli_auth_credentials_store="file"`, "login"}},
		{[]string{"openai", "--device-auth"}, []string{"codex", "-c", `cli_auth_credentials_store="file"`, "login", "--device-auth"}},
	} {
		cmd, err := subscriptionLoginCommand(test.args)
		if err != nil || !reflect.DeepEqual(cmd.Args, test.want) {
			t.Fatalf("login %v: command %v, error %v", test.args, cmd, err)
		}
	}
	for _, args := range [][]string{nil, {"other"}, {"openai", "--with-api-key"}, {"openai", "--device-auth", "extra"}} {
		if _, err := subscriptionLoginCommand(args); !errors.Is(err, errUsage) {
			t.Fatalf("login %v: expected usage error, got %v", args, err)
		}
	}
}

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
