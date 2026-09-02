package recovery_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CapedHero/brigsby/internal/recovery"
)

func TestPlanDoesNotWriteFilesOrRecoveryState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")

	service := recovery.New(filepath.Join(root, ".brigsby"))
	if _, err := service.Plan(target, replacement); err != nil {
		t.Fatalf("plan: %v", err)
	}

	if got := readFile(t, target); got != "old revision\n" {
		t.Fatalf("target after plan = %q, want original contents", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".brigsby", "recovery")); !os.IsNotExist(err) {
		t.Fatalf("recovery state after plan: err = %v, want absent", err)
	}
}

func TestApplyStoresPreimageBeforeReplacingTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")

	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	operation, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := readFile(t, target); got != "new revision\n" {
		t.Fatalf("target after apply = %q, want replacement contents", got)
	}
	if got := readFile(t, filepath.Join(operation.BundlePath, "preimage", "content")); got != "old revision\n" {
		t.Fatalf("stored preimage = %q, want original contents", got)
	}
	if _, err := os.Stat(filepath.Join(operation.BundlePath, "operation.toml")); err != nil {
		t.Fatalf("recovery manifest: %v", err)
	}
	if info, err := os.Stat(operation.BundlePath); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("recovery bundle mode = %v (err=%v), want 0700", info, err)
	}
	for _, path := range []string{filepath.Join(root, ".brigsby", "recovery"), filepath.Join(root, ".brigsby", "recovery", ".staging")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("recovery directory %s mode = %v (err=%v), want 0700", path, info, err)
		}
	}
}

func TestApplyRejectsAChangedTargetWithoutWritingRecoveryState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := os.WriteFile(target, []byte("edited after plan\n"), 0o644); err != nil {
		t.Fatalf("change target: %v", err)
	}

	if _, err := service.Apply(plan); err == nil || !strings.Contains(err.Error(), "target changed since plan") {
		t.Fatalf("apply error = %v, want stale target error", err)
	}
	if got := readFile(t, target); got != "edited after plan\n" {
		t.Fatalf("target after rejected apply = %q, want edited contents", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".brigsby", "recovery")); !os.IsNotExist(err) {
		t.Fatalf("recovery state after rejected apply: err = %v, want absent", err)
	}
}

func TestRestoreRejectsAChangedTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	operation, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(target, []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("change target: %v", err)
	}

	if _, err := service.Restore(operation.ID); err == nil || !strings.Contains(err.Error(), "target changed since apply") {
		t.Fatalf("restore error = %v, want changed target error", err)
	}
	if got := readFile(t, target); got != "user edit\n" {
		t.Fatalf("target after rejected restore = %q, want user edit", got)
	}
}

func TestRestoreReinstatesPreimageAndIsItselfRecoverable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	applied, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	restored, err := service.Restore(applied.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, target); got != "old revision\n" {
		t.Fatalf("target after restore = %q, want original contents", got)
	}
	if got := readFile(t, filepath.Join(restored.BundlePath, "preimage", "content")); got != "new revision\n" {
		t.Fatalf("restore preimage = %q, want replaced contents", got)
	}
}

func TestRecoverRestoresAnInterruptedPreparedOperation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	operation, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	manifestPath := filepath.Join(operation.BundlePath, "operation.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(strings.Replace(string(manifest), `state = "applied"`, `state = "committing"`, 1)), 0o600); err != nil {
		t.Fatalf("mark committing: %v", err)
	}

	if _, err := service.Recover(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := readFile(t, target); got != "old revision\n" {
		t.Fatalf("target after recovery = %q, want original contents", got)
	}
}

func TestRecoverDoesNotOverwriteAnEditedInterruptedTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	operation, err := service.Apply(mustPlan(t, service, target, replacement))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	markManifestState(t, operation.BundlePath, "committing")
	if err := os.WriteFile(target, []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("edit target: %v", err)
	}

	if _, err := service.Recover(); err == nil || !strings.Contains(err.Error(), "requires review") {
		t.Fatalf("recover error = %v, want review-required error", err)
	}
	if got := readFile(t, target); got != "user edit\n" {
		t.Fatalf("target after recovery = %q, want user edit", got)
	}
}

func TestRestoreRemovesAPathThatWasOriginallyAbsent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "harness", "skills", "release-notes", "SKILL.md")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	operation, err := service.Apply(mustPlan(t, service, target, replacement))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := service.Restore(operation.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target after restore: err = %v, want absent", err)
	}
}

func TestPlanRemovalAppliesAndRestoresRecoverably(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	plan, err := recovery.PlanRemoval(target)
	if err != nil {
		t.Fatalf("plan removal: %v", err)
	}
	operation, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target after removal: err = %v, want absent", err)
	}
	if _, err := service.Restore(operation.ID); err != nil {
		t.Fatalf("restore removal: %v", err)
	}
	if got := readFile(t, target); got != "old revision\n" {
		t.Fatalf("target after restore = %q", got)
	}
}

