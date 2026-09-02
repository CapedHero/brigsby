package artifact_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CapedHero/brigsby/internal/artifact"
	"github.com/CapedHero/brigsby/internal/recovery"
)

func TestStoreCapturesSkillAsSelectedDigestRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "release-notes")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: release-notes\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "references", "usage.md"), []byte("keep exact bytes\r\n"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	revision, err := artifact.NewStore(root).CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"})
	if err != nil {
		t.Fatalf("capture skill: %v", err)
	}
	if revision.Digest == "" || revision.Selector != "main/skills/release-notes" {
		t.Fatalf("revision = %#v", revision)
	}

	selected, err := artifact.NewStore(root).Selected("main/skills/release-notes")
	if err != nil {
		t.Fatalf("read selected revision: %v", err)
	}
	if selected != revision {
		t.Fatalf("selected = %#v, want %#v", selected, revision)
	}
	contents, err := os.ReadFile(filepath.Join(root, "artifacts", "main", "skills", "release-notes", "revisions", revision.Digest, "files", "references", "usage.md"))
	if err != nil {
		t.Fatalf("read captured file: %v", err)
	}
	if got, want := string(contents), "keep exact bytes\r\n"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

func TestStoreRejectsNonTextSkillBeforeWritingCanonicalState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "unsafe-skill")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "bad.bin"), []byte{0xff}, 0o644); err != nil {
		t.Fatalf("write bad content: %v", err)
	}

	if _, err := artifact.NewStore(root).CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"}); err == nil {
		t.Fatal("capture unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "brigsby.toml")); !os.IsNotExist(err) {
		t.Fatalf("canonical root was changed, stat error = %v", err)
	}
}

func TestStoreRejectsAChangedCanonicalRevisionBeforeProjection(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := artifact.NewStore(root)
	revision, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	path := filepath.Join(root, "artifacts", "main", "skills", "release-notes", "revisions", revision.Digest, "files", "SKILL.md")
	if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("tamper with canonical content: %v", err)
	}
	if _, _, err := store.SelectedFilesPath("main/skills/release-notes"); err == nil {
		t.Fatal("corrupt selected revision was accepted")
	}
}

func TestStoreSelectsAnAlreadyStoredRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := artifact.NewStore(root)
	first, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"})
	if err != nil {
		t.Fatalf("capture first: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("update source: %v", err)
	}
	second, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main", Name: "release-notes", ExplicitName: true})
	if err != nil {
		t.Fatalf("capture second: %v", err)
	}
	if second.Digest == first.Digest {
		t.Fatal("expected distinct revisions")
	}
	selected, err := store.Select("main/skills/release-notes", first.Digest)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if selected != first {
		t.Fatalf("selected = %#v, want %#v", selected, first)
	}
	current, err := artifact.NewStore(root).Selected("main/skills/release-notes")
	if err != nil {
		t.Fatalf("read selected: %v", err)
	}
	if current != first {
		t.Fatalf("current = %#v, want %#v", current, first)
	}
}

func TestStoreSelectCreatesRecoveryThatRestoresPreviousSelection(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write first source: %v", err)
	}
	store := artifact.NewStore(root)
	first, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"})
	if err != nil {
		t.Fatalf("capture first: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write second source: %v", err)
	}
	second, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main", Name: "release-notes", ExplicitName: true})
	if err != nil {
		t.Fatalf("capture second: %v", err)
	}

	if _, err := store.Select("main/skills/release-notes", first.Digest); err != nil {
		t.Fatalf("select first: %v", err)
	}
	records, err := recovery.New(root).List()
	if err != nil {
		t.Fatalf("list Recovery: %v", err)
	}
	if len(records) != 1 || records[0].State != "applied" {
		t.Fatalf("Recovery records = %#v, want one applied selection operation", records)
	}
	if _, err := recovery.New(root).Restore(records[0].ID); err != nil {
		t.Fatalf("restore selection: %v", err)
	}
	selected, err := store.Selected("main/skills/release-notes")
	if err != nil {
		t.Fatalf("read restored selection: %v", err)
	}
	if selected != second {
		t.Fatalf("selection after restore = %#v, want %#v", selected, second)
	}
}

