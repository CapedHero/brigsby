package release

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStageExportsOnlyManifestAllowList(t *testing.T) {
	source := t.TempDir()
	for name, contents := range map[string]string{
		"README.md":   "# Public\n",
		"cmd/main.go": "package main\n",
		"AGENTS.md":   "private\n",
	} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(t.TempDir(), "public")
	report, err := Stage(source, destination, Manifest{Schema: CurrentSchema, Export: Export{Allow: []string{"README.md", "cmd"}}})
	if err != nil {
		t.Fatalf("stage release: %v", err)
	}
	if got, want := report.Files, []string{"README.md", "cmd/main.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("staged files = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(destination, "README.md")); err != nil {
		t.Fatalf("missing exported README: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("private file was exported: %v", err)
	}
}

func TestStageRefusesNonEmptyDestination(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("# Public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "existing"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Stage(source, destination, Manifest{Schema: CurrentSchema, Export: Export{Allow: []string{"README.md"}}})
	if err == nil {
		t.Fatal("Stage succeeded for non-empty destination")
	}
}

func TestPublicCIRunsOnlyOnThePublicRepository(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "public-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	if want := "if: github.repository == 'CapedHero/brigsby'"; strings.Count(workflow, want) < 2 {
		t.Fatalf("public-ci.yml must scope verify and codeql to CapedHero/brigsby; got:\n%s", workflow)
	}
}

func TestFirstPartyTeachingSkillIsTheOnlyExportableSkillPath(t *testing.T) {
	if err := validateAllowedPath("skills/brigsby"); err != nil {
		t.Fatalf("first-party Skill path rejected: %v", err)
	}
	for _, pathname := range []string{"skills/maintainer", "skills/brigsby/references"} {
		if err := validateAllowedPath(pathname); err == nil {
			t.Errorf("private Skill path %q was accepted", pathname)
		}
	}
}

func TestReleaseManifestStagesTheFirstPartyTeachingSkill(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	manifest, err := Load(filepath.Join(repositoryRoot, "release-manifest.toml"))
	if err != nil {
		t.Fatalf("load release manifest: %v", err)
	}
	if !contains(manifest.Export.Allow, "skills/brigsby") {
		t.Fatalf("release manifest does not include skills/brigsby: %#v", manifest.Export.Allow)
	}

	destination := filepath.Join(t.TempDir(), "release")
	report, err := Stage(repositoryRoot, destination, manifest)
	if err != nil {
		t.Fatalf("stage release: %v", err)
	}
	for _, pathname := range []string{"skills/brigsby/SKILL.md", "skills/brigsby/references/workflows.md"} {
		if !contains(report.Files, pathname) {
			t.Errorf("staged release does not contain %q: %#v", pathname, report.Files)
		}
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(pathname))); err != nil {
			t.Errorf("staged release file %q is unavailable: %v", pathname, err)
		}
	}
	readme, err := os.ReadFile(filepath.Join(destination, "README.md"))
	if err != nil {
		t.Fatalf("read staged public README: %v", err)
	}
	publicREADME := strings.Join(strings.Fields(string(readme)), " ")
	for _, phrase := range []string{"## AI Caller Skill", "skills/brigsby", "$brigsby"} {
		if !strings.Contains(publicREADME, phrase) {
			t.Errorf("staged public README does not contain %q", phrase)
		}
	}
	for _, phrase := range []string{
		"CapedHero/brigsby-dev",
		"private development Skills",
		"Private development workspace",
		"private-development navigation",
	} {
		if strings.Contains(publicREADME, phrase) {
			t.Errorf("staged public README leaks private-workspace guidance %q", phrase)
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
