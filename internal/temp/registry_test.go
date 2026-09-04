package temp_test

import (
	"errors"
	"github.com/CapedHero/brigsby/internal/temp"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRegistryKeepsOneOffCopiesByExactID(t *testing.T) {
	r := temp.New(t.TempDir())
	x := temp.Record{ID: "codex-main-example", Harness: "codex", Paths: []string{filepath.Join(t.TempDir(), "example")}, Artifact: "main/skills/example", Revision: "sha256-deadbeef", Fingerprints: []string{"sha256-content"}, CreatedAt: "2026-09-03T00:00:00Z"}
	if err := r.Add(x); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(x); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	got, err := r.Get(x.ID)
	if err != nil || !reflect.DeepEqual(got, x) {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if err := r.Remove(x.ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.Get(x.ID)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get after Remove: %v", err)
	}
}
