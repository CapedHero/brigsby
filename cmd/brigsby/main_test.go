package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	candidates, err := discoverBuiltinCandidates()
	if err != nil {
		t.Fatalf("discover candidates: %v", err)
	}
	if candidates[0].candidate.InstructionsPath != "" {
		t.Fatalf("unstructured Codex home instruction path = %q, want unset", candidates[0].candidate.InstructionsPath)
	}
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	recoveryID := firstSyncRecoveryID(t, "sync", "--harness", "codex", "--skill", "main/release-notes")

	var list struct {
		Operations []struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Target string `json:"target"`
		} `json:"operations"`
	}
	mustCLI(t, "recovery", "list").into(t, &list)
	found := false
	for _, operation := range list.Operations {
		if operation.ID != recoveryID {
			continue
		}
		found = true
		if operation.State != "applied" {
			t.Fatalf("operation %s state = %q, want applied", operation.ID, operation.State)
		}
		if operation.Target != filepath.Join(skills, "release-notes") {
			t.Fatalf("operation target = %q, want projected Skill path", operation.Target)
		}
	}
	if !found {
		t.Fatalf("recovery list = %+v, want applied operation %s", list.Operations, recoveryID)
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	var syncPlan cliEnvelope
	if err := json.Unmarshal(syncOut.Bytes(), &syncPlan); err != nil {
		t.Fatalf("decode sync envelope: %v\n%s", err, syncOut.String())
	}
	syncIDs := syncRecoveryIDs(t, syncPlan)
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"recovery", "show", id[1]}, &stdout, &stderr), 0; got != want {
		t.Fatalf("show exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	var show struct {
		Operation struct {
			ID                     string `json:"id"`
			State                  string `json:"state"`
			Target                 string `json:"target"`
			ReplacementFingerprint string `json:"replacement_fingerprint"`
		} `json:"operation"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &show)
	target := filepath.Join(skills, "release-notes")
	if show.Operation.ID != id[1] || show.Operation.State != "applied" || show.Operation.Target != target || !strings.HasPrefix(show.Operation.ReplacementFingerprint, "sha256-") {
		t.Fatalf("recovery show operation = %+v, want applied details for %s", show.Operation, id[1])
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	var syncPlan cliEnvelope
	if err := json.Unmarshal(syncOut.Bytes(), &syncPlan); err != nil {
		t.Fatalf("decode sync envelope: %v\n%s", err, syncOut.String())
	}
	syncIDs := syncRecoveryIDs(t, syncPlan)
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"recovery", "restore", id[1]}, &stdout, &stderr), 0; got != want {
		t.Fatalf("restore exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	var restore struct {
		Restored struct {
			RecoveryID  string `json:"recovery_id"`
			OperationID string `json:"operation_id"`
		} `json:"restored"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &restore)
	if restore.Restored.RecoveryID != id[1] || restore.Restored.OperationID == "" {
		t.Fatalf("recovery restore = %+v, want restored %s with a new operation id", restore.Restored, id[1])
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	var syncPlan cliEnvelope
	if err := json.Unmarshal(syncOut.Bytes(), &syncPlan); err != nil {
		t.Fatalf("decode sync envelope: %v\n%s", err, syncOut.String())
	}
	syncIDs := syncRecoveryIDs(t, syncPlan)
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1], "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	dryRun := decodeEnvelope(t, stdout.Bytes())
	if dryRun.State != "planned" {
		t.Fatalf("dry-run state = %q, want planned", dryRun.State)
	}
	var dryRunResult struct {
		Restored struct {
			RecoveryID string `json:"recovery_id"`
		} `json:"restored"`
	}
	dryRun.into(t, &dryRunResult)
	if dryRunResult.Restored.RecoveryID != id[1] {
		t.Fatalf("dry-run restored = %+v, want %s", dryRunResult.Restored, id[1])
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	var syncPlan cliEnvelope
	if err := json.Unmarshal(syncOut.Bytes(), &syncPlan); err != nil {
		t.Fatalf("decode sync envelope: %v\n%s", err, syncOut.String())
	}
	syncIDs := syncRecoveryIDs(t, syncPlan)
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}

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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	var syncPlan cliEnvelope
	if err := json.Unmarshal(syncOut.Bytes(), &syncPlan); err != nil {
		t.Fatalf("decode sync envelope: %v\n%s", err, syncOut.String())
	}
	syncIDs := syncRecoveryIDs(t, syncPlan)
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}
	var restoreOut, restoreErr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1]}, &restoreOut, &restoreErr); got != 0 {
		t.Fatalf("restore exit code = %d; stderr = %s", got, restoreErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "clean" {
		t.Fatalf("status state = %q, want clean", env.State)
	}
	if !slices.Contains(status.linkedIDs(), "codex") {
		t.Fatalf("status linked = %+v, want codex", status.Linked)
	}
	if len(env.Problems) != 0 || len(status.Projections) != 0 {
		t.Fatalf("status problems=%+v projections=%+v, want none", env.Problems, status.Projections)
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var blockedOut, blockedErr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &blockedOut, &blockedErr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, blockedErr.String())
	}
	if !strings.Contains(blockedErr.String(), "rerun with --force") {
		t.Fatalf("blocked output = %q, want --force guidance", blockedErr.String())
	}
	var syncOut, syncErr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes", "--force"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("force sync exit code = %d; stderr = %s", got, syncErr.String())
	}
	syncIDs := syncRecoveryIDs(t, decodeEnvelope(t, syncOut.Bytes()))
	if len(syncIDs) == 0 {
		t.Fatalf("sync output = %q, no recovery_ids", syncOut.String())
	}
	id := []string{"", syncIDs[0]}
	var restoreOut, restoreErr bytes.Buffer
	if got := run([]string{"recovery", "restore", id[1]}, &restoreOut, &restoreErr); got != 0 {
		t.Fatalf("restore exit code = %d; stderr = %s", got, restoreErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--unowned"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "unowned" {
		t.Fatalf("status state = %q, want unowned", env.State)
	}
	if !strings.Contains(env.problemText(), "is not managed by Brigsby") {
		t.Fatalf("status problems = %s, want self-contained unowned-path problem", env.problemText())
	}
	if problem := env.Problems[0]; problem.Harness != "codex" || problem.Kind != "skill" || problem.Path != local {
		t.Fatalf("status problem = %+v, want structured unowned path details", problem)
	}
	if slices.Contains(env.problemCodes(), "projection_drift") || len(status.Projections) != 0 {
		t.Fatalf("status problems=%+v projections=%+v, want unowned only", env.Problems, status.Projections)
	}
}

