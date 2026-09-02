package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseManifestCheckerAcceptsExplicitPublicAllowList(t *testing.T) {
	t.Parallel()

	output := runManifestChecker(t, "--manifest", filepath.Join("testdata", "valid.toml"))
	if got, want := strings.TrimSpace(output), "release manifest valid: 2 export entries"; got != want {
		t.Fatalf("checker output = %q, want %q", got, want)
	}
}

func TestReleaseManifestCheckerRejectsPrivatePaths(t *testing.T) {
	t.Parallel()

	command := exec.Command("go", "run", ".", "--manifest", filepath.Join("testdata", "private-path.toml"))
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "must not export private development path") {
		t.Fatalf("checker output = %q, want private-path error", output)
	}
}

func runManifestChecker(t *testing.T, arguments ...string) string {
	t.Helper()

	command := exec.Command("go", append([]string{"run", "."}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release-manifest-check %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
