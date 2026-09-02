// Package recovery prepares and applies filesystem replacements with
// recoverable preimages. It owns no Harness-specific paths or rendering.
package recovery

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Service owns recovery bundles below a single canonical-state root.
type Service struct {
	root string
}

// Plan is an exact no-write description of one prospective filesystem
// replacement. Apply rechecks both fingerprints before it changes anything.
type Plan struct {
	target              string
	replacement         string
	targetFingerprint   string
	replacementChecksum string
}

// TargetFingerprint identifies the exact target state observed by Plan.
func (p Plan) TargetFingerprint() string { return p.targetFingerprint }

// ReplacementFingerprint identifies the exact replacement state observed by Plan.
func (p Plan) ReplacementFingerprint() string { return p.replacementChecksum }

// Operation identifies one on-disk Recovery bundle.
type Operation struct {
	ID         string
	BundlePath string
}

// Record is the inspectable state of one Recovery operation.
type Record struct {
	ID                     string
	State                  string
	Target                 string
	TargetFingerprint      string
	ReplacementFingerprint string
	CreatedAt              string
}

// Retention controls how long completed recovery bundles remain available.
type Retention struct {
	MaxAge   time.Duration
	MaxBytes int64
}

// PruneResult reports the recoverable operations removed by retention.
type PruneResult struct {
	Removed        []Operation
	ReclaimedBytes int64
	Exceeded       bool
}

// DefaultRetention is deliberately small because v1 recovers text-only
// projections, not arbitrary user backups.
func DefaultRetention() Retention {
	return Retention{MaxAge: 30 * 24 * time.Hour, MaxBytes: 16 << 20}
}

type manifest struct {
	SchemaVersion          int    `toml:"schema_version"`
	ID                     string `toml:"id"`
	State                  string `toml:"state"`
	Target                 string `toml:"target"`
	TargetFingerprint      string `toml:"target_fingerprint"`
	ReplacementFingerprint string `toml:"replacement_fingerprint"`
	PreimageFingerprint    string `toml:"preimage_fingerprint"`
	CreatedAt              string `toml:"created_at"`
}

// New creates a recovery service rooted at canonical state. It does not touch
// the filesystem until Apply is called.
func New(root string) Service {
	return Service{root: root}
}

// Plan calculates the current target and replacement fingerprints without
// creating recovery state or changing either path.
func (s Service) Plan(target, replacement string) (Plan, error) {
	targetFingerprint, err := fingerprint(target)
	if err != nil {
		return Plan{}, fmt.Errorf("fingerprint target: %w", err)
	}
	replacementFingerprint, err := fingerprint(replacement)
	if err != nil {
		return Plan{}, fmt.Errorf("fingerprint replacement: %w", err)
	}
	if replacementFingerprint == absentFingerprint {
		return Plan{}, fmt.Errorf("replacement does not exist: %s", replacement)
	}

	return Plan{
		target:              target,
		replacement:         replacement,
		targetFingerprint:   targetFingerprint,
		replacementChecksum: replacementFingerprint,
	}, nil
}

// PlanRemoval calculates a no-write, recovery-backed removal plan.
func PlanRemoval(target string) (Plan, error) {
	targetFingerprint, err := fingerprint(target)
	if err != nil {
		return Plan{}, fmt.Errorf("fingerprint target: %w", err)
	}
	return Plan{target: target, targetFingerprint: targetFingerprint, replacementChecksum: absentFingerprint}, nil
}

const absentFingerprint = "absent"

// Fingerprint identifies the exact current filesystem state at path. A missing
// path is reported as "absent".
func Fingerprint(path string) (string, error) {
	return fingerprint(path)
}

