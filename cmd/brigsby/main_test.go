package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/CapedHero/brigsby/internal/harness"
	"github.com/CapedHero/brigsby/internal/recovery"
)

func TestRecoveryListShowsAppliedSyncOperation(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	candidates, err := discoverPersonalCandidates()
	if err != nil {
		t.Fatalf("discover candidates: %v", err)
	}
	if candidates[0].candidate.InstructionsPath != "" {
		t.Fatalf("unstructured Codex home instruction path = %q, want unset", candidates[0].candidate.InstructionsPath)
	}
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"recovery", "list"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("list exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "RECOVERY "+id[1]+" applied ") || !strings.Contains(output, filepath.Join(skills, "release-notes")) {
		t.Fatalf("list output = %q, want applied recovery %s", output, id[1])
	}
}

func TestRecoveryShowInspectsAppliedSyncOperation(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"recovery", "show", id[1]}, &stdout, &stderr), 0; got != want {
		t.Fatalf("show exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	output := stdout.String()
	target := filepath.Join(skills, "release-notes")
	if !strings.Contains(output, "RECOVERY "+id[1]) || !strings.Contains(output, "state=applied") || !strings.Contains(output, "target="+target) || !strings.Contains(output, "replacement_fingerprint=sha256-") {
		t.Fatalf("show output = %q, want applied recovery details for %s", output, id[1])
	}
}

func TestRecoveryRestoreReinstatesPreimage(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"recovery", "restore", id[1]}, &stdout, &stderr), 0; got != want {
		t.Fatalf("restore exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "RESTORED "+id[1]) || !strings.Contains(output, "recovery=") {
		t.Fatalf("restore output = %q, want restored operation with new recovery ID", output)
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("projected Skill still present after restore, stat error = %v", err)
	}
}

func TestRecoveryRestoreDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1], "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "PLANNED") || !strings.Contains(output, id[1]) {
		t.Fatalf("dry-run output = %q, want planned restore", output)
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Canonical version\n" {
		t.Fatalf("target after dry-run = %q (err=%v), want unchanged projection", contents, err)
	}
}

func TestRecoveryRestoreBlocksWhenExpectDoesNotMatch(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1], "--expect", "sha256-deadbeef"}, &stdout, &stderr); got != 3 {
		t.Fatalf("restore exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "--expect") {
		t.Fatalf("blocked output = %q, want expect mismatch", output)
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Canonical version\n" {
		t.Fatalf("target after blocked restore = %q (err=%v)", contents, err)
	}
}

func TestHarnessStatusAfterRestoreDoesNotReportStaleProjection(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	var syncOut, syncErr bytes.Buffer
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}
	var restoreOut, restoreErr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1]}, &restoreOut, &restoreErr); got != 0 {
		t.Fatalf("restore exit code = %d; stderr = %s", got, restoreErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "LINKED codex-personal") {
		t.Fatalf("status output = %q, want linked Harness", output)
	}
	if strings.Contains(output, "DRIFT") || strings.Contains(output, "PROJECTION") || strings.Contains(output, "UNOWNED") {
		t.Fatalf("status output = %q, want no stale Projection, Drift, or Unowned path", output)
	}
}

func TestHarnessStatusAfterRestoreReportsUnownedPath(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	local := filepath.Join(skills, "release-notes")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte("# Local version\n"), 0o644); err != nil {
		t.Fatalf("write local Skill: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var blockedOut, blockedErr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &blockedOut, &blockedErr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, blockedErr.String())
	}
	fingerprint := regexp.MustCompile(`--expect (sha256-[0-9a-f]+)`).FindStringSubmatch(blockedErr.String())
	if len(fingerprint) != 2 {
		t.Fatalf("blocked output = %q, could not extract expected fingerprint", blockedErr.String())
	}
	var syncOut, syncErr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--force", "--expect", fingerprint[1]}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("force sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	id := regexp.MustCompile(`recovery=([0-9]+-[0-9a-f]{32})`).FindStringSubmatch(syncOut.String())
	if len(id) != 2 {
		t.Fatalf("sync output = %q, could not extract recovery ID", syncOut.String())
	}
	var restoreOut, restoreErr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1]}, &restoreOut, &restoreErr); got != 0 {
		t.Fatalf("restore exit code = %d; stderr = %s", got, restoreErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "UNOWNED "+local) {
		t.Fatalf("status output = %q, want Unowned path after restore", output)
	}
	if strings.Contains(output, "DRIFT") || strings.Contains(output, "PROJECTION") {
		t.Fatalf("status output = %q, want Unowned path without Drift or Projection", output)
	}
}

func TestCLIHelp(t *testing.T) {
	t.Parallel()

	output := runCLI(t, "--help")
	if !strings.Contains(output, "Brigsby") {
		t.Fatalf("help output = %q, want Brigsby", output)
	}
}

func TestRootAddAliasCapturesAnArtifact(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got := run([]string{"add", source}, &stdout, &stderr); got != 0 {
		t.Fatalf("root add exit code = %d; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CAPTURED main/skills/release-notes sha256-") {
		t.Fatalf("root add output = %q, want captured Artifact", stdout.String())
	}
}

func TestRootAddAliasUsesTheCanonicalMachineCommand(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got := run([]string{"add", source, "--json", "all"}, &stdout, &stderr); got != 0 {
		t.Fatalf("root add exit code = %d; stderr = %s", got, stderr.String())
	}
	var result struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse root add JSON = %v; output = %s", err, stdout.String())
	}
	if result.Command != "artifact add" {
		t.Fatalf("root add command = %q, want canonical artifact add", result.Command)
	}
}