func TestCLIHelp(t *testing.T) {
	t.Parallel()

	output := runCLI(t, "--help")
	if !strings.Contains(output, "Brigsby") {
		t.Fatalf("help output = %q, want Brigsby", output)
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var setupOut, setupErr bytes.Buffer
		if got := run(arguments, &setupOut, &setupErr); got != 0 {
			t.Fatalf("setup %v exit code = %d; stderr = %s", arguments, got, setupErr.String())
		}
	}

	var statusOut, statusErr bytes.Buffer
	if got := run([]string{"status", "--harness", "codex"}, &statusOut, &statusErr); got != 0 {
		t.Fatalf("root status exit code = %d; stderr = %s", got, statusErr.String())
	}
	if _, status := decodeStatus(t, statusOut.Bytes()); !slices.Contains(status.linkedIDs(), "codex") {
		t.Fatalf("root status output = %q, want linked Harness", statusOut.String())
	}

	var syncOut, syncErr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes", "--dry-run"}, &syncOut, &syncErr); got != 0 {
		t.Fatalf("root sync dry-run exit code = %d, want 0; stderr = %s", got, syncErr.String())
	}
	syncPlan := decodeEnvelope(t, syncOut.Bytes())
	var syncTargets []struct {
		Ref string `json:"ref"`
	}
	syncPlan.into(t, &syncTargets)
	if syncPlan.State != "planned" || len(syncTargets) != 1 || syncTargets[0].Ref != "main/release-notes" {
		t.Fatalf("root sync output = %q, want planned projection", syncOut.String())
	}
}

func TestCLIJSONEnvelopeSupportsSuccessDryRunBlockedResultAndJQ(t *testing.T) {
	t.Run("success and jq", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"version", "--jq", ".state"}, &stdout, &stderr); got != 0 {
			t.Fatalf("version exit code = %d; stderr = %s", got, stderr.String())
		}
		if got, want := strings.TrimSpace(stdout.String()), "\"clean\""; got != want {
			t.Fatalf("jq result = %q, want %q", got, want)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"namespace", "set-prefix", "main", "mw-", "--dry-run"}, &stdout, &stderr); got != 0 {
			t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
		}
		result := decodeEnvelope(t, stdout.Bytes())
		if result.State != "planned" || len(result.Problems) != 0 {
			t.Fatalf("dry-run JSON = %s", stdout.String())
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
		for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
			var setupOut, setupErr bytes.Buffer
			if got := run(arguments, &setupOut, &setupErr); got != 0 {
				t.Fatalf("setup %v exit code = %d; stderr = %s", arguments, got, setupErr.String())
			}
		}
		var stdout, stderr bytes.Buffer
		if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
			t.Fatalf("blocked exit code = %d, want 3; stderr = %s", got, stderr.String())
		}
		result := decodeEnvelope(t, stdout.Bytes())
		if result.State != "blocked" || len(result.Problems) != 1 || !strings.Contains(result.Problems[0].Message, "Unowned path") {
			t.Fatalf("blocked JSON = %s", stdout.String())
		}
	})
}

func TestCLIAcceptsJQWithoutAnyFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"version", "--jq", ".result.version"}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "\"dev\""; got != want {
		t.Fatalf("jq result = %q, want %q", got, want)
	}
}