// Apply verifies the no-write plan, persists an exact preimage bundle, then
// replaces the target. A failed post-write verification leaves the prepared
// bundle available for restore; it never silently rolls the target back.
func (s Service) Apply(plan Plan) (Operation, error) {
	if current, err := fingerprint(plan.target); err != nil {
		return Operation{}, fmt.Errorf("recheck target: %w", err)
	} else if current != plan.targetFingerprint {
		return Operation{}, fmt.Errorf("target changed since plan")
	}
	if plan.replacement != "" {
		if current, err := fingerprint(plan.replacement); err != nil {
			return Operation{}, fmt.Errorf("recheck replacement: %w", err)
		} else if current != plan.replacementChecksum {
			return Operation{}, fmt.Errorf("replacement changed since plan")
		}
	}

	recoveryRoot := filepath.Join(s.root, "recovery")
	if err := os.MkdirAll(filepath.Join(recoveryRoot, ".staging"), 0o700); err != nil {
		return Operation{}, fmt.Errorf("create recovery staging root: %w", err)
	}
	if err := os.Chmod(recoveryRoot, 0o700); err != nil {
		return Operation{}, fmt.Errorf("secure recovery root: %w", err)
	}
	if err := os.Chmod(filepath.Join(recoveryRoot, ".staging"), 0o700); err != nil {
		return Operation{}, fmt.Errorf("secure recovery staging root: %w", err)
	}
	id, staging, err := createStaging(recoveryRoot)
	if err != nil {
		return Operation{}, err
	}
	bundle := filepath.Join(recoveryRoot, id)
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.Chmod(staging, 0o700); err != nil {
		return Operation{}, fmt.Errorf("secure recovery staging: %w", err)
	}
	if plan.replacement != "" {
		if err := copyPath(plan.replacement, filepath.Join(staging, "replacement")); err != nil {
			return Operation{}, fmt.Errorf("stage replacement: %w", err)
		}
	}
	if plan.replacement != "" {
		if staged, err := fingerprint(filepath.Join(staging, "replacement")); err != nil {
			return Operation{}, fmt.Errorf("verify staged replacement: %w", err)
		} else if staged != plan.replacementChecksum {
			return Operation{}, fmt.Errorf("staged replacement differs from plan")
		}
	}
	if plan.targetFingerprint != absentFingerprint {
		if err := copyPath(plan.target, filepath.Join(staging, "preimage", "content")); err != nil {
			return Operation{}, fmt.Errorf("capture preimage: %w", err)
		}
		if captured, err := fingerprint(filepath.Join(staging, "preimage", "content")); err != nil || captured != plan.targetFingerprint {
			return Operation{}, fmt.Errorf("captured preimage differs from plan")
		}
	}
	if err := writeManifest(staging, id, "prepared", plan, createdAt); err != nil {
		return Operation{}, err
	}
	if err := os.MkdirAll(recoveryRoot, 0o700); err != nil {
		return Operation{}, fmt.Errorf("create recovery root: %w", err)
	}
	if err := os.Rename(staging, bundle); err != nil {
		return Operation{}, fmt.Errorf("publish recovery bundle: %w", err)
	}
	if current, err := fingerprint(plan.target); err != nil || current != plan.targetFingerprint {
		return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("target changed before commit")
	}
	if err := writeManifest(bundle, id, "committing", plan, createdAt); err != nil {
		return Operation{ID: id, BundlePath: bundle}, err
	}

	if err := removePath(plan.target); err != nil {
		return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("remove target after recovery: %w", err)
	}
	if plan.replacement != "" {
		if err := copyPath(filepath.Join(bundle, "replacement"), plan.target); err != nil {
			return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("write replacement: %w", err)
		}
	}
	if current, err := fingerprint(plan.target); err != nil {
		return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("verify target after write: %w", err)
	} else if current != plan.replacementChecksum {
		return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("post-write verification failed")
	}
	if err := os.RemoveAll(filepath.Join(bundle, "replacement")); err != nil {
		return Operation{ID: id, BundlePath: bundle}, fmt.Errorf("remove staged replacement: %w", err)
	}
	if err := writeManifest(bundle, id, "applied", plan, createdAt); err != nil {
		return Operation{ID: id, BundlePath: bundle}, err
	}
	return Operation{ID: id, BundlePath: bundle}, nil
}

