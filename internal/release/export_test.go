package release

import (
	"os"
	"path/filepath"
	"reflect"
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
