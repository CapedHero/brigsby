// Package lifecycle owns recoverable multi-path content lifecycle batches.
package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/pelletier/go-toml/v2"
)

type Service struct{ root string }

func New(root string) Service { return Service{root: root} }

type Target struct{ Path string }
type Batch struct {
	ID    string
	Paths []string
}
type savedTarget struct {
	Path     string `toml:"path"`
	Before   string `toml:"before"`
	After    string `toml:"after"`
	Present  bool   `toml:"present"`
	Restored bool   `toml:"restored"`
}
type manifest struct {
	SchemaVersion int           `toml:"schema_version"`
	ID            string        `toml:"id"`
	State         string        `toml:"state"`
	CreatedAt     string        `toml:"created_at"`
	Target        []savedTarget `toml:"target"`
}

var validBatchID = regexp.MustCompile(`^lifecycle-[0-9]+-[0-9a-f]{16}$`)

// Apply snapshots every target before mutation. If mutate fails, the persisted
// partial batch is still independently restorable as one logical operation.
func (s Service) Apply(targets []Target, mutate func() error) (Batch, error) {
	if len(targets) == 0 {
		return Batch{}, fmt.Errorf("lifecycle batch needs targets")
	}
	id, err := batchID()
	if err != nil {
		return Batch{}, err
	}
	dir := filepath.Join(s.root, "lifecycle", id)
	if err := os.MkdirAll(filepath.Join(dir, "before"), 0o700); err != nil {
		return Batch{}, err
	}
	m := manifest{SchemaVersion: 1, ID: id, State: "prepared", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for i, target := range targets {
		if !filepath.IsAbs(target.Path) {
			return Batch{}, fmt.Errorf("lifecycle target must be absolute: %s", target.Path)
		}
		before, err := recovery.Fingerprint(target.Path)
		if err != nil {
			return Batch{}, err
		}
		entry := savedTarget{Path: target.Path, Before: before, Present: before != "absent"}
		if entry.Present {
			if err := copyPath(target.Path, filepath.Join(dir, "before", fmt.Sprintf("%03d", i))); err != nil {
				return Batch{}, err
			}
		}
		m.Target = append(m.Target, entry)
	}
	if err := writeManifest(dir, m); err != nil {
		return Batch{}, err
	}
	// Persist that mutation is about to begin before touching a target. If the
	// later completed-state write fails, recovery can still use the preimages.
	m.State = "applying"
	if err := writeManifest(dir, m); err != nil {
		return Batch{}, err
	}
	err = mutate()
	postStateKnown := true
	for i := range m.Target {
		after, fingerprintErr := recovery.Fingerprint(m.Target[i].Path)
		if fingerprintErr != nil {
			postStateKnown = false
			if err == nil {
				err = fingerprintErr
			}
		}
		m.Target[i].After = after
	}
	if !postStateKnown {
		m.State = "applying"
	} else if err != nil {
		m.State = "partial"
	} else {
		m.State = "applied"
	}
	if writeErr := writeManifest(dir, m); writeErr != nil && err == nil {
		err = writeErr
	}
	paths := make([]string, len(m.Target))
	for i, target := range m.Target {
		paths[i] = target.Path
	}
	return Batch{ID: id, Paths: paths}, err
}

func (s Service) Restore(id string) error {
	dir, err := s.batchDir(id)
	if err != nil {
		return err
	}
	m, err := readManifest(dir)
	if err != nil {
		return err
	}
	if m.ID != id {
		return fmt.Errorf("invalid lifecycle batch %s", id)
	}
	if m.State != "applied" && m.State != "partial" && m.State != "applying" && m.State != "restoring" {
		return fmt.Errorf("lifecycle batch %s is not restorable", id)
	}
	unsafeResume := m.State == "applying"
	if m.State != "restoring" && !unsafeResume {
		for _, target := range m.Target {
			current, err := recovery.Fingerprint(target.Path)
			if err != nil {
				return err
			}
			if current != target.After {
				return fmt.Errorf("BLOCKED: lifecycle target changed since batch: %s", target.Path)
			}
		}
	}
	if m.State != "restoring" {
		m.State = "restoring"
		if err := writeManifest(dir, m); err != nil {
			return err
		}
	}
	for i := range m.Target {
		target := &m.Target[i]
		current, err := recovery.Fingerprint(target.Path)
		if err != nil {
			return err
		}
		if target.Restored || current == target.Before {
			target.Restored = true
			continue
		}
		// A target can be absent only when a previous restore attempt removed it
		// before copying its saved preimage. The persisted "restoring" state makes
		// this safe to resume without treating the batch as permanently blocked.
		if !unsafeResume && current != target.After && current != "absent" {
			return fmt.Errorf("BLOCKED: lifecycle target changed during restore: %s", target.Path)
		}
		if err := os.RemoveAll(target.Path); err != nil {
			return err
		}
		if target.Present {
			if err := copyPath(filepath.Join(dir, "before", fmt.Sprintf("%03d", i)), target.Path); err != nil {
				return err
			}
		}
		target.Restored = true
		if err := writeManifest(dir, m); err != nil {
			return err
		}
	}
	m.State = "recovered"
	return writeManifest(dir, m)
}

func (s Service) Exists(id string) bool {
	dir, err := s.batchDir(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "manifest.toml"))
	return err == nil
}