func createStaging(recoveryRoot string) (string, string, error) {
	for range 8 {
		id, err := newOperationID()
		if err != nil {
			return "", "", err
		}
		staging := filepath.Join(recoveryRoot, ".staging", id)
		if err := os.Mkdir(staging, 0o700); err == nil {
			return id, staging, nil
		} else if !os.IsExist(err) {
			return "", "", fmt.Errorf("create recovery staging: %w", err)
		}
	}
	return "", "", fmt.Errorf("allocate unique recovery operation ID")
}

func newOperationID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate recovery operation ID: %w", err)
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(bytes)), nil
}

// List returns Recovery operations newest first. A missing recovery root is an
// empty library.
func (s Service) List() ([]Record, error) {
	recoveryRoot := filepath.Join(s.root, "recovery")
	entries, err := os.ReadDir(recoveryRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery root: %w", err)
	}
	records := make([]Record, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".staging" {
			continue
		}
		bundle := filepath.Join(recoveryRoot, entry.Name())
		saved, err := loadManifest(bundle)
		if err != nil {
			return nil, err
		}
		if err := validateManifest(bundle, saved); err != nil {
			return nil, err
		}
		records = append(records, recordFrom(saved))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt > records[j].CreatedAt })
	return records, nil
}

func recordFrom(saved manifest) Record {
	return Record{
		ID:                     saved.ID,
		State:                  saved.State,
		Target:                 saved.Target,
		TargetFingerprint:      saved.TargetFingerprint,
		ReplacementFingerprint: saved.ReplacementFingerprint,
		CreatedAt:              saved.CreatedAt,
	}
}

// Show returns one Recovery operation after validating its bundle.
func (s Service) Show(id string) (Record, error) {
	bundle, err := s.bundlePath(id)
	if err != nil {
		return Record{}, err
	}
	saved, err := loadManifest(bundle)
	if err != nil {
		return Record{}, err
	}
	if err := validateManifest(bundle, saved); err != nil {
		return Record{}, err
	}
	return recordFrom(saved), nil
}

// Restore replaces a still-unmodified applied target with its recorded
// preimage. It uses Apply, so restoring is itself a new recovery operation.
func (s Service) Restore(id string) (Operation, error) {
	bundle, err := s.bundlePath(id)
	if err != nil {
		return Operation{}, err
	}
	saved, err := loadManifest(bundle)
	if err != nil {
		return Operation{}, err
	}
	if err := validateManifest(bundle, saved); err != nil {
		return Operation{}, err
	}
	if saved.State != "applied" {
		return Operation{}, fmt.Errorf("invalid applied recovery bundle: %s", id)
	}
	if current, err := fingerprint(saved.Target); err != nil {
		return Operation{}, fmt.Errorf("fingerprint restore target: %w", err)
	} else if current != saved.ReplacementFingerprint {
		return Operation{}, fmt.Errorf("target changed since apply")
	}
	if saved.TargetFingerprint == absentFingerprint {
		plan, err := PlanRemoval(saved.Target)
		if err != nil {
			return Operation{}, fmt.Errorf("plan restore removal: %w", err)
		}
		return s.Apply(plan)
	}
	plan, err := s.Plan(saved.Target, filepath.Join(bundle, "preimage", "content"))
	if err != nil {
		return Operation{}, fmt.Errorf("plan restore: %w", err)
	}
	return s.Apply(plan)
}

func (s Service) bundlePath(id string) (string, error) {
	parts := strings.Split(id, "-")
	if len(parts) != 2 || id == ".staging" || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid recovery operation ID: %q", id)
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		return "", fmt.Errorf("invalid recovery operation ID: %q", id)
	}
	if len(parts[1]) != 32 {
		return "", fmt.Errorf("invalid recovery operation ID: %q", id)
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", fmt.Errorf("invalid recovery operation ID: %q", id)
	}
	return filepath.Join(s.root, "recovery", id), nil
}

