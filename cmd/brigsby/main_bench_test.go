package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkCLIHelp(b *testing.B) {
	for b.Loop() {
		command := newRootCommand(io.Discard, io.Discard)
		command.SetArgs([]string{"--help"})
		if err := command.Execute(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHarnessSyncDryRun(b *testing.B) {
	home := b.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.Setenv("HOME", home)
	b.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != 0 {
			b.Fatalf("setup %v failed with %d: %s", arguments, code, stderr.String())
		}
	}

	b.ResetTimer()
	for b.Loop() {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--dry-run"}, &stdout, &stderr); code != 1 {
			b.Fatalf("dry-run failed with %d: %s", code, stderr.String())
		}
	}
}

func BenchmarkRootSyncDryRunJSON(b *testing.B) {
	home := b.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.Setenv("HOME", home)
	b.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"add", source}} {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != 0 {
			b.Fatalf("setup %v failed with %d: %s", arguments, code, stderr.String())
		}
	}

	b.ResetTimer()
	for b.Loop() {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--dry-run", "--json", "all"}, &stdout, &stderr); code != 1 {
			b.Fatalf("JSON dry-run failed with %d: %s", code, stderr.String())
		}
	}
}
