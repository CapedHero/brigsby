package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const CurrentSchema = 1

type Manifest struct {
	Schema int    `toml:"schema"`
	Export Export `toml:"export"`
}

type Export struct {
	Allow []string `toml:"allow"`
}

// StageReport is the reviewed, deterministic inventory created by one manual
// release staging operation. It contains no destination Git operations.
type StageReport struct {
	Files          []string
	ManifestDigest string
}

type stageFile struct {
	source      string
	destination string
	mode        fs.FileMode
}

func Load(pathname string) (Manifest, error) {
	file, err := os.Open(pathname)
	if err != nil {
		return Manifest{}, fmt.Errorf("open release manifest: %w", err)
	}
	defer file.Close()

	var manifest Manifest
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.Schema != CurrentSchema {
		return fmt.Errorf("release manifest schema = %d, want %d", manifest.Schema, CurrentSchema)
	}
	if len(manifest.Export.Allow) == 0 {
		return fmt.Errorf("release manifest must allow at least one public path")
	}

	seen := make(map[string]struct{}, len(manifest.Export.Allow))
	for _, allowedPath := range manifest.Export.Allow {
		if err := validateAllowedPath(allowedPath); err != nil {
			return err
		}
		if _, duplicate := seen[allowedPath]; duplicate {
			return fmt.Errorf("release manifest repeats export path %q", allowedPath)
		}
		seen[allowedPath] = struct{}{}
	}
	return nil
}

func validateAllowedPath(allowedPath string) error {
	if allowedPath == "" || path.IsAbs(allowedPath) || path.Clean(allowedPath) != allowedPath || allowedPath == "." || strings.HasPrefix(allowedPath, "../") {
		return fmt.Errorf("release manifest has invalid export path %q", allowedPath)
	}

	for _, privatePath := range []string{
		".agents",
		".codex",
		".git",
		"AGENTS.md",
		"CONTEXT.md",
		"docs/adr",
		"docs/research",
		"prototypes",
	} {
		if allowedPath == privatePath || strings.HasPrefix(allowedPath, privatePath+"/") {
			return fmt.Errorf("release manifest must not export private development path %q", allowedPath)
		}
	}
	if strings.HasPrefix(allowedPath, "skills/") && allowedPath != "skills/brigsby" {
		return fmt.Errorf("release manifest must not export private development path %q", allowedPath)
	}
	return nil
}

// Stage copies exactly manifest-allow-listed regular files from source into an
// empty destination. It never commits, pushes, or otherwise publishes a
// release; maintainers review its output before creating a public pull request.
func Stage(source, destination string, manifest Manifest) (StageReport, error) {
	if err := manifest.Validate(); err != nil {
		return StageReport{}, err
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return StageReport{}, fmt.Errorf("resolve release source: %w", err)
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return StageReport{}, fmt.Errorf("resolve release destination: %w", err)
	}
	if absSource == absDestination || within(absDestination, absSource) {
		return StageReport{}, fmt.Errorf("release destination must be outside source")
	}
	if entries, err := os.ReadDir(absDestination); err == nil && len(entries) != 0 {
		return StageReport{}, fmt.Errorf("release destination must be empty")
	} else if err != nil && !os.IsNotExist(err) {
		return StageReport{}, err
	}
	files := make([]stageFile, 0)
	for _, allowed := range manifest.Export.Allow {
		entry, err := os.Lstat(filepath.Join(absSource, filepath.FromSlash(allowed)))
		if err != nil {
			return StageReport{}, fmt.Errorf("read allowed release path %q: %w", allowed, err)
		}
		if entry.Mode()&fs.ModeSymlink != 0 || (!entry.IsDir() && !entry.Mode().IsRegular()) {
			return StageReport{}, fmt.Errorf("allowed release path %q is not a regular file or directory", allowed)
		}
		err = filepath.WalkDir(filepath.Join(absSource, filepath.FromSlash(allowed)), func(pathname string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(absSource, pathname)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return fmt.Errorf("release path %q contains a non-regular member", relative)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			files = append(files, stageFile{source: pathname, destination: relative, mode: info.Mode()})
			return nil
		})
		if err != nil {
			return StageReport{}, err
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].destination < files[right].destination })
	staging, err := os.MkdirTemp(filepath.Dir(absDestination), ".brigsby-release-")
	if err != nil {
		return StageReport{}, err
	}
	defer os.RemoveAll(staging)
	for _, file := range files {
		if err := copyRegularFile(file.source, filepath.Join(staging, filepath.FromSlash(file.destination)), file.mode); err != nil {
			return StageReport{}, err
		}
	}
	if err := os.Remove(absDestination); err != nil && !os.IsNotExist(err) {
		return StageReport{}, err
	}
	if err := os.Rename(staging, absDestination); err != nil {
		return StageReport{}, err
	}
	encoded, err := toml.Marshal(manifest)
	if err != nil {
		return StageReport{}, err
	}
	sum := sha256.Sum256(encoded)
	report := StageReport{ManifestDigest: "sha256-" + hex.EncodeToString(sum[:])}
	for _, file := range files {
		report.Files = append(report.Files, file.destination)
	}
	return report, nil
}

func copyRegularFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func within(pathname, root string) bool {
	relative, err := filepath.Rel(root, pathname)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