// Recover restores preimages from operations interrupted after their bundles
// were published but before they were marked applied.
func (s Service) Recover() ([]Operation, error) {
	recoveryRoot := filepath.Join(s.root, "recovery")
	entries, err := os.ReadDir(recoveryRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery root: %w", err)
	}
	recovered := make([]Operation, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".staging" {
			continue
		}
		bundle := filepath.Join(recoveryRoot, entry.Name())
		saved, err := loadManifest(bundle)
		if err != nil {
			return nil, err
		}
		if err := validateManifest(bundle, saved); err != nil {
			return nil, err
		}
		if saved.State == "prepared" {
			if current, err := fingerprint(saved.Target); err != nil || current != saved.TargetFingerprint {
				return nil, fmt.Errorf("prepared recovery requires review: %s", saved.ID)
			}
			saved.State = "abandoned"
			if err := writeManifestRecord(bundle, saved); err != nil {
				return nil, err
			}
			continue
		}
		if saved.State != "committing" {
			continue
		}
		current, err := fingerprint(saved.Target)
		if err != nil {
			return nil, err
		}
		if current == saved.TargetFingerprint {
			saved.State = "recovered"
			if err := writeManifestRecord(bundle, saved); err != nil {
				return nil, err
			}
			recovered = append(recovered, Operation{ID: saved.ID, BundlePath: bundle})
			continue
		}
		if current != absentFingerprint && current != saved.ReplacementFingerprint {
			return nil, fmt.Errorf("interrupted recovery requires review: %s", saved.ID)
		}
		if err := removePath(saved.Target); err != nil {
			return nil, fmt.Errorf("remove interrupted target: %w", err)
		}
		if saved.TargetFingerprint != absentFingerprint {
			if err := copyPath(filepath.Join(bundle, "preimage", "content"), saved.Target); err != nil {
				return nil, fmt.Errorf("restore interrupted target: %w", err)
			}
		}
		saved.State = "recovered"
		if err := writeManifestRecord(bundle, saved); err != nil {
			return nil, err
		}
		recovered = append(recovered, Operation{ID: saved.ID, BundlePath: bundle})
	}
	return recovered, nil
}

// Prune removes only whole completed or recovered operations. Prepared
// operations are never eligible. It first applies the age limit, then removes
// the oldest remaining eligible operations to satisfy the size limit.
func (s Service) Prune(policy Retention, now time.Time) (PruneResult, error) {
	type candidate struct {
		operation Operation
		state     string
		modified  time.Time
		bytes     int64
	}
	recoveryRoot := filepath.Join(s.root, "recovery")
	entries, err := os.ReadDir(recoveryRoot)
	if os.IsNotExist(err) {
		return PruneResult{}, nil
	}
	if err != nil {
		return PruneResult{}, err
	}
	candidates := make([]candidate, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".staging" {
			continue
		}
		bundle := filepath.Join(recoveryRoot, entry.Name())
		saved, err := loadManifest(bundle)
		if err != nil {
			return PruneResult{}, err
		}
		if err := validateManifest(bundle, saved); err != nil {
			return PruneResult{}, err
		}
		createdAt, err := time.Parse(time.RFC3339Nano, saved.CreatedAt)
		if err != nil {
			return PruneResult{}, fmt.Errorf("invalid recovery timestamp: %s", saved.ID)
		}
		bytes, err := directorySize(bundle)
		if err != nil {
			return PruneResult{}, err
		}
		candidates = append(candidates, candidate{Operation{saved.ID, bundle}, saved.State, createdAt, bytes})
	}
	result := PruneResult{}
	remove := func(value candidate) error {
		if err := os.RemoveAll(value.operation.BundlePath); err != nil {
			return err
		}
		result.Removed = append(result.Removed, value.operation)
		result.ReclaimedBytes += value.bytes
		return nil
	}
	remaining := make([]candidate, 0, len(candidates))
	for _, value := range candidates {
		if eligibleForPruning(value.state) && policy.MaxAge > 0 && now.Sub(value.modified) > policy.MaxAge {
			if err := remove(value); err != nil {
				return PruneResult{}, err
			}
			continue
		}
		remaining = append(remaining, value)
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].modified.Before(remaining[j].modified) })
	var total int64
	for _, value := range remaining {
		total += value.bytes
	}
	for total > policy.MaxBytes {
		index := -1
		for i, value := range remaining {
			if eligibleForPruning(value.state) {
				index = i
				break
			}
		}
		eligible := 0
		for _, value := range remaining {
			if eligibleForPruning(value.state) {
				eligible++
			}
		}
		if index < 0 || eligible == 1 {
			result.Exceeded = true
			break
		}
		value := remaining[index]
		if err := remove(value); err != nil {
			return PruneResult{}, err
		}
		total -= value.bytes
		remaining = append(remaining[:index], remaining[index+1:]...)
	}
	return result, nil
}

