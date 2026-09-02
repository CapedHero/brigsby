package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Projection records that Brigsby copied one Revision to one Harness path.
type Projection struct {
	HarnessID   string `toml:"harness_id"`
	Path        string `toml:"path"`
	Artifact    string `toml:"artifact"`
	Revision    string `toml:"revision"`
	Fingerprint string `toml:"fingerprint"`
}

type projectionManifest struct {
	SchemaVersion int          `toml:"schema_version"`
	Projections   []Projection `toml:"projection"`
}

// RecordProjection upserts ownership of one projected Harness path.
func (r Registry) RecordProjection(projection Projection) error {
	if projection.HarnessID == "" || !filepath.IsAbs(projection.Path) || projection.Artifact == "" || projection.Revision == "" || projection.Fingerprint == "" {
		return fmt.Errorf("invalid Projection record")
	}
	current, err := r.readProjections()
	if err != nil {
		return err
	}
	replaced := false
	for index, existing := range current.Projections {
		if existing.HarnessID == projection.HarnessID && existing.Path == projection.Path {
			current.Projections[index] = projection
			replaced = true
			break
		}
	}
	if !replaced {
		current.Projections = append(current.Projections, projection)
	}
	return r.writeProjections(current)
}

// ForgetProjection drops ownership of one Harness path. Missing rows are a no-op.
func (r Registry) ForgetProjection(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("invalid Projection path")
	}
	current, err := r.readProjections()
	if err != nil {
		return err
	}
	kept := current.Projections[:0]
	for _, existing := range current.Projections {
		if existing.Path != path {
			kept = append(kept, existing)
		}
	}
	current.Projections = kept
	return r.writeProjections(current)
}

// ListProjections returns recorded Projections without creating canonical state.
func (r Registry) ListProjections() ([]Projection, error) {
	current, err := r.readProjections()
	if err != nil {
		return nil, err
	}
	return current.Projections, nil
}

func (r Registry) projectionsPath() string { return filepath.Join(r.root, "projections.toml") }

func (r Registry) readProjections() (projectionManifest, error) {
	contents, err := os.ReadFile(r.projectionsPath())
	if os.IsNotExist(err) {
		return projectionManifest{SchemaVersion: 1}, nil
	}
	if err != nil {
		return projectionManifest{}, err
	}
	var current projectionManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&current); err != nil {
		return projectionManifest{}, fmt.Errorf("parse Projection ledger: %w", err)
	}
	if current.SchemaVersion != 1 {
		return projectionManifest{}, fmt.Errorf("unsupported Projection ledger schema %d", current.SchemaVersion)
	}
	return current, nil
}

func (r Registry) writeProjections(current projectionManifest) error {
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
	temporary := r.projectionsPath() + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, r.projectionsPath())
}