func TestCLIReportsInvalidJSONFilterAsMachineProblem(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"version", "--jq", "["}, &stdout, &stderr); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	result := decodeEnvelope(t, stdout.Bytes())
	if result.State != "invalid" || len(result.Problems) != 1 || result.Problems[0].Code != "invalid_request" {
		t.Fatalf("JSON result = %s, want invalid state", stdout.String())
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
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "status", "--harness", "codex"}, &stdout, &stderr); got != 0 {
		t.Fatalf("status exit code = %d; stderr = %s", got, stderr.String())
	}
	if _, status := decodeStatus(t, stdout.Bytes()); !slices.Contains(status.linkedIDs(), "codex") {
		t.Fatalf("status output = %q, want filtered linked Harness", stdout.String())
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}

	var unlinkOut, unlinkErr bytes.Buffer
	if got, want := run([]string{"harness", "unlink", "codex"}, &unlinkOut, &unlinkErr), 0; got != want {
		t.Fatalf("unlink exit code = %d, want %d; stderr = %s", got, want, unlinkErr.String())
	}
	unlinkEnv := decodeEnvelope(t, unlinkOut.Bytes())
	var unlinked struct {
		Unlinked string `json:"unlinked"`
	}
	unlinkEnv.into(t, &unlinked)
	if unlinkEnv.State != "applied" || unlinked.Unlinked != "codex" {
		t.Fatalf("unlink output = %q, want applied unlink", unlinkOut.String())
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Release notes\n" {
		t.Fatalf("Harness files after unlink = %q (err=%v), want unchanged Skill", contents, err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--unowned"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	if _, status := decodeStatus(t, stdout.Bytes()); len(status.Linked) != 0 || len(status.Projections) != 0 {
		t.Fatalf("status output = %q, want no linked Harness or Projection claim", stdout.String())
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
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "unlink", "codex", "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	dryRun := decodeEnvelope(t, stdout.Bytes())
	var unlinked struct {
		Unlinked string `json:"unlinked"`
	}
	dryRun.into(t, &unlinked)
	if dryRun.State != "planned" || unlinked.Unlinked != "codex" {
		t.Fatalf("dry-run output = %q, want planned unlink", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "status"}, &stdout, &stderr); got != 0 {
		t.Fatalf("status exit code = %d; stderr = %s", got, stderr.String())
	}
	if _, status := decodeStatus(t, stdout.Bytes()); !slices.Contains(status.linkedIDs(), "codex") {
		t.Fatalf("status after dry-run = %q, want still linked", stdout.String())
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
	if got, want := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr), 0; got != want {
		t.Fatalf("link exit code = %d, want %d; stderr = %s", got, want, linkErr.String())
	}
	var statusOut, statusErr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &statusOut, &statusErr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, statusErr.String())
	}
	if output := statusOut.String(); !strings.Contains(output, "codex") || !strings.Contains(output, skills) {
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
	if !strings.Contains(discoverOut.String(), "claude") || !strings.Contains(discoverOut.String(), skills) {
		t.Fatalf("discover output = %q, want Claude fixture", discoverOut.String())
	}
	for _, arguments := range [][]string{{"harness", "link", "claude"}, {"skill", "add", source}, {"sync", "--harness", "claude", "--skill", "main/release-notes"}} {
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
	if !strings.Contains(discoverOut.String(), "opencode") || !strings.Contains(discoverOut.String(), skills) {
		t.Fatalf("discover output = %q, want OpenCode fixture", discoverOut.String())
	}
	for _, arguments := range [][]string{{"harness", "link", "opencode"}, {"skill", "add", source}, {"sync", "--harness", "opencode", "--skill", "main/release-notes"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"instruction", "add", source}, {"sync", "--harness", "codex", "--instruction", "main/personal-instructions"}} {
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
	if err := os.RemoveAll(filepath.Join(home, ".codex")); err != nil {
		t.Fatalf("remove projected Instruction root: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "missing" || status.projectionStatus("main/personal-instructions") != "missing" || !slices.Contains(env.problemCodes(), "projection_missing") {
		t.Fatalf("status output = %q, want missing Instruction Projection", stdout.String())
	}
	if problem := env.Problems[0]; problem.Kind != "instruction" || problem.Ref != "main/personal-instructions" || problem.Remedy != "brigsby sync --instruction main/personal-instructions --harness codex" {
		t.Fatalf("status problem = %+v, want Instruction restore command", problem)
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
	if got := run([]string{"skill", "add", source}, &stdout, &stderr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, stderr.String())
	}
	for _, arguments := range [][]string{{"package", "create", "--skill", "main/release-notes", "--output", filepath.Join(home, "release-notes.tar.gz")}, {"package", "inspect", filepath.Join(home, "release-notes.tar.gz")}} {
		stdout.Reset()
		stderr.Reset()
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
		var result struct {
			Digest string          `json:"digest"`
			Skills json.RawMessage `json:"skills"`
		}
		decodeEnvelopeResult(t, stdout.Bytes(), &result)
		if !strings.HasPrefix(result.Digest, "sha256-") {
			t.Fatalf("%v result = %s, want a package digest", arguments, stdout.String())
		}
		if arguments[1] == "inspect" && !strings.Contains(string(result.Skills), "main/release-notes") {
			t.Fatalf("inspect skills = %s, want included Skill ref", result.Skills)
		}
	}
}

func TestCLIActionableDomainErrorsDoNotPrintUsage(t *testing.T) {
	for _, arguments := range [][]string{
		{"package", "create", "--skill", "main/release-notes"},
		{"skill", "promote", "friend/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 3 {
			t.Fatalf("%v exit code = %d, want 3; stderr=%q", arguments, got, stderr.String())
		}
		if strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("%v stderr = %q, want concise domain error", arguments, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"skill", "promote", "friend/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("JSON domain error exit code = %d, want 3; stderr=%q", got, stderr.String())
	}
	if got := decodeEnvelope(t, stdout.Bytes()); len(got.Problems) != 1 || got.Problems[0].Code != "domain_error" {
		t.Fatalf("JSON domain error = %q, want one domain_error problem", stdout.String())
	}
}

func TestHarnessStatusExplainsNoLinkedHarnesses(t *testing.T) {
	t.Setenv("BRIGSBY_HOME", filepath.Join(t.TempDir(), ".brigsby"))
	var stdout, stderr bytes.Buffer
	if got := run([]string{"harness", "status"}, &stdout, &stderr); got != 0 {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "clean" || len(status.Linked) != 0 || len(env.Problems) != 0 {
		t.Fatalf("status = %q, want clean with no linked Harnesses", stdout.String())
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
	for _, arguments := range [][]string{{"skill", "add", source}, {"package", "create", "--skill", "main/release-notes", "--output", archive}} {
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
	var imported struct {
		Imported []struct {
			Ref    string `json:"ref"`
			Digest string `json:"digest"`
		} `json:"imported"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &imported)
	if len(imported.Imported) != 1 || imported.Imported[0].Ref != "friend/release-notes" {
		t.Fatalf("import result=%q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(recipient, ".brigsby", "skills", "main", "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("import changed main: %v", err)
	}
	digest := imported.Imported[0].Digest
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"skill", "promote", "friend/release-notes", "--revision", digest}, &stdout, &stderr); got != 0 {
		t.Fatalf("promote exit=%d stderr=%s", got, stderr.String())
	}
	var promoted struct {
		Promoted struct {
			Ref    string `json:"ref"`
			Origin struct {
				Ref      string `json:"ref"`
				Revision string `json:"revision"`
			} `json:"origin"`
		} `json:"promoted"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &promoted)
	if promoted.Promoted.Ref != "main/release-notes" || promoted.Promoted.Origin.Ref != "friend/release-notes" {
		t.Fatalf("promote result=%q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"skill", "show", "main/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("show exit=%d stderr=%s", got, stderr.String())
	}
	var shown struct {
		Revision struct {
			Origin struct {
				Ref      string `json:"ref"`
				Revision string `json:"revision"`
			} `json:"origin"`
		} `json:"revision"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &shown)
	if shown.Revision.Origin.Ref != "friend/release-notes" || shown.Revision.Origin.Revision != digest {
		t.Fatalf("show result=%q", stdout.String())
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
	for _, arguments := range [][]string{{"skill", "add", filepath.Join(sender, "alpha")}, {"skill", "add", filepath.Join(sender, "beta")}, {"package", "create", "--skill", "main/alpha", "--skill", "main/beta", "--output", archive}} {
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
	if got := run([]string{"skill", "add", conflicting, "--namespace", "friend"}, &stdout, &stderr); got != 0 {
		t.Fatalf("capture collision fixture exit=%d stderr=%s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"package", "import", archive, "--namespace", "friend"}, &stdout, &stderr); got != 3 {
		t.Fatalf("import exit=%d stderr=%s", got, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(recipient, ".brigsby", "skills", "friend", "alpha")); !os.IsNotExist(err) {
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
	for _, arguments := range [][]string{{"harness", "link", "claude"}, {"instruction", "add", source}, {"sync", "--harness", "claude", "--instruction", "main/personal-instructions"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "opencode"}, {"instruction", "add", source}, {"sync", "--harness", "opencode", "--instruction", "main/personal-instructions"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"instruction", "add", source}, {"sync", "--harness", "codex", "--instruction", "main/personal-instructions"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatalf("edit projected Instruction root: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--instruction", "main/personal-instructions"}, &stdout, &stderr); got != 3 || !strings.Contains(stderr.String(), "BLOCKED: Instruction Projection") {
		t.Fatalf("drifted sync exit=%d stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "rerun with --force") {
		t.Fatalf("drifted sync stderr=%q, want --force guidance", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--instruction", "main/personal-instructions", "--force"}, &stdout, &stderr); got != 0 {
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
	for _, arguments := range [][]string{{"harness", "link", "opencode"}, {"skill", "add", source}, {"namespace", "set-prefix", "main", "mw-"}, {"sync", "--harness", "opencode", "--skill", "main/release-notes"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "claude"}, {"skill", "add", source}, {"namespace", "set-prefix", "main", "mw-"}, {"sync", "--harness", "claude", "--skill", "main/release-notes"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "claude"}, {"skill", "add", source}, {"sync", "--harness", "claude", "--skill", "main/release-notes"}, {"namespace", "set-prefix", "main", "mw-"}, {"sync", "--harness", "claude", "--skill", "main/release-notes"}} {
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
	for _, arguments := range [][]string{{"harness", "link", "claude"}, {"skill", "add", source}, {"sync", "--harness", "claude", "--skill", "main/release-notes"}, {"namespace", "set-prefix", "main", "mw-"}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("drift previous projection: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "claude", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
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
	if got, want := run([]string{"skill", "add", source}, &addOut, &addErr), 0; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, addErr.String())
	}
	if !strings.Contains(addOut.String(), "main/release-notes") || !strings.Contains(addOut.String(), "sha256-") {
		t.Fatalf("add output = %q, want selector and revision digest", addOut.String())
	}

	var showOut, showErr bytes.Buffer
	if got, want := run([]string{"skill", "show", "main/release-notes"}, &showOut, &showErr), 0; got != want {
		t.Fatalf("show exit code = %d, want %d; stderr = %s", got, want, showErr.String())
	}
	if !strings.Contains(showOut.String(), "main/release-notes") || !strings.Contains(showOut.String(), "sha256-") {
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
	if got := run([]string{"skill", "add", source}, &addOut, &addErr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, addErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "list"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("list exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	var listed struct {
		Skills []struct {
			Ref    string `json:"ref"`
			Digest string `json:"digest"`
		} `json:"skills"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &listed)
	if len(listed.Skills) != 1 || listed.Skills[0].Ref != "main/release-notes" || !strings.HasPrefix(listed.Skills[0].Digest, "sha256-") {
		t.Fatalf("list output = %q, want captured Skill", stdout.String())
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
	if got := run([]string{"skill", "add", source}, &addOut, &addErr); got != 0 {
		t.Fatalf("add exit code = %d; stderr = %s", got, addErr.String())
	}

	listRefs := func(group string, arguments ...string) []string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if got := run(append([]string{group, "list"}, arguments...), &stdout, &stderr); got != 0 {
			t.Fatalf("list %s %v exit code = %d; stderr = %s", group, arguments, got, stderr.String())
		}
		var listed map[string][]struct {
			Ref string `json:"ref"`
		}
		decodeEnvelopeResult(t, stdout.Bytes(), &listed)
		var refs []string
		for _, entry := range listed[group+"s"] {
			refs = append(refs, entry.Ref)
		}
		return refs
	}
	if got := listRefs("instruction"); len(got) != 0 {
		t.Fatalf("instructions list = %v, want empty", got)
	}
	if got := listRefs("skill", "--namespace", "main"); !slices.Contains(got, "main/release-notes") {
		t.Fatalf("skills list = %v, want captured Skill", got)
	}
	if got := listRefs("skill", "--namespace", "imported"); len(got) != 0 {
		t.Fatalf("imported list = %v, want empty", got)
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
	if got := run([]string{"skill", "add", source}, &firstOut, &firstErr); got != 0 {
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
	if got := run([]string{"skill", "add", source, "--name", "release-notes"}, &secondOut, &secondErr); got != 0 {
		t.Fatalf("second add exit code = %d; stderr = %s", got, secondErr.String())
	}
	second := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(secondOut.String())
	if second == "" || second == first {
		t.Fatalf("second add output = %q, want a different digest than %s", secondOut.String(), first)
	}

	var selectOut, selectErr bytes.Buffer
	if got, want := run([]string{"skill", "select", "main/release-notes", "--revision", first}, &selectOut, &selectErr), 0; got != want {
		t.Fatalf("select exit code = %d, want %d; stderr = %s", got, want, selectErr.String())
	}
	var selected struct {
		Selected struct {
			Ref    string `json:"ref"`
			Digest string `json:"digest"`
		} `json:"selected"`
	}
	decodeEnvelopeResult(t, selectOut.Bytes(), &selected)
	if selected.Selected.Ref != "main/release-notes" || selected.Selected.Digest != first {
		t.Fatalf("select output = %q, want first revision selected", selectOut.String())
	}
	var showOut, showErr bytes.Buffer
	if got := run([]string{"skill", "show", "main/release-notes"}, &showOut, &showErr); got != 0 {
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
	if got := run([]string{"skill", "add", source}, &firstOut, &firstErr); got != 0 {
		t.Fatalf("first add exit code = %d; stderr = %s", got, firstErr.String())
	}
	first := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(firstOut.String())
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("update Skill fixture: %v", err)
	}
	var secondOut, secondErr bytes.Buffer
	if got := run([]string{"skill", "add", source, "--name", "release-notes"}, &secondOut, &secondErr); got != 0 {
		t.Fatalf("second add exit code = %d; stderr = %s", got, secondErr.String())
	}
	second := regexp.MustCompile(`sha256-[0-9a-f]+`).FindString(secondOut.String())

	var stdout, stderr bytes.Buffer
	if got := run([]string{"skill", "select", "main/release-notes", "--revision", first, "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("dry-run exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	dryRun := decodeEnvelope(t, stdout.Bytes())
	var selected struct {
		Selected struct {
			Ref    string `json:"ref"`
			Digest string `json:"digest"`
		} `json:"selected"`
	}
	dryRun.into(t, &selected)
	if dryRun.State != "planned" || selected.Selected.Ref != "main/release-notes" || selected.Selected.Digest != first {
		t.Fatalf("dry-run output = %q, want planned select", stdout.String())
	}
	var showOut, showErr bytes.Buffer
	if got := run([]string{"skill", "show", "main/release-notes"}, &showOut, &showErr); got != 0 {
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
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
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "clean" || !slices.Contains(status.linkedIDs(), "codex") {
		t.Fatalf("status output = %q, want clean linked Harness", stdout.String())
	}
	if status.projectionStatus("main/release-notes") != "projected" || len(env.Problems) != 0 {
		t.Fatalf("status output = %q, want a clean Projection with no problems", stdout.String())
	}
}

func TestHarnessStatusReportsStateErrorWhenCanonicalSkillIsMissing(t *testing.T) {
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d, want 0; stderr = %s", arguments, got, stderr.String())
		}
	}
	revisions := filepath.Join(home, ".brigsby", "skills", "main", "release-notes", "revisions")
	entries, err := os.ReadDir(revisions)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read canonical Skill revisions: entries=%v, err=%v", entries, err)
	}
	if err := os.RemoveAll(filepath.Join(revisions, entries[0].Name(), "files")); err != nil {
		t.Fatalf("remove canonical Skill revision fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 3; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	env, _ := decodeStatus(t, stdout.Bytes())
	if env.State != "invalid" || !slices.Contains(env.problemCodes(), "state_error") {
		t.Fatalf("status output = %q, want invalid state_error", stdout.String())
	}
	if !strings.Contains(env.problemText(), "canonical skill main/release-notes is unavailable") {
		t.Fatalf("status problems = %+v, want unavailable canonical Skill", env.Problems)
	}
	if problem := env.Problems[0]; problem.Harness != "codex" || problem.Kind != "skill" || problem.Ref != "main/release-notes" || problem.Path != filepath.Join(skills, "release-notes") {
		t.Fatalf("status problem = %+v, want structured canonical-state error details", problem)
	}
}

func TestHarnessStatusReportsMissingProjectionWithRestoreCommand(t *testing.T) {
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.RemoveAll(filepath.Join(skills, "release-notes")); err != nil {
		t.Fatalf("remove projected Skill: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "missing" || status.projectionStatus("main/release-notes") != "missing" || !slices.Contains(env.problemCodes(), "projection_missing") {
		t.Fatalf("status output = %q, want missing projection", stdout.String())
	}
	problem := env.Problems[0]
	if problem.Harness != "codex" || problem.Kind != "skill" || problem.Ref != "main/release-notes" || problem.Path != filepath.Join(skills, "release-notes") || problem.Remedy != "brigsby sync --skill main/release-notes --harness codex" || !strings.Contains(problem.Message, "is missing") {
		t.Fatalf("status problem = %+v, want self-contained missing details and restore command", problem)
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
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
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "drifted" || status.projectionStatus("main/release-notes") != "drift" {
		t.Fatalf("status output = %q, want Drift for the projected Skill", stdout.String())
	}
	if !slices.Contains(env.problemCodes(), "projection_drift") {
		t.Fatalf("status problems = %+v, want a projection_drift problem", env.Problems)
	}
	if problem := env.Problems[0]; problem.Harness != "codex" || problem.Kind != "skill" || problem.Ref != "main/release-notes" || problem.Path != filepath.Join(skills, "release-notes") || !strings.Contains(problem.Message, "differs from its recorded content") {
		t.Fatalf("status problem = %+v, want self-contained drift details", problem)
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
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--unowned"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "unowned" || !strings.Contains(env.problemText(), "is not managed by Brigsby") {
		t.Fatalf("status output = %q, want self-contained unowned path problem", stdout.String())
	}
	if problem := env.Problems[0]; problem.Harness != "codex" || problem.Kind != "skill" || problem.Path != skills {
		t.Fatalf("status problem = %+v, want structured unowned path details", problem)
	}
	if len(status.Projections) != 0 || slices.Contains(env.problemCodes(), "projection_drift") {
		t.Fatalf("status output = %q, want Unowned path without Projection or Drift", stdout.String())
	}
}

func TestHarnessStatusReportsStaleProjectionWhenSelectedRevisionChanges(t *testing.T) {
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
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
	if got := run([]string{"skill", "add", source, "--name", "release-notes"}, &addOut, &addErr); got != 0 {
		t.Fatalf("recapture exit code = %d; stderr = %s", got, addErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("status exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	env, status := decodeStatus(t, stdout.Bytes())
	if env.State != "stale" || status.projectionStatus("main/release-notes") != "stale" || !slices.Contains(env.problemCodes(), "projection_stale") {
		t.Fatalf("status output = %q, want stale Projection after selected Revision changed", stdout.String())
	}
	if problem := env.Problems[0]; problem.Harness != "codex" || problem.Kind != "skill" || problem.Ref != "main/release-notes" || problem.Path != filepath.Join(skills, "release-notes") || problem.Remedy != "brigsby sync --skill main/release-notes --harness codex" || !strings.Contains(problem.Message, "stale but unchanged") {
		t.Fatalf("status problem = %+v, want self-contained stale details and update command", problem)
	}
}

func TestHarnessSyncFastForwardsPristineStaleProjectionWithRecovery(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# First revision\n"), 0o644); err != nil {
		t.Fatalf("write first Skill revision: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("write second Skill revision: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"skill", "add", source, "--name", "release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("recapture exit code = %d; stderr = %s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes", "--dry-run"}, &stdout, &stderr); got != 0 || decodeEnvelope(t, stdout.Bytes()).State != "planned" {
		t.Fatalf("fast-forward dry-run exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("fast-forward sync exit=%d stderr=%q", got, stderr.String())
	}
	syncIDs := syncRecoveryIDs(t, decodeEnvelope(t, stdout.Bytes()))
	if len(syncIDs) == 0 {
		t.Fatalf("fast-forward output = %q, want Recovery ID", stdout.String())
	}
	id := []string{"", syncIDs[0]}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Second revision\n" {
		t.Fatalf("fast-forward Projection = %q, err=%v", contents, err)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"recovery", "restore", id[1]}, &stdout, &stderr); got != 0 {
		t.Fatalf("restore fast-forward preimage exit=%d stderr=%q", got, stderr.String())
	}
	contents, err = os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# First revision\n" {
		t.Fatalf("restored preimage = %q, err=%v", contents, err)
	}
}

func TestHarnessSyncBlocksEditedStaleProjection(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# First revision\n"), 0o644); err != nil {
		t.Fatalf("write first Skill revision: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	for _, arguments := range [][]string{
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Local edit\n"), 0o644); err != nil {
		t.Fatalf("edit Projection: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Second revision\n"), 0o644); err != nil {
		t.Fatalf("write second Skill revision: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"skill", "add", source, "--name", "release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("recapture exit code = %d; stderr = %s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("edited stale sync exit=%d stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "BLOCKED: Drift") || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("edited stale sync stderr=%q, want concise drift block", stderr.String())
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
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
	result := decodeEnvelope(t, stdout.Bytes())
	if result.State != "drifted" {
		t.Fatalf("JSON result = %s", stdout.String())
	}
	if len(result.Problems) != 1 || result.Problems[0].Code != "projection_drift" {
		t.Fatalf("JSON problems = %+v, want projection_drift", result.Problems)
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

	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := harness.NewRegistry(root).Link(harness.Candidate{ID: "codex-work", Name: "codex", SkillsPath: workSkills}); err != nil {
		t.Fatalf("link work fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--harness", "codex-work", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	if err := harness.NewRegistry(root).Link(harness.Candidate{ID: "codex-work", Name: "codex", SkillsPath: workSkills}); err != nil {
		t.Fatalf("link work fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--harness", "codex-work", "--skill", "main/release-notes"}, &stdout, &stderr); got != 0 {
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

	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var syncOut, syncErr bytes.Buffer
	if got, want := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &syncOut, &syncErr), 0; got != want {
		t.Fatalf("sync exit code = %d, want %d; stderr = %s", got, want, syncErr.String())
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil {
		t.Fatalf("read projected Skill: %v", err)
	}
	if got, want := string(contents), "# Release notes\n"; got != want {
		t.Fatalf("projected Skill = %q, want %q", got, want)
	}
	if decodeEnvelope(t, syncOut.Bytes()).State != "applied" {
		t.Fatalf("sync output = %q, want applied state", syncOut.String())
	}
}

func TestHarnessSyncBlocksUnownedPathWithSkillAddAndForce(t *testing.T) {
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "Unowned path") || !strings.Contains(output, "skill add "+local) || !strings.Contains(output, "rerun with --force") {
		t.Fatalf("blocked output = %q, want Unowned path with skill add and force", output)
	}
	if strings.Contains(output, "Usage:") {
		t.Fatalf("blocked output = %q, want the actionable blocker without command help", output)
	}
	if strings.Contains(output, "Drift") {
		t.Fatalf("blocked output = %q, want Unowned path rather than Drift", output)
	}
}

func TestHarnessSyncBlocksDriftWithSkillAddAndForce(t *testing.T) {
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
		{"harness", "link", "codex"},
		{"skill", "add", source},
		{"sync", "--harness", "codex", "--skill", "main/release-notes"},
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
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "Drift") || !strings.Contains(output, "skill add "+projected) || !strings.Contains(output, "rerun with --force") {
		t.Fatalf("blocked output = %q, want Drift with skill add and force", output)
	}
	if strings.Contains(output, "Unowned path") {
		t.Fatalf("blocked output = %q, want Drift rather than Unowned path", output)
	}
}

func TestHarnessSyncBlocksChangedTargetWithAReadyToRunForce(t *testing.T) {
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 {
		t.Fatalf("sync exit code = %d, want 3; stderr = %s", got, stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "BLOCKED") || !strings.Contains(output, "rerun with --force") {
		t.Fatalf("blocked output = %q, want ready-to-run force action", output)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes", "--force"}, &stdout, &stderr); got != 0 {
		t.Fatalf("force exit code = %d; stderr = %s", got, stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(skills, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "# Canonical version\n" {
		t.Fatalf("target after force = %q (err=%v)", contents, err)
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
	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes", "--dry-run"}, &stdout, &stderr); got != 0 {
		t.Fatalf("sync exit code = %d, want 0; stderr = %s", got, stderr.String())
	}
	result := decodeEnvelope(t, stdout.Bytes())
	if result.State != "planned" {
		t.Fatalf("JSON result = %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(skills, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a projection, stat error = %v", err)
	}
}

func TestCLIVersion(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if got := run([]string{"version"}, &stdout, &stderr); got != 0 {
		t.Fatalf("version exit code = %d; stderr = %s", got, stderr.String())
	}
	var result struct {
		Version string `json:"version"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &result)
	if result.Version != "dev" {
		t.Fatalf("version result = %q, want dev", stdout.String())
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
		{name: "injected", ldflags: []string{"-ldflags", "-X main.version=1.2.3"}, want: "1.2.3"},
		{name: "checkout stays dev", ldflags: nil, want: "dev"},
	} {
		binary := filepath.Join(directory, "brigsby-"+testCase.name)
		build := exec.Command("go", append(append([]string{"build"}, testCase.ldflags...), "-o", binary, ".")...)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%s build: %v\n%s", testCase.name, err, output)
		}

		// The `version` subcommand is a machine result: a JSON envelope.
		output, err := exec.Command(binary, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s: brigsby version: %v\n%s", testCase.name, err, output)
		}
		var result struct {
			Version string `json:"version"`
		}
		decodeEnvelopeResult(t, output, &result)
		if result.Version != testCase.want {
			t.Fatalf("%s: brigsby version = %q, want version %q", testCase.name, output, testCase.want)
		}

		// The --version flag is the conventional plain-text affordance.
		output, err = exec.Command(binary, "--version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s: brigsby --version: %v\n%s", testCase.name, err, output)
		}
		if got := strings.TrimSpace(string(output)); got != "brigsby "+testCase.want {
			t.Fatalf("%s: brigsby --version = %q, want %q", testCase.name, got, "brigsby "+testCase.want)
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

func TestHarnessSyncIsNotACommand(t *testing.T) {
	code, envelope, stderr := execCLI(t, "harness", "sync")
	if code != 2 || envelope.State != "invalid" {
		t.Fatalf("harness sync code=%d envelope=%+v, want invalid command", code, envelope)
	}
	if !strings.Contains(stderr, "unknown command \"sync\"") {
		t.Fatalf("harness sync stderr=%q, want unknown-command diagnostic", stderr)
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

	for _, arguments := range [][]string{{"harness", "link", "codex"}, {"skill", "add", source}} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 0 {
		t.Fatalf("content-identical existing Skill sync exit=%d stderr=%s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"skill", "show", "main/release-notes", "--files"}, &stdout, &stderr); got != 0 {
		t.Fatalf("show files exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	var shown struct {
		Files []struct {
			Path     string `json:"path"`
			Bytes    int    `json:"bytes"`
			Contents string `json:"contents"`
		} `json:"files"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &shown)
	if len(shown.Files) != 1 || shown.Files[0].Path != "SKILL.md" || shown.Files[0].Bytes != 16 || shown.Files[0].Contents != "# Release notes\n" {
		t.Fatalf("show files result=%q, want the SKILL.md text file", stdout.String())
	}
	if err := os.WriteFile(filepath.Join(skills, "release-notes", "SKILL.md"), []byte("# Local edit\n"), 0o644); err != nil {
		t.Fatalf("edit existing Skill: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"sync", "--harness", "codex", "--skill", "main/release-notes"}, &stdout, &stderr); got != 3 || !strings.Contains(stderr.String(), "BLOCKED:") {
		t.Fatalf("changed-content sync exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"harness", "discover"}, &stdout, &stderr); got != 0 {
		t.Fatalf("discover exit=%d stderr=%s", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"instruction", "add", filepath.Join(home, ".codex")}, &stdout, &stderr); got != 3 || !strings.Contains(stderr.String(), "structured Instruction set") || strings.Contains(stderr.String(), "Usage:") {
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

// cliEnvelope is the single JSON object every command prints to stdout.
type cliEnvelope struct {
	State       string          `json:"state"`
	Problems    []cliProblem    `json:"problems"`
	Result      json.RawMessage `json:"result"`
	RecoveryIDs []string        `json:"recovery_ids"`
}

type cliProblem struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Harness string `json:"harness"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Ref     string `json:"ref"`
	Remedy  string `json:"remedy"`
}

// execCLI runs one invocation and returns its exit code, the decoded stdout
// envelope, and stderr. Every result -- success, blocked, or invalid -- is a
// JSON envelope on stdout now, so stderr is empty except for the help path.
func execCLI(t *testing.T, arguments ...string) (int, cliEnvelope, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(arguments, &stdout, &stderr)
	var envelope cliEnvelope
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("brigsby %s: stdout is not one JSON envelope: %v\n%s", strings.Join(arguments, " "), err, stdout.String())
		}
	}
	return code, envelope, stderr.String()
}

// mustCLI runs one invocation, requires exit 0, and returns the envelope.
func mustCLI(t *testing.T, arguments ...string) cliEnvelope {
	t.Helper()
	code, envelope, stderr := execCLI(t, arguments...)
	if code != 0 {
		t.Fatalf("brigsby %s exit=%d stderr=%s problems=%+v", strings.Join(arguments, " "), code, stderr, envelope.Problems)
	}
	return envelope
}

// into decodes the envelope's result payload into target.
func (e cliEnvelope) into(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(e.Result, target); err != nil {
		t.Fatalf("decode result %s: %v", e.Result, err)
	}
}

// decodeEnvelope parses one stdout blob into the canonical envelope.
func decodeEnvelope(t *testing.T, raw []byte) cliEnvelope {
	t.Helper()
	var envelope cliEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, raw)
	}
	return envelope
}

// decodeEnvelopeResult parses one stdout blob and decodes its result into target.
func decodeEnvelopeResult(t *testing.T, raw []byte, target any) {
	t.Helper()
	decodeEnvelope(t, raw).into(t, target)
}

// addedRefs returns result.revisions[].ref from a `skill add` / `instruction
// add` envelope.
func addedRefs(t *testing.T, raw []byte) []string {
	t.Helper()
	var added struct {
		Revisions []struct {
			Ref    string `json:"ref"`
			Kind   string `json:"kind"`
			Digest string `json:"digest"`
		} `json:"revisions"`
	}
	decodeEnvelopeResult(t, raw, &added)
	refs := make([]string, len(added.Revisions))
	for index, revision := range added.Revisions {
		refs[index] = revision.Ref
	}
	return refs
}

// problemText joins every problem message so a test can assert on the blocker
// prose the CLI used to print to stderr.
func (e cliEnvelope) problemText() string {
	parts := make([]string, len(e.Problems))
	for index, problem := range e.Problems {
		parts[index] = problem.Message
	}
	return strings.Join(parts, "\n")
}

// statusResult mirrors `harness status` result.{linked,projections}.
type statusResult struct {
	Linked []struct {
		ID         string `json:"id"`
		Harness    string `json:"harness"`
		SkillsPath string `json:"skills_path"`
	} `json:"linked"`
	Projections []struct {
		Harness  string `json:"harness"`
		Kind     string `json:"kind"`
		Ref      string `json:"ref"`
		Revision string `json:"revision"`
		Path     string `json:"path"`
		Status   string `json:"status"`
	} `json:"projections"`
}

func decodeStatus(t *testing.T, raw []byte) (cliEnvelope, statusResult) {
	t.Helper()
	envelope := decodeEnvelope(t, raw)
	var result statusResult
	envelope.into(t, &result)
	return envelope, result
}

func (s statusResult) linkedIDs() []string {
	ids := make([]string, len(s.Linked))
	for index, linked := range s.Linked {
		ids[index] = linked.ID
	}
	return ids
}

// projectionStatus returns the reported status for one Skill/Instruction's
// Projection, or "" when status has none. ref is "namespace/name".
func (s statusResult) projectionStatus(ref string) string {
	for _, projection := range s.Projections {
		if projection.Ref == ref {
			return projection.Status
		}
	}
	return ""
}

// problemCodes returns every problem code in an envelope.
func (e cliEnvelope) problemCodes() []string {
	codes := make([]string, len(e.Problems))
	for index, problem := range e.Problems {
		codes[index] = problem.Code
	}
	return codes
}

// syncRecoveryIDs returns recovery_ids from a sync envelope.
func syncRecoveryIDs(t *testing.T, e cliEnvelope) []string {
	t.Helper()
	return e.RecoveryIDs
}

// firstSyncRecoveryID runs a sync, requires success, and returns its single
// Recovery ID -- the replacement for the old `recovery=<id>` text scrape.
func firstSyncRecoveryID(t *testing.T, arguments ...string) string {
	t.Helper()
	envelope := mustCLI(t, arguments...)
	ids := syncRecoveryIDs(t, envelope)
	if len(ids) == 0 {
		t.Fatalf("brigsby %s: no recovery_ids in %s", strings.Join(arguments, " "), envelope.Result)
	}
	return ids[0]
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

func TestArtifactAddCapturesEverySkillUnderADirectory(t *testing.T) {
	home := t.TempDir()
	tree := filepath.Join(home, "skills")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		directory := filepath.Join(tree, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create Skill fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write Skill fixture: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tree, "not-a-skill"), 0o755); err != nil {
		t.Fatalf("create non-Skill directory: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "add", tree}, &stdout, &stderr), 0; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	captured := addedRefs(t, stdout.Bytes())
	for _, selector := range []string{"main/alpha", "main/beta", "main/gamma"} {
		if !slices.Contains(captured, selector) {
			t.Fatalf("add result = %v, want captured %s", captured, selector)
		}
	}
	if slices.ContainsFunc(captured, func(s string) bool { return strings.Contains(s, "not-a-skill") }) {
		t.Fatalf("add result = %v, want no capture for a directory without SKILL.md", captured)
	}
}

func TestArtifactAddCapturesMultipleExplicitPaths(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "one")
	second := filepath.Join(home, "two")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create Skill fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("# "+filepath.Base(directory)+"\n"), 0o644); err != nil {
			t.Fatalf("write Skill fixture: %v", err)
		}
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "add", first, second}, &stdout, &stderr), 0; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	captured := addedRefs(t, stdout.Bytes())
	if !slices.Contains(captured, "main/one") || !slices.Contains(captured, "main/two") {
		t.Fatalf("add result = %v, want both Skills captured", captured)
	}
}

func TestArtifactAddRejectsNameWithMultipleSkills(t *testing.T) {
	home := t.TempDir()
	tree := filepath.Join(home, "skills")
	for _, name := range []string{"alpha", "beta"} {
		directory := filepath.Join(tree, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create Skill fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write Skill fixture: %v", err)
		}
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "add", tree, "--name", "combined"}, &stdout, &stderr), 3; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--name cannot rename 2 Skills") || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("add error = %q, want a bare multiple-Skill --name rejection", stderr.String())
	}
}

func TestArtifactAddDirectoryWithoutSkillsReportsActionableError(t *testing.T) {
	home := t.TempDir()
	empty := filepath.Join(home, "empty")
	if err := os.MkdirAll(filepath.Join(empty, "child"), 0o755); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "add", empty}, &stdout, &stderr), 3; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no root-level SKILL.md and no immediate subdirectory containing one") || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("add error = %q, want a bare actionable no-Skill error", stderr.String())
	}
}

func TestArtifactAddNotesUntrackedLinkedHarnessSource(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, ".agents", "skills")
	source := filepath.Join(skills, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Codex fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	var linkOut, linkErr bytes.Buffer
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "add", source}, &stdout, &stderr), 0; got != want {
		t.Fatalf("add exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if !slices.Contains(addedRefs(t, stdout.Bytes()), "main/release-notes") {
		t.Fatalf("add result = %q, want captured Artifact", stdout.String())
	}
	if !strings.Contains(stderr.String(), "NOTE ") || !strings.Contains(stderr.String(), "not drift-tracked") ||
		!strings.Contains(stderr.String(), "brigsby sync --skill main/release-notes --harness codex") {
		t.Fatalf("add advisory = %q, want untracked-source NOTE with the sync command", stderr.String())
	}
}

func TestHarnessStatusDefaultsToManagedAndCanShowUnownedOrAll(t *testing.T) {
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
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var plainOut, plainErr bytes.Buffer
	if got, want := run([]string{"harness", "status"}, &plainOut, &plainErr), 0; got != want {
		t.Fatalf("plain status exit code = %d, want %d; stderr = %s", got, want, plainErr.String())
	}
	if plainEnv := decodeEnvelope(t, plainOut.Bytes()); plainEnv.State != "clean" || slices.Contains(plainEnv.problemCodes(), "unowned_path") {
		t.Fatalf("plain status output = %q, want managed-only clean status", plainOut.String())
	}

	var managedOut, managedErr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--managed"}, &managedOut, &managedErr), 0; got != want {
		t.Fatalf("managed status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, managedErr.String(), managedOut.String())
	}
	managedEnv, managedStatus := decodeStatus(t, managedOut.Bytes())
	if managedEnv.State != "clean" || len(managedEnv.Problems) != 0 {
		t.Fatalf("managed status = %q, want a clean state with no problems", managedOut.String())
	}
	if !slices.Contains(managedStatus.linkedIDs(), "codex") {
		t.Fatalf("managed status = %q, want the linked Harness", managedOut.String())
	}

	var unownedOut, unownedErr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--unowned"}, &unownedOut, &unownedErr), 0; got != want {
		t.Fatalf("unowned status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, unownedErr.String(), unownedOut.String())
	}
	if unownedEnv := decodeEnvelope(t, unownedOut.Bytes()); unownedEnv.State != "unowned" || !slices.Contains(unownedEnv.problemCodes(), "unowned_path") {
		t.Fatalf("unowned status output = %q, want an Unowned path", unownedOut.String())
	}

	var allOut, allErr bytes.Buffer
	if got, want := run([]string{"harness", "status", "--all"}, &allOut, &allErr), 0; got != want {
		t.Fatalf("all status exit code = %d, want %d; stderr = %s; stdout = %s", got, want, allErr.String(), allOut.String())
	}
	if allEnv := decodeEnvelope(t, allOut.Bytes()); allEnv.State != "unowned" || !slices.Contains(allEnv.problemCodes(), "unowned_path") {
		t.Fatalf("all status output = %q, want managed and Unowned paths", allOut.String())
	}
}

func TestRootStatusManagedAliasOmitsUnownedPaths(t *testing.T) {
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
	if got := run([]string{"harness", "link", "codex"}, &linkOut, &linkErr); got != 0 {
		t.Fatalf("link exit code = %d; stderr = %s", got, linkErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"status", "--managed"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("root status --managed exit code = %d, want %d; stderr = %s; stdout = %s", got, want, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), "UNOWNED") {
		t.Fatalf("root status --managed output = %q, want no Unowned paths", stdout.String())
	}
}

func TestArtifactPromoteWithoutRevisionUsesSoleStoredRevision(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Release notes\n"), 0o644); err != nil {
		t.Fatalf("write Skill fixture: %v", err)
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	for _, arguments := range [][]string{
		{"skill", "add", source, "--namespace", "friend"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "promote", "friend/release-notes"}, &stdout, &stderr), 0; got != want {
		t.Fatalf("promote exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	var promoted struct {
		Promoted struct {
			Ref    string `json:"ref"`
			Digest string `json:"digest"`
		} `json:"promoted"`
	}
	decodeEnvelopeResult(t, stdout.Bytes(), &promoted)
	if promoted.Promoted.Ref != "main/release-notes" || !strings.HasPrefix(promoted.Promoted.Digest, "sha256-") {
		t.Fatalf("promote result = %q, want promotion of the sole stored Revision", stdout.String())
	}
}

func TestArtifactPromoteWithoutRevisionRejectsAmbiguousHistory(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Skill fixture: %v", err)
	}
	writeRevision := func(body string) {
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write Skill fixture: %v", err)
		}
	}
	t.Setenv("BRIGSBY_HOME", filepath.Join(home, ".brigsby"))
	writeRevision("# Release notes v1\n")
	for _, arguments := range [][]string{
		{"skill", "add", source, "--namespace", "friend"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(arguments, &stdout, &stderr); got != 0 {
			t.Fatalf("%v exit code = %d; stderr = %s", arguments, got, stderr.String())
		}
	}
	writeRevision("# Release notes v2\n")
	var secondOut, secondErr bytes.Buffer
	if got := run([]string{"skill", "add", source, "--namespace", "friend", "--name", "release-notes"}, &secondOut, &secondErr); got != 0 {
		t.Fatalf("second add exit code = %d; stderr = %s", got, secondErr.String())
	}

	var stdout, stderr bytes.Buffer
	if got, want := run([]string{"skill", "promote", "friend/release-notes"}, &stdout, &stderr), 3; got != want {
		t.Fatalf("promote exit code = %d, want %d; stderr = %s", got, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), "has 2 stored Revisions") || !strings.Contains(stderr.String(), "--revision") || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("promote error = %q, want bare ambiguous-history guidance", stderr.String())
	}
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