func TestStoreRendersPrefixedSkillWithoutChangingCanonicalRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "release-notes")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	canonical := "---\nname: release-notes\ndescription: Release notes workflow\n---\n# Release notes\n"
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(canonical), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := artifact.NewStore(root)
	if _, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := store.SetPrefix("main", "mw-"); err != nil {
		t.Fatalf("set prefix: %v", err)
	}
	rendered, err := store.RenderSelectedSkill("main/skills/release-notes")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	defer rendered.Cleanup()
	if rendered.Name != "mw-release-notes" {
		t.Fatalf("rendered name = %q", rendered.Name)
	}
	renderedContents, err := os.ReadFile(filepath.Join(rendered.FilesPath, "SKILL.md"))
	if err != nil {
		t.Fatalf("read rendered Skill: %v", err)
	}
	if !strings.Contains(string(renderedContents), "name: mw-release-notes") {
		t.Fatalf("rendered Skill = %q", renderedContents)
	}
	canonicalContents, err := os.ReadFile(filepath.Join(root, "artifacts", "main", "skills", "release-notes", "revisions", rendered.Revision.Digest, "files", "SKILL.md"))
	if err != nil || string(canonicalContents) != canonical {
		t.Fatalf("canonical Skill changed = %q (err=%v)", canonicalContents, err)
	}
}

func TestStorePrefixRecoveryRestoresPreviousRenderingRule(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := artifact.NewStore(root)
	if err := store.SetPrefix("main", "mw-"); err != nil {
		t.Fatalf("set prefix: %v", err)
	}
	if got, err := store.Prefix("main"); err != nil || got != "mw-" {
		t.Fatalf("prefix after set = %q, %v; want mw-", got, err)
	}
	records, err := recovery.New(root).List()
	if err != nil {
		t.Fatalf("list Recovery: %v", err)
	}
	if len(records) != 1 || records[0].State != "applied" {
		t.Fatalf("Recovery records = %#v, want one applied prefix operation", records)
	}
	if _, err := recovery.New(root).Restore(records[0].ID); err != nil {
		t.Fatalf("restore prefix: %v", err)
	}
	if got, err := store.Prefix("main"); err != nil || got != "" {
		t.Fatalf("prefix after restore = %q, %v; want empty", got, err)
	}
}

func TestStoreSelectsAnAlreadyStoredInstructionRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "personal-instructions")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	for relative, contents := range map[string]string{"AGENTS.md": "# Personal\n", "docs/core.md": "first\n", "instructions.toml": "schema_version = 1\nindex = \"AGENTS.md\"\n\n[[document]]\nid = \"core\"\npath = \"docs/core.md\"\n"} {
		if err := os.WriteFile(filepath.Join(source, relative), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	store := artifact.NewStore(root)
	first, err := store.CaptureInstructions(source, artifact.CaptureOptions{Namespace: "main"})
	if err != nil {
		t.Fatalf("capture first: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "docs", "core.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("update source: %v", err)
	}
	second, err := store.CaptureInstructions(source, artifact.CaptureOptions{Namespace: "main", Name: "personal-instructions", ExplicitName: true})
	if err != nil {
		t.Fatalf("capture second: %v", err)
	}
	if _, err := store.Select(first.Selector, first.Digest); err != nil {
		t.Fatalf("select first: %v", err)
	}
	if selected, err := store.Selected(first.Selector); err != nil || selected != first || second == first {
		t.Fatalf("selected = %#v, %v; want %#v", selected, err, first)
	}
}

func TestStoreListsSelectedSkillsSortedBySelector(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := artifact.NewStore(root)
	for _, name := range []string{"zeta-notes", "alpha-notes"} {
		source := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatalf("create source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		if _, err := store.CaptureSkill(source, artifact.CaptureOptions{Namespace: "main"}); err != nil {
			t.Fatalf("capture %s: %v", name, err)
		}
	}

	listed, err := artifact.NewStore(root).List(artifact.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].Selector != "main/skills/alpha-notes" || listed[1].Selector != "main/skills/zeta-notes" {
		t.Fatalf("listed = %#v, want alpha then zeta", listed)
	}
	filtered, err := artifact.NewStore(root).List(artifact.ListOptions{Kind: "instructions"})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("instructions = %#v, want empty", filtered)
	}
}