func TestRootStatusAndSyncAliasesPreserveHarnessBehavior(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Harness fixture: %v", err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"add", source}} {
		var setupOut, setupErr bytes.Buffer
		if got := run(arguments, &setupOut, &setupErr); got != 0 {
			t.Fatalf("setup %v exit code = %d; stderr = %s", arguments, got, setupErr.String())
		}
	}

	var statusOut, statusErr bytes.Buffer
	if got := run([]string{"status", "--harness", "codex-personal"}, &statusOut, &statusErr); got != 0 {
		t.Fatalf("root status exit code = %d; stderr = %s", got, statusErr.String())
	}
	if !strings.Contains(statusOut.String(), "LINKED codex-personal") {
		t.Fatalf("root status output = %q, want linked Harness", statusOut.String())
	}

	var syncOut, syncErr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--dry-run"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("root sync dry-run exit code = %d, want 0; stderr = %s", got, syncErr.String())
	}
	if !strings.Contains(syncOut.String(), "PLANNED main/skills/release-notes") {
		t.Fatalf("root sync output = %q, want planned projection", syncOut.String())
	}
}

func TestCLIJSONEnvelopeSupportsSuccessDryRunBlockedResultAndJQ(t *testing.T) {
	t.Run("success and jq", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"version", "--json", "all", "--jq", ".state"}, &stdout, &stderr); got != 0 {
			t.Fatalf("version exit code = %d; stderr = %s", got, stderr.String())
		}
		if got, want := strings.TrimSpace(stdout.String()), "\"clean\""; got != want {
			t.Fatalf("jq result = %q, want %q", got, want)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"namespace", "set-prefix", "main", "mw-", "--dry-run", "--json", "all"}, &stdout, &stderr); got != 0 {
			t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
		}
		var result struct {
			Command  string `json:"command"`
			State    string `json:"state"`
			Problems []any  `json:"problems"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("parse dry-run JSON = %v; output = %s", err, stdout.String())
		}
		if result.Command != "namespace set-prefix" || result.State != "planned" || len(result.Problems) != 0 {
			t.Fatalf("dry-run JSON = %#v", result)
		}
	})

	t.Run("blocked result", func(t *testing.T) {
		home := t.TempDir()
		skills := filepath.Join(home, ".agents", "skills", "release-notes")
		source := filepath.Join(home, "release-notes")
		for path, contents := range map[string]string{
			filepath.Join(skills, "SKILL.md"): "# Local copy\n",
			filepath.Join(source, "SKILL.md"): "# Canonical copy\n",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("create fixture directory: %v", err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
		}
		t.Setenv("HOME", home)
		t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
		for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
			var setupOut, setupErr bytes.Buffer
			if got := run(arguments, &setupOut, &setupErr); got != 0 {
				t.Fatalf("setup %v exit code = %d; stderr = %s", arguments, got, setupErr.String())
			}
		}
		var stdout, stderr bytes.Buffer
		if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--json", "all"}, &stdout, &stderr); got != 3 {
			t.Fatalf("blocked exit code = %d, want 3; stderr = %s", got, stderr.String())
		}
		var result struct {
			State    string `json:"state"`
			Problems []struct {
				Message string `json:"message"`
			} `json:"problems"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("parse blocked JSON = %v; output = %s", err, stdout.String())
		}
		if result.State != "blocked" || len(result.Problems) != 1 || !strings.Contains(result.Problems[0].Message, "Unowned path") {
			t.Fatalf("blocked JSON = %#v", result)
		}
	})
}

func TestCLIRejectsJQWithoutJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"version", "--jq", ".state"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "--jq requires --json") {
		t.Fatalf("error output = %q, want explicit JSON requirement", stderr.String())
	}
}

