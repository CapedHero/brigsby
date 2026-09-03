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
		HarnessID:   "codex",
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
		HarnessID:   "codex",
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
	candidate := harness.Candidate{ID: "codex", Name: "codex", SkillsPath: path}
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

	if err := harness.NewRegistry(root).Unlink("codex"); err != nil {
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
	candidate := harness.Candidate{ID: "codex", Name: "codex", SkillsPath: filepath.Join(root, "home", ".agents", "skills")}
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

func TestRegistryLinkMigratesLegacyPersonalIDAndProjectionClaims(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	skillsPath := filepath.Join(root, "home", ".agents", "skills")
	legacy := harness.Candidate{ID: "codex-personal", Name: "codex", SkillsPath: skillsPath}
	registry := harness.NewRegistry(root)
	if err := registry.Link(legacy); err != nil {
		t.Fatalf("link legacy candidate: %v", err)
	}
	projection := harness.Projection{
		HarnessID:   legacy.ID,
		Path:        filepath.Join(skillsPath, "release-notes"),
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}
	if err := registry.RecordProjection(projection); err != nil {
		t.Fatalf("record legacy Projection: %v", err)
	}

	canonical := harness.Candidate{
		ID:               "codex",
		Name:             "codex",
		SkillsPath:       skillsPath,
		InstructionsPath: filepath.Join(root, "home", ".codex"),
	}
	if err := registry.Link(canonical); err != nil {
		t.Fatalf("migrate legacy candidate: %v", err)
	}

	linked, err := registry.List()
	if err != nil {
		t.Fatalf("list linked Harnesses: %v", err)
	}
	if len(linked) != 1 || linked[0] != canonical {
		t.Fatalf("linked = %#v, want %#v", linked, []harness.Candidate{canonical})
	}
	projections, err := registry.ListProjections()
	if err != nil {
		t.Fatalf("list Projections: %v", err)
	}
	projection.HarnessID = canonical.ID
	if len(projections) != 1 || projections[0] != projection {
		t.Fatalf("projections = %#v, want %#v", projections, []harness.Projection{projection})
	}
}

func TestRegistryLinkCompletesLegacyClaimMigrationAfterCanonicalLinkExists(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	skillsPath := filepath.Join(root, "home", ".claude", "skills")
	canonical := harness.Candidate{ID: "claude", Name: "claude", SkillsPath: skillsPath}
	registry := harness.NewRegistry(root)
	if err := registry.Link(canonical); err != nil {
		t.Fatalf("link canonical candidate: %v", err)
	}
	legacy := harness.Candidate{ID: "claude-personal", Name: "claude", SkillsPath: skillsPath}
	if err := registry.Link(legacy); err != nil {
		t.Fatalf("link legacy candidate: %v", err)
	}
	if err := registry.RecordProjection(harness.Projection{
		HarnessID:   legacy.ID,
		Path:        filepath.Join(skillsPath, "release-notes"),
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}); err != nil {
		t.Fatalf("record legacy Projection: %v", err)
	}

	if err := registry.Link(canonical); err != nil {
		t.Fatalf("complete legacy migration: %v", err)
	}
	linked, err := registry.List()
	if err != nil {
		t.Fatalf("list linked Harnesses: %v", err)
	}
	if len(linked) != 1 || linked[0] != canonical {
		t.Fatalf("linked = %#v, want %#v", linked, []harness.Candidate{canonical})
	}
	projections, err := registry.ListProjections()
	if err != nil {
		t.Fatalf("list Projections: %v", err)
	}
	if len(projections) != 1 || projections[0].HarnessID != canonical.ID {
		t.Fatalf("projections = %#v, want canonical claim", projections)
	}
}

func TestRegistryLinkMigratesLegacyOpenCodePersonalID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	skillsPath := filepath.Join(root, "home", ".config", "opencode", "skills")
	legacy := harness.Candidate{ID: "opencode-personal", Name: "opencode", SkillsPath: skillsPath}
	registry := harness.NewRegistry(root)
	if err := registry.Link(legacy); err != nil {
		t.Fatalf("link legacy candidate: %v", err)
	}
	if err := registry.RecordProjection(harness.Projection{
		HarnessID:   legacy.ID,
		Path:        filepath.Join(skillsPath, "release-notes"),
		Artifact:    "main/skills/release-notes",
		Revision:    "sha256-aaa",
		Fingerprint: "sha256-bbb",
	}); err != nil {
		t.Fatalf("record legacy Projection: %v", err)
	}

	canonical := harness.Candidate{ID: "opencode", Name: "opencode", SkillsPath: skillsPath}
	if err := registry.Link(canonical); err != nil {
		t.Fatalf("migrate legacy candidate: %v", err)
	}
	linked, err := registry.List()
	if err != nil {
		t.Fatalf("list linked Harnesses: %v", err)
	}
	if len(linked) != 1 || linked[0] != canonical {
		t.Fatalf("linked = %#v, want %#v", linked, []harness.Candidate{canonical})
	}
	projections, err := registry.ListProjections()
	if err != nil {
		t.Fatalf("list Projections: %v", err)
	}
	if len(projections) != 1 || projections[0].HarnessID != canonical.ID {
		t.Fatalf("projections = %#v, want canonical claim", projections)
	}
}