func (s Service) batchDir(id string) (string, error) {
	if !validBatchID.MatchString(id) || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid lifecycle batch ID %q", id)
	}
	return filepath.Join(s.root, "lifecycle", id), nil
}

// Prune removes only completed or recovered batches. It applies age retention
// first, then removes the oldest eligible batches until the size budget fits.
func (s Service) Prune(policy recovery.Retention, now time.Time) (recovery.PruneResult, error) {
	type candidate struct {
		id      string
		state   string
		created time.Time
		bytes   int64
		dir     string
	}
	root := filepath.Join(s.root, "lifecycle")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return recovery.PruneResult{}, nil
	}
	if err != nil {
		return recovery.PruneResult{}, err
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		m, err := readManifest(dir)
		if err != nil {
			return recovery.PruneResult{}, err
		}
		created, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
		if err != nil {
			return recovery.PruneResult{}, err
		}
		bytes, err := directorySize(dir)
		if err != nil {
			return recovery.PruneResult{}, err
		}
		candidates = append(candidates, candidate{id: m.ID, state: m.State, created: created, bytes: bytes, dir: dir})
	}
	result := recovery.PruneResult{}
	remove := func(value candidate) error {
		if err := os.RemoveAll(value.dir); err != nil {
			return err
		}
		result.Removed = append(result.Removed, recovery.Operation{ID: value.id})
		result.ReclaimedBytes += value.bytes
		return nil
	}
	remaining := make([]candidate, 0, len(candidates))
	for _, value := range candidates {
		if lifecycleEligible(value.state) && policy.MaxAge > 0 && now.Sub(value.created) > policy.MaxAge {
			if err := remove(value); err != nil {
				return recovery.PruneResult{}, err
			}
			continue
		}
		remaining = append(remaining, value)
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].created.Before(remaining[j].created) })
	var total int64
	for _, value := range remaining {
		total += value.bytes
	}
	for total > policy.MaxBytes {
		index := -1
		for i, value := range remaining {
			if lifecycleEligible(value.state) {
				if index < 0 {
					index = i
				}
			}
		}
		if index < 0 {
			result.Exceeded = true
			break
		}
		value := remaining[index]
		if err := remove(value); err != nil {
			return recovery.PruneResult{}, err
		}
		total -= value.bytes
		remaining = append(remaining[:index], remaining[index+1:]...)
	}
	sort.Slice(result.Removed, func(i, j int) bool { return result.Removed[i].ID < result.Removed[j].ID })
	return result, nil
}

func lifecycleEligible(state string) bool { return state == "applied" || state == "recovered" }

func directorySize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func batchID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("lifecycle-%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(bytes)), nil
}
func writeManifest(dir string, m manifest) error {
	data, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.toml"), data, 0o600)
}
func readManifest(dir string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.toml"))
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return manifest{}, err
	}
	if m.SchemaVersion != 1 || m.ID == "" {
		return manifest{}, fmt.Errorf("invalid lifecycle batch")
	}
	return m, nil
}
func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			target := filepath.Join(destination, relative)
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.MkdirAll(target, entryInfo.Mode().Perm())
			}
			data, err := os.Open(path)
			if err != nil {
				return err
			}
			defer data.Close()
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, entryInfo.Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, data)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		})
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.WriteFile(destination, data, info.Mode().Perm())
}