func TestCLIReportsInvalidJSONFilterAsMachineProblem(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"version", "--json", "all", "--jq", "["}, &stdout, &stderr); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	var result struct {
		State    string `json:"state"`
		Problems []struct {
			Code string `json:"code"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse JSON = %v; output = %s", err, stdout.String())
	}
	if result.State != "invalid" || len(result.Problems) != 1 || result.Problems[0].Code != "invalid_request" {
		t.Fatalf("JSON result = %#v, want invalid state", result)
	}
}

func TestHarnessDiscoverFindsCodexUserSkillsInTemporaryHome(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "discover", "--harness", "codex"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "codex") || !strings.Contains(output, skills) {
		t.Fatalf("discovery output = %q, want Codex fixture path", output)
	}
}

func TestHarnessStatusFiltersByLinkedHarnessID(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var linkOut, linkErr bytes.Buffer
	if got := run([]string{"harness", "link", "codex-personal"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "status", "--harness", "codex-personal"}, &stdout, &stderr); got != 0 {
		t.Fatalf("status exit code = %d; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "LINKED codex-personal") {
		t.Fatalf("status output = %q, want filtered linked Harness", output)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "status", "--harness", "missing"}, &stdout, &stderr); got != 2 {
		t.Fatalf("missing Harness exit code = %d, want 2; stderr = %s", got, stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "missing") {
		t.Fatalf("missing Harness output = %q, want unknown linked Harness", output)
	}
}

func TestHarnessUnlinkRemovesAssociationAndKeepsHarnessFiles(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}

	var unlinkOut, unlinkErr bytes.Buffer
	if got, want := run([]string{"harness", "unlink", "codex-personal"}, &unlinkOut, &unlinkErr), 0; got != want {
		t.Fatalf("unlink exit code = %d, want %d; stderr = %s", got, want, unlinkErr.String())
	}
	if output := unlinkOut.String(); !strings.Contains(output, "UNLINKED codex-personal") {
		t.Fatalf("unlink output = %q, want UNLINKED", output)
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Release notes\n" {
		t.Fatalf("Harness files after unlink = %q (err=%v), want unchanged Skill", contents, err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	if output := stdout.String(); strings.Contains(output, "LINKED") || strings.Contains(output, "PROJECTION") || strings.Contains(output, "DRIFT") {
		t.Fatalf("status output = %q, want no linked Harness or Projection claim", output)
	}
}

func TestHarnessUnlinkDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var linkOut, linkErr bytes.Buffer
	if got := run([]string{"harness", "link", "codex-personal"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "unlink", "codex-personal", "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "PLANNED unlink codex-personal") {
		t.Fatalf("dry-run output = %q, want planned unlink", output)
	}

	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "status"}, &stdout, &stderr); got != 0 {
		t.Fatalf("status exit code = %d; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "LINKED codex-personal") {
		t.Fatalf("status after dry-run = %q, want still linked", output)
	}
}

func TestHarnessUnlinkRejectsUnknownID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "unlink", "missing"}, &stdout, &stderr); got != 2 {
		t.Fatalf("unlink exit code = %d, want 2; stderr = %s", got, stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "missing") {
		t.Fatalf("unlink output = %q, want unknown linked Harness", output)
	}
}

func TestHarnessLinkAndStatusPersistCodexFixture(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var linkOut, linkErr bytes.Buffer
	if got, want := run([]string{"harness", "link", "codex-personal"}, &linkOut, &linkErr), 0; got != want {
		t.Fatalf("link exit code = %d, want %d; stderr = %s", got, want, linkErr.String())
	}
	var statusOut, statusErr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &statusOut, &statusErr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, statusErr.String())
	}
	if output := statusOut.String(); !strings.Contains(output, "codex-personal") || !strings.Contains(output, skills) {
		t.Fatalf("status output = %q, want linked Codex fixture", output)
	}
}

func TestHarnessDiscoverLinkAndSyncClaudeFixture(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Claude fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var discoverOut, discoverErr bytes.Buffer
	if got := run([]string{"harness", "discover", "--harness", "claude"}, &discoverOut, &discoverErr); got != 0 {
		t.Fatalf("discover exit code = %d; stderr = %s", got, discoverErr.String())
	}
	if !strings.Contains(discoverOut.String(), "claude-personal") || !strings.Contains(discoverOut.String(), skills) {
		t.Fatalf("discover output = %q, want Claude fixture", discoverOut.String())
	}
	for _, arguments := range [][]string{{"harness", "link", "claude-personal"}, {"artifact", "add", source}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes", "SKILL.md")); err != nil {
		t.Fatalf("read projected Claude Skill: %v", err)
	}
}

func TestHarnessDiscoverLinkAndSyncOpenCodeFixture(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "xdg")
	skills := filepath.Join(configHome, "opencode", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create OpenCode fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var discoverOut, discoverErr bytes.Buffer
	if got := run([]string{"harness", "discover", "--harness", "opencode"}, &discoverOut, &discoverErr); got != 0 {
		t.Fatalf("discover exit code = %d; stderr = %s", got, discoverErr.String())
	}
	if !strings.Contains(discoverOut.String(), "opencode-personal") || !strings.Contains(discoverOut.String(), skills) {
		t.Fatalf("discover output = %q, want OpenCode fixture", discoverOut.String())
	}
	for _, arguments := range [][]string{{"harness", "link", "opencode-personal"}, {"artifact", "add", source}, {"harness", "sync", "--harness", "opencode-personal", "--artifact", "main/skills/release-notes"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes", "SKILL.md")); err != nil {
		t.Fatalf("read projected OpenCode Skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("OpenCode wrote a compatibility target: %v", err)
	}
}

func TestHarnessSyncProjectsStructuredGlobalInstructionsToCodex(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755); err != nil {
		t.Fatalf("create Codex Skills fixture: %v", err)
	}
	writeInstructionLocationMarker(t, filepath.Join(home, ".codex"))
	source := filepath.Join(home, "personal-instructions")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatalf("create Instruction source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("# Personal guidance\n"), 0o644); err != nil {
		t.Fatalf("write Instruction index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "docs", "core.md"), []byte("Use focused tests.\n"), 0o644); err != nil {
		t.Fatalf("write Instruction document: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "instructions.toml"), []byte("schema_version = 1\nindex = \"AGENTS.md\"\n\n[[document]]\nid = \"core\"\npath = \"docs/core.md\"\n"), 0o644); err != nil {
		t.Fatalf("write Instruction manifest: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", "--kind", "instructions", source}, {"harness", "sync", "--harness", "codex-personal", "--artifact", "main/instructions/personal-instructions"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	root, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read rendered Codex Instruction index: %v", err)
	}
	if !strings.Contains(string(root), "# Personal guidance") || !strings.Contains(string(root), "brigsby/personal-instructions/docs/core.md") {
		t.Fatalf("rendered Codex Instruction index = %q", root)
	}
	document, err := os.ReadFile(filepath.Join(home, ".codex", "brigsby", "personal-instructions", "docs", "core.md"))
	if err != nil || string(document) != "Use focused tests.\n" {
		t.Fatalf("rendered Codex Instruction doc = %q (err=%v)", document, err)
	}
}

func TestPackageCreateAndInspectSelectedSkill(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "references", "usage.md"), []byte("Use release notes.\n"), 0o644); err != nil {
		t.Fatalf("write Skill reference: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var stdout, stderr bytes.Buffer
	if got := run([]string{"artifact", "add", source}, &stdout, &stderr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, stderr.String())
	}
	for _, arguments := range [][]string{{"package", "create", "main/skills/release-notes", "--output", filepath.Join(home, "release-notes.tar.gz")}, {"package", "inspect", filepath.Join(home, "release-notes.tar.gz")}} {
		stdout.Reset()
		stderr.Reset()
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "PACKAGE") {
			t.Fatalf("%v output = %q, want package result", arguments, stdout.String())
		}
	}
}

func TestPackageImportStoresSkillInIsolatedNamespaceWithoutHarnessSync(t *testing.T) {
	sender := t.TempDir()
	source := filepath.Join(sender, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	archive := filepath.Join(sender, "release-notes.tar.gz")
	t.Setenv("BRIGSBY_HOME", filepath.Join(sender, ".brigsby"))
	for _, arguments := range [][]string{{"artifact", "add", source}, {"package", "create", "main/skills/release-notes", "--output", archive}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit=%d stderr=%s", arguments, got, stderr.String())
		}
	}
	recipient := t.TempDir()
	t.Setenv("BRIGSBY_HOME", filepath.Join(recipient, ".brigsby"))
	var stdout, stderr bytes.Buffer
	if got := run([]string{"package", "import", archive, "--namespace", "friend"}, &stdout, &stderr); got != 0 {
		t.Fatalf("import exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "IMPORTED friend/skills/release-notes") {
		t.Fatalf("import output=%q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(recipient, ".brigsby", "artifacts", "main", "skills", "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("import changed main: %v", err)
	}
	digest := regexp.MustCompile(`sha256-[0-9a-f]{64}`).FindString(stdout.String())
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "promote", "friend/skills/release-notes", "--revision", digest}, &stdout, &stderr); got != 0 {
		t.Fatalf("promote exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PROMOTED main/skills/release-notes") || !strings.Contains(stdout.String(), "origin=friend/skills/release-notes@") {
		t.Fatalf("promote output=%q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "show", "main/skills/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("show exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "origin=friend/skills/release-notes@"+digest) {
		t.Fatalf("show output=%q", stdout.String())
	}
}

func TestPackageImportPreflightLeavesNoPartialArtifactsOnLaterCollision(t *testing.T) {
	sender := t.TempDir()
	for name, contents := range map[string]string{"alpha": "# Alpha\n", "beta": "# Sender beta\n"} {
		source := filepath.Join(sender, name)
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	archive := filepath.Join(sender, "two-skills.tar.gz")
	t.Setenv("BRIGSBY_HOME", filepath.Join(sender, ".brigsby"))
	for _, arguments := range [][]string{{"artifact", "add", filepath.Join(sender, "alpha")}, {"artifact", "add", filepath.Join(sender, "beta")}, {"package", "create", "main/skills/alpha", "main/skills/beta", "--output", archive}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit=%d stderr=%s", arguments, got, stderr.String())
		}
	}
	recipient := t.TempDir()
	t.Setenv("BRIGSBY_HOME", filepath.Join(recipient, ".brigsby"))
	conflicting := filepath.Join(recipient, "beta")
	if err := os.MkdirAll(conflicting, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflicting, "SKILL.md"), []byte("# Recipient beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"artifact", "add", conflicting, "--namespace", "friend"}, &stdout, &stderr); got != 0 {
		t.Fatalf("capture collision fixture exit=%d stderr=%s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"package", "import", archive, "--namespace", "friend"}, &stdout, &stderr); got != 3 {
		t.Fatalf("import exit=%d stderr=%s", got, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(recipient, ".brigsby", "artifacts", "friend", "skills", "alpha")); !os.IsNotExist(err) {
		t.Fatalf("import left partial alpha artifact: %v", err)
	}
}

func TestHarnessSyncSelectsOnlyClaudeInstructionException(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatalf("create Claude Skills fixture: %v", err)
	}
	writeInstructionLocationMarker(t, filepath.Join(home, ".claude"))
	source := filepath.Join(home, "personal-instructions")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatalf("create Instruction source: %v", err)
	}
	files := map[string]string{
		"AGENTS.md":               "# Personal guidance\n",
		"docs/core.md":            "Shared guidance.\n",
		"docs/claude-specific.md": "Claude guidance.\n",
		"instructions.toml":       "schema_version = 1\nindex = \"AGENTS.md\"\n\n[[document]]\nid = \"core\"\npath = \"docs/core.md\"\n\n[[document]]\nid = \"claude-specific\"\npath = \"docs/claude-specific.md\"\nharness = \"claude\"\nsupersedes = [\"core\"]\n",
	}
	for relative, contents := range files {
		if err := os.WriteFile(filepath.Join(source, relative), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "claude-personal"}, {"artifact", "add", "--kind", "instructions", source}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/instructions/personal-instructions"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	root, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || !strings.Contains(string(root), "brigsby/personal-instructions/docs/claude-specific.md") || strings.Contains(string(root), "docs/core.md") {
		t.Fatalf("rendered Claude Instruction index = %q (err=%v)", root, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "brigsby", "personal-instructions", "docs", "core.md")); !os.IsNotExist(err) {
		t.Fatalf("superseded shared Instruction document was projected: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(home, ".claude", "brigsby", "personal-instructions", "docs", "claude-specific.md"))
	if err != nil || string(contents) != "Claude guidance.\n" {
		t.Fatalf("Claude exception = %q (err=%v)", contents, err)
	}
}

func TestHarnessSyncProjectsStructuredGlobalInstructionsToOpenCode(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "xdg")
	if err := os.MkdirAll(filepath.Join(configHome, "opencode", "skills"), 0o755); err != nil {
		t.Fatalf("create OpenCode Skills fixture: %v", err)
	}
	writeInstructionLocationMarker(t, filepath.Join(configHome, "opencode"))
	source := filepath.Join(home, "personal-instructions")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatalf("create Instruction source: %v", err)
	}
	for relative, contents := range map[string]string{"AGENTS.md": "# Personal guidance\n", "docs/core.md": "Use focused tests.\n", "instructions.toml": "schema_version = 1\nindex = \"AGENTS.md\"\n\n[[document]]\nid = \"core\"\npath = \"docs/core.md\"\n"} {
		if err := os.WriteFile(filepath.Join(source, relative), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "opencode-personal"}, {"artifact", "add", "--kind", "instructions", source}, {"harness", "sync", "--harness", "opencode-personal", "--artifact", "main/instructions/personal-instructions"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(configHome, "opencode", "AGENTS.md")); err != nil {
		t.Fatalf("read rendered OpenCode Instruction index: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("OpenCode wrote a Claude compatibility instruction: %v", err)
	}
}

func TestHarnessSyncBlocksDriftedGlobalInstructionProjection(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755); err != nil {
		t.Fatalf("create Codex Skills fixture: %v", err)
	}
	writeInstructionLocationMarker(t, filepath.Join(home, ".codex"))
	source := filepath.Join(home, "personal-instructions")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatalf("create Instruction source: %v", err)
	}
	for relative, contents := range map[string]string{"AGENTS.md": "# Personal guidance\n", "docs/core.md": "Use focused tests.\n", "instructions.toml": "schema_version = 1\nindex = \"AGENTS.md\"\n\n[[document]]\nid = \"core\"\npath = \"docs/core.md\"\n"} {
		if err := os.WriteFile(filepath.Join(source, relative), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", "--kind", "instructions", source}, {"harness", "sync", "--harness", "codex-personal", "--artifact", "main/instructions/personal-instructions"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatalf("edit projected Instruction root: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/instructions/personal-instructions"}, &stdout, &stderr); got != 3 || !strings.Contains(stderr.String(), "BLOCKED: Instruction Projection") {
		t.Fatalf("drifted sync exit=%d stderr=%q", got, stderr.String())
	}
	expect := strings.SplitN(strings.Split(stderr.String(), "--expect ")[1], "\n", 2)[0]
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/instructions/personal-instructions", "--force", "--expect", expect}, &stdout, &stderr); got != 0 {
		t.Fatalf("force sync exit=%d stderr=%q", got, stderr.String())
	}
}

func TestNamespacePrefixRendersOpenCodeSkillAtNativePath(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "xdg")
	skills := filepath.Join(configHome, "opencode", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create OpenCode fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: release-notes\ndescription: Release notes workflow\n---\n# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "opencode-personal"}, {"artifact", "add", source}, {"namespace", "set-prefix", "main", "mw-"}, {"harness", "sync", "--harness", "opencode-personal", "--artifact", "main/skills/release-notes"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	contents, err := os.ReadFile(filepath.Join(skills, "mw-release-notes", "SKILL.md"))
	if err != nil {
		t.Fatalf("read prefixed OpenCode Skill: %v", err)
	}
	if !strings.Contains(string(contents), "name: mw-release-notes") {
		t.Fatalf("prefixed OpenCode Skill = %q, want rendered frontmatter", contents)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "mw-release-notes")); !os.IsNotExist(err) {
		t.Fatalf("OpenCode wrote a compatibility target: %v", err)
	}
}

func TestNamespacePrefixRendersClaudeSkillDirectoryAndFrontmatter(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Claude fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: release-notes\ndescription: Release notes workflow\n---\n# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "claude-personal"}, {"artifact", "add", source}, {"namespace", "set-prefix", "main", "mw-"}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	contents, err := os.ReadFile(filepath.Join(skills, "mw-release-notes", "SKILL.md"))
	if err != nil {
		t.Fatalf("read prefixed projection: %v", err)
	}
	if !strings.Contains(string(contents), "name: mw-release-notes") {
		t.Fatalf("prefixed projection = %q, want rendered frontmatter", contents)
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("unprefixed projection exists: %v", err)
	}
}

func TestNamespacePrefixMigratesExistingClaudeProjection(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Claude fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: release-notes\ndescription: Release notes workflow\n---\n# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "claude-personal"}, {"artifact", "add", source}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}, {"namespace", "set-prefix", "main", "mw-"}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(skills, "mw-release-notes", "SKILL.md")); err != nil {
		t.Fatalf("read migrated projection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("previous projection remains after migration: %v", err)
	}
}

func TestNamespacePrefixBlocksMigrationWhenPreviousClaudeProjectionDrifted(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Claude fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: release-notes\ndescription: Release notes workflow\n---\n# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "claude-personal"}, {"artifact", "add", source}, {"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}, {"namespace", "set-prefix", "main", "mw-"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("drift previous projection: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "claude-personal", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "previous Projection") || !strings.Contains(stderr.String(), "drifted") {
		t.Fatalf("sync error = %q, want drift blocker", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(skills, "mw-release-notes")); !os.IsNotExist(err) {
		t.Fatalf("new target changed despite drifted migration: %v", err)
	}
}

func TestArtifactAddCapturesSkillAndShowReportsItsSelectedRevision(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var addOut, addErr bytes.Buffer
	if got, want := run([]string{"artifact", "add", source}, &addOut, &addErr), 0; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, addErr.String())
	}
	if !strings.Contains(addOut.String(), "main/skills/release-notes") || !strings.Contains(addOut.String(), "sha256-") {
		t.Fatalf("add output = %q, want selector and revision digest", addOut.String())
	}

	var showOut, showErr bytes.Buffer
	if got, want := run([]string{"artifact", "show", "main/skills/release-notes"}, &showOut, &showErr), 0; got != want {
		t.Fatalf("show exit code = %d, want %d; stderr = %s", got, want, showErr.String())
	}
	if !strings.Contains(showOut.String(), "main/skills/release-notes") || !strings.Contains(showOut.String(), "sha256-") {
		t.Fatalf("show output = %q, want selector and revision digest", showOut.String())
	}
}

func TestArtifactListReportsCapturedSkills(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var addOut, addErr bytes.Buffer
	if got := run([]string{"artifact", "add", source}, &addOut, &addErr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, addErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"artifact", "list"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("list exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "ARTIFACT main/skills/release-notes sha256-") {
		t.Fatalf("list output = %q, want captured Skill", output)
	}
}

func TestArtifactListFiltersByNamespaceAndKind(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var addOut, addErr bytes.Buffer
	if got := run([]string{"artifact", "add", source}, &addOut, &addErr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, addErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"artifact", "list", "--kind", "instructions"}, &stdout, &stderr); got != 0 {
		t.Fatalf("instructions list exit code = %d; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); output != "" {
		t.Fatalf("instructions list = %q, want empty", output)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "list", "--kind", "skills", "--namespace", "main"}, &stdout, &stderr); got != 0 {
		t.Fatalf("skills list exit code = %d; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "ARTIFACT main/skills/release-notes sha256-") {
		t.Fatalf("skills list = %q, want captured Skill", output)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "list", "--namespace", "imported"}, &stdout, &stderr); got != 0 {
		t.Fatalf("imported list exit code = %d; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); output != "" {
		t.Fatalf("imported list = %q, want empty", output)
	}
}

func TestArtifactSelectSwitchesSelectedRevisionWithoutRecopying(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# First revision\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var firstOut, firstErr bytes.Buffer
	if got := run([]string{"artifact", "add", source}, &firstOut, &firstErr); got != 0 {
		t.Fatalf("first add exit code = %d; stderr = %s", got, firstErr.String())
	}
	first := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(firstOut.String())
	if first == "" {
		t.Fatalf("first add output = %q, could not extract digest", firstOut.String())
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("update Skill fixture: %v", err)
	}
	var secondOut, secondErr bytes.Buffer
	if got := run([]string{"artifact", "add", source, "--name", "release-notes"}, &secondOut, &secondErr); got != 0 {
		t.Fatalf("second add exit code = %d; stderr = %s", got, secondErr.String())
	}
	second := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(secondOut.String())
	if second == "" || second == first {
		t.Fatalf("second add output = %q, want a different digest than %s", secondOut.String(), first)
	}

	var selectOut, selectErr bytes.Buffer
	if got, want := run([]string{"artifact", "select", "main/skills/release-notes", "--revision", first}, &selectOut, &selectErr), 0; got != want {
		t.Fatalf("select exit code = %d, want %d; stderr = %s", got, want, selectErr.String())
	}
	if output := selectOut.String(); !strings.Contains(output, "SELECTED main/skills/release-notes "+first) {
		t.Fatalf("select output = %q, want first revision selected", output)
	}
	var showOut, showErr bytes.Buffer
	if got := run([]string{"artifact", "show", "main/skills/release-notes"}, &showOut, &showErr); got != 0 {
		t.Fatalf("show exit code = %d; stderr = %s", got, showErr.String())
	}
	if output := showOut.String(); !strings.Contains(output, first) || strings.Contains(output, second) {
		t.Fatalf("show output = %q, want first revision %s", output, first)
	}
}

func TestArtifactSelectDryRunDoesNotChangeSelection(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# First revision\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var firstOut, firstErr bytes.Buffer
	if got := run([]string{"artifact", "add", source}, &firstOut, &firstErr); got != 0 {
		t.Fatalf("first add exit code = %d; stderr = %s", got, firstErr.String())
	}
	first := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(firstOut.String())
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("update Skill fixture: %v", err)
	}
	var secondOut, secondErr bytes.Buffer
	if got := run([]string{"artifact", "add", source, "--name", "release-notes"}, &secondOut, &secondErr); got != 0 {
		t.Fatalf("second add exit code = %d; stderr = %s", got, secondErr.String())
	}
	second := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(secondOut.String())

	var stdout, stderr bytes.Buffer
	if got := run([]string{"artifact", "select", "main/skills/release-notes", "--revision", first, "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "PLANNED select main/skills/release-notes "+first) {
		t.Fatalf("dry-run output = %q, want planned select", output)
	}
	var showOut, showErr bytes.Buffer
	if got := run([]string{"artifact", "show", "main/skills/release-notes"}, &showOut, &showErr); got != 0 {
		t.Fatalf("show exit code = %d; stderr = %s", got, showErr.String())
	}
	if output := showOut.String(); !strings.Contains(output, second) {
		t.Fatalf("show after dry-run = %q, want still %s", output, second)
	}
}

func TestHarnessStatusReportsCleanProjectionAfterSync(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "LINKED codex-personal") || !strings.Contains(output, "PROJECTION main/skills/release-notes") {
		t.Fatalf("status output = %q, want linked Harness and clean Projection", output)
	}
	if strings.Contains(output, "DRIFT") || strings.Contains(output, "UNOWNED") {
		t.Fatalf("status output = %q, want no Drift or Unowned path", output)
	}
}

func TestHarnessStatusReportsDriftAfterProjectedSkillIsEdited(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Edited locally\n"), 0o644); err != nil {
		t.Fatalf("edit projected Skill: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "DRIFT main/skills/release-notes") {
		t.Fatalf("status output = %q, want Drift for the projected Skill", output)
	}
	if strings.Contains(output, "PROJECTION main/skills/release-notes") || strings.Contains(output, "UNOWNED") {
		t.Fatalf("status output = %q, want Drift without a clean Projection", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("status stderr = %q, want empty", stderr.String())
	}
}

func TestHarnessStatusReportsUnownedLocalSkill(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills", "release-notes")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skills, "SKILL.md"), []byte("# Local skill\n"), 0o644); err != nil {
		t.Fatalf("write local Skill: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var linkOut, linkErr bytes.Buffer
	if got := run([]string{"harness", "link", "codex-personal"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "UNOWNED "+skills) {
		t.Fatalf("status output = %q, want Unowned path %s", output, skills)
	}
	if output := stdout.String(); strings.Contains(output, "PROJECTION") || strings.Contains(output, "DRIFT") {
		t.Fatalf("status output = %q, want Unowned path without Projection or Drift", output)
	}
}

func TestHarnessStatusReportsDriftWhenSelectedRevisionChanges(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# First revision\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("update Skill source: %v", err)
	}
	var addOut, addErr bytes.Buffer
	if got := run([]string{"artifact", "add", source, "--name", "release-notes"}, &addOut, &addErr); got != 0 {
		t.Fatalf("recapture exit code = %d; stderr = %s", got, addErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "DRIFT main/skills/release-notes") {
		t.Fatalf("status output = %q, want Drift after selected Revision changed", output)
	}
}

func TestHarnessStatusJSONReportsDrift(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Edited locally\n"), 0o644); err != nil {
		t.Fatalf("edit projected Skill: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--json", "all"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	var result struct {
		Command  string `json:"command"`
		State    string `json:"state"`
		Problems []struct {
			Code string `json:"code"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse JSON = %v; output = %s", err, stdout.String())
	}
	if result.Command != "harness status" || result.State != "drifted" {
		t.Fatalf("JSON result = %#v", result)
	}
	if len(result.Problems) != 1 || result.Problems[0].Code != "projection_drift" {
		t.Fatalf("JSON problems = %#v, want projection_drift", result.Problems)
	}
}

