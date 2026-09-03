// Package harness owns linked Harness registrations. Adapter-specific discovery
// remains separate from the core-owned filesystem mutation path.
package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Candidate is one concrete user-level Harness installation discovered locally.
type Candidate struct {
	ID               string `toml:"id"`
	Name             string `toml:"name"`
	SkillsPath       string `toml:"skills_path"`
	InstructionsPath string `toml:"instructions_path,omitempty"`
}

// Registry stores linked Harnesses below canonical state.
type Registry struct{ root string }

// NewRegistry creates a no-write linked-Harness registry.
func NewRegistry(root string) Registry { return Registry{root: root} }

type manifest struct {
	SchemaVersion int         `toml:"schema_version"`
	Harnesses     []Candidate `toml:"harness"`
}

type rootManifest struct {
	SchemaVersion int `toml:"schema_version"`
}

// Link records one concrete built-in Harness installation. Repeating the same
// candidate is idempotent; a conflicting ID fails closed.
func (r Registry) Link(candidate Candidate) error {
	if candidate.ID == "" || !supportedHarness(candidate.Name) || !filepath.IsAbs(candidate.SkillsPath) {
		return fmt.Errorf("invalid Harness candidate")
	}
	current, err := r.read()
	if err != nil {
		return err
	}
	legacyID, migratesLegacyPersonal := legacyPersonalID(candidate)
	canonicalIndex := -1
	legacyIndex := -1
	for index, linked := range current.Harnesses {
		if linked.ID == candidate.ID {
			if linked != candidate {
				return fmt.Errorf("linked Harness ID %q already refers to a different installation", candidate.ID)
			}
			canonicalIndex = index
		}
		if migratesLegacyPersonal && linked.ID == legacyID && linked.Name == candidate.Name && linked.SkillsPath == candidate.SkillsPath {
			legacyIndex = index
		}
	}
	if legacyIndex == -1 {
		if canonicalIndex != -1 && migratesLegacyPersonal {
			return r.migrateLegacyProjections(legacyID, candidate.ID)
		}
		if canonicalIndex != -1 {
			return nil
		}
		current.Harnesses = append(current.Harnesses, candidate)
		return r.write(current)
	}

	if canonicalIndex == -1 {
		// Keep the legacy link until the claim migration completes. If a later
		// write fails, status can still associate the old claims with this path
		// and a repeated link completes the migration.
		current.Harnesses = append(current.Harnesses, candidate)
		if err := r.write(current); err != nil {
			return err
		}
	}
	if err := r.migrateLegacyProjections(legacyID, candidate.ID); err != nil {
		return err
	}
	current.Harnesses = append(current.Harnesses[:legacyIndex], current.Harnesses[legacyIndex+1:]...)
	return r.write(current)
}

func legacyPersonalID(candidate Candidate) (string, bool) {
	if candidate.ID != candidate.Name || !supportedHarness(candidate.Name) {
		return "", false
	}
	return candidate.Name + "-personal", true
}

func (r Registry) migrateLegacyProjections(legacyID, canonicalID string) error {
	projections, err := r.readProjections()
	if err != nil {
		return err
	}
	canonicalByPath := make(map[string]Projection, len(projections.Projections))
	for _, projection := range projections.Projections {
		if projection.HarnessID == canonicalID {
			canonicalByPath[projection.Path] = projection
		}
	}
	changed := false
	kept := projections.Projections[:0]
	for _, projection := range projections.Projections {
		if projection.HarnessID != legacyID {
			kept = append(kept, projection)
			continue
		}
		if existing, found := canonicalByPath[projection.Path]; found {
			projection.HarnessID = canonicalID
			if existing != projection {
				return fmt.Errorf("legacy Projection %q conflicts with canonical Projection claim", projection.Path)
			}
			changed = true
			continue
		}
		projection.HarnessID = canonicalID
		kept = append(kept, projection)
		changed = true
	}
	if !changed {
		return nil
	}
	projections.Projections = kept
	return r.writeProjections(projections)
}

func supportedHarness(name string) bool {
	return name == "codex" || name == "claude" || name == "opencode"
}

// Unlink removes one linked installation and its Projection claims. It never
// deletes Harness files.
func (r Registry) Unlink(id string) error {
	if id == "" {
		return fmt.Errorf("linked Harness ID is required")
	}
	current, err := r.read()
	if err != nil {
		return err
	}
	kept := current.Harnesses[:0]
	found := false
	for _, linked := range current.Harnesses {
		if linked.ID == id {
			found = true
			continue
		}
		kept = append(kept, linked)
	}
	if !found {
		return fmt.Errorf("linked Harness %q was not found", id)
	}
	current.Harnesses = kept
	if err := r.write(current); err != nil {
		return err
	}
	projections, err := r.readProjections()
	if err != nil {
		return err
	}
	retained := projections.Projections[:0]
	for _, projection := range projections.Projections {
		if projection.HarnessID != id {
			retained = append(retained, projection)
		}
	}
	projections.Projections = retained
	return r.writeProjections(projections)
}

// List returns linked installations without touching the filesystem when none
// have been registered.
func (r Registry) List() ([]Candidate, error) {
	current, err := r.read()
	if err != nil {
		return nil, err
	}
	return current.Harnesses, nil
}

func (r Registry) path() string { return filepath.Join(r.root, "harnesses.toml") }

func (r Registry) read() (manifest, error) {
	contents, err := os.ReadFile(r.path())
	if os.IsNotExist(err) {
		return manifest{SchemaVersion: 1}, nil
	}
	if err != nil {
		return manifest{}, err
	}
	var current manifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&current); err != nil {
		return manifest{}, fmt.Errorf("parse linked Harness registry: %w", err)
	}
	if current.SchemaVersion != 1 {
		return manifest{}, fmt.Errorf("unsupported linked Harness registry schema %d", current.SchemaVersion)
	}
	return current, nil
}

func (r Registry) write(current manifest) error {
	contents, err := toml.Marshal(current)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(r.root, 0o700); err != nil {
		return err
	}
	if err := r.ensureCanonicalRoot(); err != nil {
		return err
	}
	temporary := r.path() + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, r.path())
}

func (r Registry) ensureCanonicalRoot() error {
	path := filepath.Join(r.root, "brigsby.toml")
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, []byte("schema_version = 1\n"), 0o600)
	}
	if err != nil {
		return err
	}
	var root rootManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("parse canonical root manifest: %w", err)
	}
	if root.SchemaVersion != 1 {
		return fmt.Errorf("unsupported canonical root schema %d", root.SchemaVersion)
	}
	return nil
}