func eligibleForPruning(state string) bool {
	return state == "applied" || state == "recovered" || state == "abandoned"
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func writeManifest(bundle, id, state string, plan Plan, createdAt string) error {
	preimage := absentFingerprint
	if plan.targetFingerprint != absentFingerprint {
		preimage = plan.targetFingerprint
	}
	return writeManifestRecord(bundle, manifest{SchemaVersion: 1, ID: id, State: state, Target: plan.target, TargetFingerprint: plan.targetFingerprint, ReplacementFingerprint: plan.replacementChecksum, PreimageFingerprint: preimage, CreatedAt: createdAt})
}

func loadManifest(bundle string) (manifest, error) {
	contents, err := os.ReadFile(filepath.Join(bundle, "operation.toml"))
	if err != nil {
		return manifest{}, fmt.Errorf("read recovery manifest: %w", err)
	}
	var saved manifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return manifest{}, fmt.Errorf("parse recovery manifest: %w", err)
	}
	return saved, nil
}

func validateManifest(bundle string, saved manifest) error {
	if saved.SchemaVersion != 1 || saved.ID == "" || saved.ID != filepath.Base(bundle) || !filepath.IsAbs(saved.Target) {
		return fmt.Errorf("invalid recovery manifest: %s", bundle)
	}
	if saved.State != "prepared" && saved.State != "committing" && saved.State != "applied" && saved.State != "recovered" && saved.State != "abandoned" {
		return fmt.Errorf("invalid recovery state: %s", saved.State)
	}
	if saved.TargetFingerprint == "" || saved.ReplacementFingerprint == "" || saved.PreimageFingerprint == "" || saved.CreatedAt == "" {
		return fmt.Errorf("incomplete recovery manifest: %s", saved.ID)
	}
	if _, err := time.Parse(time.RFC3339Nano, saved.CreatedAt); err != nil {
		return fmt.Errorf("invalid recovery timestamp: %s", saved.ID)
	}
	if saved.TargetFingerprint == absentFingerprint {
		if saved.PreimageFingerprint != absentFingerprint {
			return fmt.Errorf("invalid absent preimage: %s", saved.ID)
		}
		return nil
	}
	preimage, err := fingerprint(filepath.Join(bundle, "preimage", "content"))
	if err != nil || preimage != saved.PreimageFingerprint || preimage != saved.TargetFingerprint {
		return fmt.Errorf("invalid recovery preimage: %s", saved.ID)
	}
	return nil
}

func writeManifestRecord(bundle string, saved manifest) error {
	contents := "schema_version = " + strconv.Itoa(saved.SchemaVersion) + "\n" +
		"id = " + strconv.Quote(saved.ID) + "\n" +
		"state = " + strconv.Quote(saved.State) + "\n" +
		"target = " + strconv.Quote(saved.Target) + "\n" +
		"target_fingerprint = " + strconv.Quote(saved.TargetFingerprint) + "\n" +
		"replacement_fingerprint = " + strconv.Quote(saved.ReplacementFingerprint) + "\n"
	contents += "preimage_fingerprint = " + strconv.Quote(saved.PreimageFingerprint) + "\n"
	contents += "created_at = " + strconv.Quote(saved.CreatedAt) + "\n"
	temporary := filepath.Join(bundle, "operation.toml.tmp")
	if err := os.WriteFile(temporary, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write recovery manifest: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(bundle, "operation.toml")); err != nil {
		return fmt.Errorf("publish recovery manifest: %w", err)
	}
	return nil
}

