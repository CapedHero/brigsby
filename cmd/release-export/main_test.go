package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseExportStagesReviewedManifestWithoutPublishing(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("# Public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(source, "release-manifest.toml")
	if err := os.WriteFile(manifest, []byte("schema = 1\n\n[export]\nallow = [\"README.md\", \"cmd\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte("cmd/*.secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "--initial-branch=main", source}, {"-C", source, "config", "user.email", "test@example.com"}, {"-C", source, "config", "user.name", "Test"}, {"-C", source, "add", "."}, {"-C", source, "commit", "-m", "fixture"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	destination := filepath.Join(t.TempDir(), "staged")
	var stdout, stderr strings.Builder
	if got := run([]string{"--manifest", manifest, "--source", source, "--destination", destination}, &stdout, &stderr); got != 0 {
		t.Fatalf("release-export exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "staged release: files=2 manifest=sha256-") || !strings.Contains(stdout.String(), "exporter=brigsby-release-export/v1") || !strings.Contains(stdout.String(), "source=") || !strings.Contains(stdout.String(), "FILE README.md") || !strings.Contains(stdout.String(), "FILE cmd/main.go") {
		t.Fatalf("release-export output=%q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(destination, "README.md")); err != nil {
		t.Fatalf("missing staged public file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "untracked.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secondDestination := filepath.Join(t.TempDir(), "blocked")
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"--manifest", manifest, "--source", source, "--destination", secondDestination}, &stdout, &stderr); got != 1 {
		t.Fatalf("release-export with untracked source exit=%d stderr=%s", got, stderr.String())
	}
	if _, err := os.Stat(secondDestination); !os.IsNotExist(err) {
		t.Fatalf("untracked source created a destination: %v", err)
	}
	if err := os.Remove(filepath.Join(source, "cmd", "untracked.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "private.secret"), []byte("must not export\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	thirdDestination := filepath.Join(t.TempDir(), "ignored-blocked")
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"--manifest", manifest, "--source", source, "--destination", thirdDestination}, &stdout, &stderr); got != 1 {
		t.Fatalf("release-export with ignored source exit=%d stderr=%s", got, stderr.String())
	}
	if _, err := os.Stat(thirdDestination); !os.IsNotExist(err) {
		t.Fatalf("ignored source created a destination: %v", err)
	}
}
