// Package temp records deliberately unmanaged, one-off Harness copies.
package temp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

type Record struct {
	ID           string   `toml:"id"`
	Harness      string   `toml:"harness"`
	Paths        []string `toml:"paths"`
	Artifact     string   `toml:"artifact"`
	Revision     string   `toml:"revision"`
	Fingerprints []string `toml:"fingerprints"`
	CreatedAt    string   `toml:"created_at"`
}
type manifest struct {
	SchemaVersion int      `toml:"schema_version"`
	Temps         []Record `toml:"temp"`
}
type Registry struct{ root string }

func New(root string) Registry  { return Registry{root} }
func (r Registry) path() string { return filepath.Join(r.root, "temps.toml") }
func (r Registry) read() (manifest, error) {
	b, e := os.ReadFile(r.path())
	if os.IsNotExist(e) {
		return manifest{SchemaVersion: 1}, nil
	}
	if e != nil {
		return manifest{}, e
	}
	var m manifest
	d := toml.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&m); e != nil {
		return m, fmt.Errorf("parse temp registry: %w", e)
	}
	if m.SchemaVersion != 1 {
		return m, fmt.Errorf("unsupported temp registry schema %d", m.SchemaVersion)
	}
	return m, nil
}
func (r Registry) write(m manifest) error {
	b, e := toml.Marshal(m)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(r.root, 0700); e != nil {
		return e
	}
	if e = os.Chmod(r.root, 0700); e != nil {
		return e
	}
	return os.WriteFile(r.path(), b, 0600)
}
func (r Registry) List() ([]Record, error) {
	m, e := r.read()
	if e != nil {
		return nil, e
	}
	sort.Slice(m.Temps, func(i, j int) bool { return m.Temps[i].ID < m.Temps[j].ID })
	return m.Temps, nil
}
func (r Registry) Get(id string) (Record, error) {
	m, e := r.read()
	if e != nil {
		return Record{}, e
	}
	for _, x := range m.Temps {
		if x.ID == id {
			return x, nil
		}
	}
	return Record{}, os.ErrNotExist
}
func (r Registry) Add(x Record) error {
	if x.ID == "" || x.Harness == "" || len(x.Paths) == 0 || len(x.Paths) != len(x.Fingerprints) || x.Artifact == "" || x.Revision == "" {
		return fmt.Errorf("invalid temp record")
	}
	for _, path := range x.Paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("invalid temp record")
		}
	}
	for _, fingerprint := range x.Fingerprints {
		if fingerprint == "" {
			return fmt.Errorf("invalid temp record")
		}
	}
	m, e := r.read()
	if e != nil {
		return e
	}
	for _, v := range m.Temps {
		if v.ID == x.ID {
			return fmt.Errorf("temp ID %q already exists", x.ID)
		}
	}
	m.Temps = append(m.Temps, x)
	return r.write(m)
}
func (r Registry) Remove(id string) error {
	m, e := r.read()
	if e != nil {
		return e
	}
	out := m.Temps[:0]
	found := false
	for _, x := range m.Temps {
		if x.ID == id {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return os.ErrNotExist
	}
	m.Temps = out
	return r.write(m)
}