func removePath(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink is not supported: %s", source)
	}
	if info.Mode().IsRegular() {
		return copyFile(source, destination)
	}
	if !info.IsDir() {
		return fmt.Errorf("unsupported filesystem entry: %s", source)
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(destination, contents, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}

func fingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return absentFingerprint, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink is not supported: %s", path)
	}
	if info.Mode().IsRegular() {
		return fingerprintFile(path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("unsupported filesystem entry: %s", path)
	}
	return fingerprintTree(path)
}

func fingerprintFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if err := writeRecord(hash, []byte("brigsby-recovery-file-v1"), []byte(fmt.Sprintf("%o", info.Mode().Perm()))); err != nil {
		return "", err
	}
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256-%x", hash.Sum(nil)), nil
}

// ContentFingerprint identifies the text tree Brigsby projects, intentionally
// excluding permission bits. Recovery.Fingerprint remains mode-sensitive for
// recovery preconditions and exact preimage restoration; this separate value
// answers the user-facing question of whether a Skill's content drifted.
func ContentFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return absentFingerprint, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink is not supported: %s", path)
	}
	if info.Mode().IsRegular() {
		return contentFingerprintFile(path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("unsupported filesystem entry: %s", path)
	}
	return contentFingerprintTree(path)
}

func contentFingerprintFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if err := writeRecord(hash, []byte("brigsby-content-file-v1"), contents); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256-%x", hash.Sum(nil)), nil
}

func contentFingerprintTree(root string) (string, error) {
	type member struct {
		relative  string
		directory bool
	}
	var members []member
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		members = append(members, member{filepath.ToSlash(relative), entry.IsDir()})
		return nil
	}); err != nil {
		return "", err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].relative < members[j].relative })
	hash := sha256.New()
	if err := writeRecord(hash, []byte("brigsby-content-tree-v1")); err != nil {
		return "", err
	}
	for _, member := range members {
		kind := []byte("file")
		contents := []byte(nil)
		if member.directory {
			kind = []byte("directory")
		} else {
			var err error
			contents, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(member.relative)))
			if err != nil {
				return "", err
			}
		}
		if err := writeRecord(hash, kind, []byte(member.relative), contents); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sha256-%x", hash.Sum(nil)), nil
}

func fingerprintTree(root string) (string, error) {
	type member struct {
		relative  string
		directory bool
		mode      os.FileMode
	}
	members := make([]member, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		members = append(members, member{filepath.ToSlash(relative), entry.IsDir(), info.Mode().Perm()})
		return nil
	}); err != nil {
		return "", err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].relative < members[j].relative })

	hash := sha256.New()
	if err := writeRecord(hash, []byte("brigsby-recovery-tree-v1")); err != nil {
		return "", err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if err := writeRecord(hash, []byte("root-directory"), []byte(fmt.Sprintf("%o", rootInfo.Mode().Perm()))); err != nil {
		return "", err
	}
	for _, member := range members {
		kind := []byte("file")
		contents := []byte(nil)
		if member.directory {
			kind = []byte("directory")
		} else {
			var err error
			contents, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(member.relative)))
			if err != nil {
				return "", err
			}
		}
		if err := writeRecord(hash, kind, []byte(member.relative), []byte(fmt.Sprintf("%o", member.mode)), contents); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sha256-%x", hash.Sum(nil)), nil
}

func writeRecord(writer io.Writer, fields ...[]byte) error {
	for _, field := range fields {
		if _, err := io.WriteString(writer, strconv.Itoa(len(field))+":"); err != nil {
			return err
		}
		if _, err := writer.Write(field); err != nil {
			return err
		}
	}
	return nil
}