func TestHarnessSyncPreflightBlocksEverySelectedHarnessWhenOneHasCollision(t *testing.T) {
	home := t.TempDir()
	personalSkills := filepath.Join(home, ".agents", "skills")
	workSkills := filepath.Join(home, "work", "skills")
	if err := os.MkdirAll(filepath.Join(personalSkills, "release-notes"), 0o755); err != nil {
		t.Fatalf("create conflicting personal Skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personalSkills, "release-notes", "SKILL.md"), []byte("# Local version\n"), 0o644); err != nil {
		t.Fatalf("write conflicting personal Skill: %v", err)
	}
	if err := os.MkdirAll(workSkills, 0o755); err != nil {
		t.Fatalf("create clean work fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".brigsby")
	t.Setenv("BRIGSBY_HOME", root)

	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := harness.NewRegistry(root).Link(harness.Candidate{ID: "codex-work", Name: "codex", SkillsPath: workSkills}); err != nil {
		t.Fatalf("link work fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--harness", "codex-work", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 2; stderr = %s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "BLOCKED") {
		t.Fatalf("sync error = %q, want collision blocker", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workSkills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("clean target changed despite blocked batch: %v", err)
	}
}

func TestHarnessSyncProjectsSkillToEverySelectedHarness(t *testing.T) {
	home := t.TempDir()
	personalSkills := filepath.Join(home, ".agents", "skills")
	workSkills := filepath.Join(home, "work", "skills")
	for _, skills := range []string{personalSkills, workSkills} {
		if err := os.MkdirAll(skills, 0o755); err != nil {
			t.Fatalf("create Harness fixture: %v", err)
		}
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".brigsby")
	t.Setenv("BRIGSBY_HOME", root)
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := harness.NewRegistry(root).Link(harness.Candidate{ID: "codex-work", Name: "codex", SkillsPath: workSkills}); err != nil {
		t.Fatalf("link work fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--harness", "codex-work", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, stderr.String())
	}
	for _, skills := range []string{personalSkills, workSkills} {
		contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
		if err != nil || string(contents) != "# Canonical version\n" {
			t.Fatalf("projection at %s = %q (err=%v)", skills, contents, err)
		}
	}
}

func TestHarnessSyncProjectsSelectedSkillToLinkedCodexFixture(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var syncOut, syncErr bytes.Buffer
	if got, want := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &syncOut, &syncErr), 0; got != want {
		t.Fatalf("sync exit code = %d, want %d; stderr = %s", got, want, syncErr.String())
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil {
		t.Fatalf("read projected Skill: %v", err)
	}
	if got, want := string(contents), "# Release notes\n"; got != want {
		t.Fatalf("projected Skill = %q, want %q", got, want)
	}
	if !strings.Contains(syncOut.String(), "APPLIED") {
		t.Fatalf("sync output = %q, want applied state", syncOut.String())
	}
}

func TestHarnessSyncBlocksUnownedPathWithArtifactAddAndGuardedForce(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	local := filepath.Join(skills, "release-notes")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte("# Local version\n"), 0o644); err != nil {
		t.Fatalf("write local Skill: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "Unowned path") || !strings.Contains(output, "artifact add "+local) || !strings.Contains(output, "--force --expect sha256-") {
		t.Fatalf("blocked output = %q, want Unowned path with artifact add and guarded force", output)
	}
	if strings.Contains(output, "Usage:") {
		t.Fatalf("blocked output = %q, want the actionable blocker without command help", output)
	}
	if strings.Contains(output, "Drift") {
		t.Fatalf("blocked output = %q, want Unowned path rather than Drift", output)
	}
}

func TestHarnessSyncBlocksDriftWithArtifactAddAndGuardedForce(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex-personal"},
		{"artifact", "add", source},
		{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	projected := filepath.Join(skills, "release-notes")
	if err := os.WriteFile(filepath.Join(projected, "SKILL.md"), []byte("# Edited locally\n"), 0o644); err != nil {
		t.Fatalf("edit projected Skill: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "Drift") || !strings.Contains(output, "artifact add "+projected) || !strings.Contains(output, "--force --expect sha256-") {
		t.Fatalf("blocked output = %q, want Drift with artifact add and guarded force", output)
	}
	if strings.Contains(output, "Unowned path") {
		t.Fatalf("blocked output = %q, want Drift rather than Unowned path", output)
	}
}

func TestHarnessSyncBlocksChangedTargetWithAReadyToRunGuardedForce(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(filepath.Join(skills, "release-notes"), 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Local version\n"), 0o644); err != nil {
		t.Fatalf("write local Skill: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Canonical version\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "--force --expect sha256-") {
		t.Fatalf("blocked output = %q, want guarded force action", output)
	}
	fingerprint := regexp.MustCompile(`--expect (sha256-[0-9a-f]+)`).FindStringSubmatch(stderr.String())
	if len(fingerprint) != 2 {
		t.Fatalf("blocked output = %q, could not extract expected fingerprint", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--force", "--expect", fingerprint[1]}, &stdout, &stderr); got != 0 {
		t.Fatalf("guarded force exit code = %d; stderr = %s", got, stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Canonical version\n" {
		t.Fatalf("target after guarded force = %q (err=%v)", contents, err)
	}
}

func TestHarnessSyncReportsAProvisionalJSONPlan(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill source: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"artifact", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "sync", "--harness", "codex-personal", "--artifact", "main/skills/release-notes", "--dry-run", "--json", "all"}, &stdout, &stderr); got != 0 {
		t.Fatalf("sync exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	var result struct {
		Command string `json:"command"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse JSON = %v; output = %s", err, stdout.String())
	}
	if result.Command != "harness sync" || result.State != "planned" {
		t.Fatalf("JSON result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a projection, stat error = %v", err)
	}
}

func TestCLIVersion(t *testing.T) {
	t.Parallel()

	output := runCLI(t, "version")
	if got, want := strings.TrimSpace(output), "brigsby dev"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestPickVersion(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		ldflag    string
		biVersion string
		biOK      bool
		want      string
	}{
		{name: "ldflags injected wins", ldflag: "0.0.2", biVersion: "(devel)", biOK: true, want: "0.0.2"},
		{name: "ldflags injected wins over build info", ldflag: "0.0.2", biVersion: "v0.0.3", biOK: true, want: "0.0.2"},
		{name: "go install module version", ldflag: "dev", biVersion: "v0.0.2", biOK: true, want: "v0.0.2"},
		{name: "go install prerelease tag", ldflag: "dev", biVersion: "v1.2.0-rc.1", biOK: true, want: "v1.2.0-rc.1"},
		{name: "go install v2 incompatible tag", ldflag: "dev", biVersion: "v2.0.0+incompatible", biOK: true, want: "v2.0.0+incompatible"},
		{name: "checkout build info is devel", ldflag: "dev", biVersion: "(devel)", biOK: true, want: "dev"},
		{name: "checkout build pseudo-version no reachable tag", ldflag: "dev", biVersion: "v0.0.0-20260902105352-bfa9779124d0", biOK: true, want: "dev"},
		{name: "checkout build pseudo-version after a tag", ldflag: "dev", biVersion: "v0.0.2-0.20260902123935-2f7569f9e392", biOK: true, want: "dev"},
		{name: "checkout build pseudo-version after a prerelease tag", ldflag: "dev", biVersion: "v1.2.3-pre.0.20260902105352-abcdef123456", biOK: true, want: "dev"},
		{name: "checkout build dirty pseudo-version", ldflag: "dev", biVersion: "v0.0.0-20260902105352-bfa9779124d0+dirty", biOK: true, want: "dev"},
		{name: "checkout build dirty release tag", ldflag: "dev", biVersion: "v0.0.3+dirty", biOK: true, want: "dev"},
		{name: "empty build info", ldflag: "dev", biVersion: "", biOK: true, want: "dev"},
		{name: "no build info", ldflag: "dev", biVersion: "", biOK: false, want: "dev"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := pickVersion(testCase.ldflag, testCase.biVersion, testCase.biOK); got != testCase.want {
				t.Fatalf("pickVersion(%q, %q, %t) = %q, want %q", testCase.ldflag, testCase.biVersion, testCase.biOK, got, testCase.want)
			}
		})
	}
}

func TestCLIVersionCompiledBinary(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	for _, testCase := range []struct {
		name    string
		ldflags []string
		want    string
	}{
		{name: "injected", ldflags: []string{"-ldflags", "-X main.version=1.2.3"}, want: "brigsby 1.2.3"},
		{name: "checkout stays dev", ldflags: nil, want: "brigsby dev"},
	} {
		binary := filepath.Join(directory, "brigsby-"+testCase.name)
		build := exec.Command("go", append(append([]string{"build"}, testCase.ldflags...), "-o", binary, ".")...)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%s build: %v\n%s", testCase.name, err, output)
		}
		for _, arguments := range [][]string{{"version"}, {"--version"}} {
			output, err := exec.Command(binary, arguments...).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: brigsby %s: %v\n%s", testCase.name, strings.Join(arguments, " "), err, output)
			}
			if got := strings.TrimSpace(string(output)); got != testCase.want {
				t.Fatalf("%s: brigsby %s = %q, want %q", testCase.name, strings.Join(arguments, " "), got, testCase.want)
			}
		}
	}
}

func TestCLIUnknownCommandReportsActionableUsage(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"nonesuch"}, &stdout, &stderr), 2; got != want {
		t.Fatalf("exit code = %d, want %d", got, want)
	}
	if output := stderr.String(); !strings.Contains(output, "unknown command") || !strings.Contains(output, "Usage:") {
		t.Fatalf("error output = %q, want unknown-command usage", output)
	}
}

func TestProjectionFingerprintMatchesLegacyExactFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release-notes")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create projection: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# Release notes\n"), 0o600); err != nil {
		t.Fatalf("write projection: %v", err)
	}
	exact, err := recovery.Fingerprint(path)
	if err != nil {
		t.Fatalf("legacy exact fingerprint: %v", err)
	}
	matches, err := projectionFingerprintMatches(path, exact)
	if err != nil || !matches {
		t.Fatalf("legacy fingerprint match = %t, err=%v", matches, err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# Changed\n"), 0o600); err != nil {
		t.Fatalf("change projection: %v", err)
	}
	matches, err = projectionFingerprintMatches(path, exact)
	if err != nil || matches {
		t.Fatalf("changed legacy projection match = %t, err=%v", matches, err)
	}
}

func TestCLIUsabilityFixesForSafeObservationAndExistingSkills(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	skills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skills, "release-notes"), 0o755); err != nil {
		t.Fatalf("create existing Skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write existing Skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("create unstructured Harness home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	for _, arguments := range [][]string{{"harness", "link", "codex-personal"}, {"add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex-personal", "main/skills/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("content-identical existing Skill sync exit=%d stderr=%s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "show", "main/skills/release-notes", "--files"}, &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "FILE SKILL.md bytes=16\n# Release notes\n\n---\n") {
		t.Fatalf("show files exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Local edit\n"), 0o644); err != nil {
		t.Fatalf("edit existing Skill: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex-personal", "main/skills/release-notes"}, &stdout, &stderr); got != 3 || !strings.Contains(stderr.String(), "BLOCKED:") {
		t.Fatalf("changed-content sync exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"--json", "all", "harness", "discover"}, &stdout, &stderr); got != 0 {
		t.Fatalf("leading JSON discover exit=%d stderr=%s", got, stderr.String())
	}
	var result struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse discover JSON: %v; output=%s", err, stdout.String())
	}
	if result.Command != "harness discover" {
		t.Fatalf("leading JSON command=%q, want harness discover", result.Command)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"artifact", "add", "--kind", "instructions", filepath.Join(home, ".codex")}, &stdout, &stderr); got != 2 || !strings.Contains(stderr.String(), "structured Instruction set") {
		t.Fatalf("unstructured instruction source exit=%d stderr=%q", got, stderr.String())
	}
}

func TestCLIVersionRejectsArguments(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"version", "extra"}, &stdout, &stderr), 2; got != want {
		t.Fatalf("exit code = %d, want %d", got, want)
	}
	if output := stderr.String(); !strings.Contains(output, "unknown command") || !strings.Contains(output, "Usage:") {
		t.Fatalf("error output = %q, want command usage", output)
	}
}

func runCLI(t *testing.T, arguments ...string) string {
	t.Helper()

	command := exec.Command("go", append([]string{"run", "."}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("brigsby %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeInstructionLocationMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create structured Instruction location: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "instructions.toml"), []byte("# Brigsby-managed Instruction location\n"), 0o644); err != nil {
		t.Fatalf("mark structured Instruction location: %v", err)
	}
}
