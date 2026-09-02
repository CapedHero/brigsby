package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CapedHero/brigsby/internal/harness"
)

func TestRegistryRecordsAndListsProjections(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := harness.NewRegistry(root)
	first := harness.Projection{
		HarnessID:   "codex-personal",
		Path:        filepath.Join(root, "skills", "release-notes"),
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}
	if err := registry.RecordProjection(first); err != nil {
		t.Fatalf("record projection: %v", err)
	}
	updated := first
	updated.Revision = "sha256-ccc"
	updated.Fingerprint = "sha256-ddd"
	if err := harness.NewRegistry(root).RecordProjection(updated); err != nil {
		t.Fatalf("update projection: %v", err)
	}

	listed, err := harness.NewRegistry(root).ListProjections()
	if err != nil {
		t.Fatalf("list projections: %v", err)
	}
	if len(listed) != 1 || listed[0] != updated {
		t.Fatalf("projections = %#v, want %#v", listed, []harness.Projection{updated})
	}
}

func TestRegistryForgetsProjectionByPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := harness.NewRegistry(root)
	path := filepath.Join(root, "skills", "release-notes")
	if err := registry.RecordProjection(harness.Projection{
		HarnessID:   "codex-personal",
		Path:        path,
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}); err != nil {
		t.Fatalf("record projection: %v", err)
	}
	if err := harness.NewRegistry(root).ForgetProjection(path); err != nil {
		t.Fatalf("forget projection: %v", err)
	}
	listed, err := harness.NewRegistry(root).ListProjections()
	if err != nil {
		t.Fatalf("list projections: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("projections = %#v, want empty", listed)
	}
}

func TestRegistryUnlinkRemovesAssociationAndProjectionClaims(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := harness.NewRegistry(root)
	path := filepath.Join(root, "home", ".agents", "skills")
	candidate := harness.Candidate{ID: "codex-personal", Name: "codex", SkillsPath: path}
	if err := registry.Link(candidate); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := registry.RecordProjection(harness.Projection{
		HarnessID:   candidate.ID,
		Path:        filepath.Join(path, "release-notes"),
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}); err != nil {
		t.Fatalf("record projection: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(path, "release-notes"), 0o755); err != nil {
		t.Fatalf("create harness files: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "release-notes", "SKILL.md"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write harness file: %v", err)
	}

	if err := harness.NewRegistry(root).Unlink("codex-personal"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	linked, err := harness.NewRegistry(root).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(linked) != 0 {
		t.Fatalf("linked = %#v, want empty", linked)
	}
	projections, err := harness.NewRegistry(root).ListProjections()
	if err != nil {
		t.Fatalf("list projections: %v", err)
	}
	if len(projections) != 0 {
		t.Fatalf("projections = %#v, want empty", projections)
	}
	contents, err := os.ReadFile(filepath.Join(path, "release-notes", "SKILL.md"))
	if err != nil || string(contents) != "keep\n" {
		t.Fatalf("harness files = %q (err=%v), want unchanged", contents, err)
	}
}

func TestRegistryLinksAndListsCodexInstallation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := harness.NewRegistry(root)
	candidate := harness.Candidate{ID: "codex-personal", Name: "codex", SkillsPath: filepath.Join(root, "home", ".agents", "skills")}
	if err := registry.Link(candidate); err != nil {
		t.Fatalf("link: %v", err)
	}

	linked, err := harness.NewRegistry(root).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(linked) != 1 || linked[0] != candidate {
		t.Fatalf("linked = %#v, want %#v", linked, []harness.Candidate{candidate})
	}
	contents, err := os.ReadFile(filepath.Join(root, "brigsby.toml"))
	if err != nil || string(contents) != "schema_version = 1\n" {
		t.Fatalf("canonical root manifest = %q (err=%v)", contents, err)
	}
}