func TestRestorePreservesPreimagePermissions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))
	operation, err := service.Apply(mustPlan(t, service, target, replacement))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := service.Restore(operation.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat restored target: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("restored mode = %o, want %o", got, want)
	}
}

func TestApplyAndRestorePreserveNestedAndEmptyDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "harness", "skills", "release-notes")
	source := filepath.Join(root, "canonical", "release-notes")
	writeFile(t, target, "SKILL.md", "old revision\n")
	writeFile(t, target, "references/guide.md", "old guide\n")
	if err := os.MkdirAll(filepath.Join(target, "empty"), 0o700); err != nil {
		t.Fatalf("create empty target directory: %v", err)
	}
	writeFile(t, source, "SKILL.md", "new revision\n")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatalf("create empty source directory: %v", err)
	}
	service := recovery.New(filepath.Join(root, ".brigsby"))
	operation, err := service.Apply(mustPlan(t, service, target, source))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := service.Restore(operation.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "references", "guide.md")); got != "old guide\n" {
		t.Fatalf("restored nested file = %q", got)
	}
	info, err := os.Stat(filepath.Join(target, "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("restored empty directory: info=%v err=%v", info, err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("empty directory mode = %o, want %o", got, want)
	}
}

func TestPruneRemovesExpiredCompletedOperationsButKeepsPreparedOnes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := recovery.New(filepath.Join(root, ".brigsby"))
	target := writeFile(t, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := writeFile(t, root, "canonical/release-notes/SKILL.md", "new revision\n")
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	completed, err := service.Apply(plan)
	if err != nil {
		t.Fatalf("apply completed operation: %v", err)
	}
	prepared, err := service.Apply(mustPlan(t, service, target, replacement))
	if err != nil {
		t.Fatalf("apply prepared operation: %v", err)
	}
	markManifestState(t, prepared.BundlePath, "prepared")

	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour)
	setManifestCreatedAt(t, completed.BundlePath, old)
	setManifestCreatedAt(t, prepared.BundlePath, old)

	if _, err := service.Prune(recovery.DefaultRetention(), now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(completed.BundlePath); !os.IsNotExist(err) {
		t.Fatalf("completed bundle after prune: err = %v, want absent", err)
	}
	if _, err := os.Stat(prepared.BundlePath); err != nil {
		t.Fatalf("prepared bundle after prune: %v", err)
	}
}

func TestPruneUsesWholeOperationsForTheSizeLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := recovery.New(filepath.Join(root, ".brigsby"))
	first := applyFixture(t, service, root, "one", "old one\n", "new one\n")
	second := applyFixture(t, service, root, "two", "old two\n", "new two\n")
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	setManifestCreatedAt(t, first.BundlePath, old)

	result, err := service.Prune(recovery.Retention{MaxBytes: 1}, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !result.Exceeded {
		t.Fatal("size prune should warn when the newest operation alone exceeds the limit")
	}
	if _, err := os.Stat(first.BundlePath); !os.IsNotExist(err) {
		t.Fatalf("oldest bundle after size prune: err = %v, want absent", err)
	}
	if _, err := os.Stat(second.BundlePath); err != nil {
		t.Fatalf("newest bundle after size prune: %v", err)
	}
}

func applyFixture(t *testing.T, service recovery.Service, root, name, old, replacement string) recovery.Operation {
	t.Helper()
	target := writeFile(t, root, "harness/skills/"+name+"/SKILL.md", old)
	source := writeFile(t, root, "canonical/"+name+"/SKILL.md", replacement)
	operation, err := service.Apply(mustPlan(t, service, target, source))
	if err != nil {
		t.Fatalf("apply fixture: %v", err)
	}
	return operation
}

func mustPlan(t *testing.T, service recovery.Service, target, replacement string) recovery.Plan {
	t.Helper()
	plan, err := service.Plan(target, replacement)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func markManifestState(t *testing.T, bundlePath, state string) {
	t.Helper()
	path := filepath.Join(bundlePath, "operation.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(contents), `state = "applied"`, `state = "`+state+`"`, 1)), 0o600); err != nil {
		t.Fatalf("mark manifest state: %v", err)
	}
}

func setManifestCreatedAt(t *testing.T, bundlePath string, createdAt time.Time) {
	t.Helper()
	path := filepath.Join(bundlePath, "operation.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	updated := strings.Replace(string(contents), `created_at = "`+extractManifestValue(t, string(contents), "created_at")+`"`, `created_at = "`+createdAt.Format(time.RFC3339Nano)+`"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("set manifest timestamp: %v", err)
	}
}

func extractManifestValue(t *testing.T, contents, key string) string {
	t.Helper()
	prefix := key + ` = "`
	start := strings.Index(contents, prefix)
	if start < 0 {
		t.Fatalf("missing %s", key)
	}
	start += len(prefix)
	end := strings.Index(contents[start:], `"`)
	if end < 0 {
		t.Fatalf("unterminated %s", key)
	}
	return contents[start : start+end]
}

func writeFile(t *testing.T, root, relativePath, contents string) string {
	t.Helper()

	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
