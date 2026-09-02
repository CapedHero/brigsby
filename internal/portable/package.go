// Package portable creates and verifies Brigsby's closed text-only packages.
package portable

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CapedHero/brigsby/internal/artifact"
	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/pelletier/go-toml/v2"
)

const (
	maxArchiveBytes      = 8 << 20
	maxPackageMemberSize = 1 << 20
)

type member struct {
	Path   string `toml:"path"`
	Size   int64  `toml:"size"`
	Digest string `toml:"digest"`
}

type packageArtifact struct {
	Selector string   `toml:"selector"`
	Digest   string   `toml:"digest"`
	Members  []member `toml:"member"`
}

type manifest struct {
	SchemaVersion int               `toml:"schema_version"`
	Artifacts     []packageArtifact `toml:"artifact"`
}

// Result identifies a verified Package and its immutable revisions.
type Result struct {
	Digest    string
	Artifacts []artifact.Revision
}

// Create makes a deterministic text-only archive from selected Skill revisions.
func Create(root string, selectors []string, output string, replace bool, expect string) (Result, error) {
	if !filepath.IsAbs(output) {
		return Result{}, fmt.Errorf("Package output must be an absolute path")
	}
	if len(selectors) == 0 {
		return Result{}, fmt.Errorf("Package requires at least one Artifact selector")
	}
	if current, err := recovery.Fingerprint(output); err != nil {
		return Result{}, err
	} else if current != "absent" && (!replace || expect != current) {
		return Result{}, fmt.Errorf("BLOCKED: Package output exists; rerun with --replace --expect %s", current)
	}
	store := artifact.NewStore(root)
	entries := make([]packageArtifact, 0, len(selectors))
	revisions := make([]artifact.Revision, 0, len(selectors))
	archiveFiles := map[string][]byte{}
	seen := map[string]struct{}{}
	for _, selector := range selectors {
		if _, duplicate := seen[selector]; duplicate {
			continue
		}
		seen[selector] = struct{}{}
		revision, source, err := store.SelectedFilesPath(selector)
		if err != nil {
			return Result{}, fmt.Errorf("read selected Package Artifact: %w", err)
		}
		members, err := readTree(source)
		if err != nil {
			return Result{}, err
		}
		entry := packageArtifact{Selector: selector, Digest: revision.Digest}
		for _, item := range members {
			entry.Members = append(entry.Members, member{Path: item.path, Size: int64(len(item.contents)), Digest: sha(item.contents)})
			archiveFiles[packagePath(len(entries), item.path)] = item.contents
		}
		entries = append(entries, entry)
		revisions = append(revisions, revision)
	}
	encoded, err := toml.Marshal(manifest{SchemaVersion: 1, Artifacts: entries})
	if err != nil {
		return Result{}, err
	}
	temporary, err := os.CreateTemp(root, ".package-")
	if err != nil {
		return Result{}, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return Result{}, err
	}
	defer os.Remove(temporaryPath)
	if err := writeArchive(temporaryPath, encoded, archiveFiles); err != nil {
		return Result{}, err
	}
	plan, err := recovery.New(root).Plan(output, temporaryPath)
	if err != nil {
		return Result{}, err
	}
	if _, err := recovery.New(root).Apply(plan); err != nil {
		return Result{}, err
	}
	digest, err := archiveDigest(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Digest: digest, Artifacts: revisions}, nil
}

// Inspect validates every declared Package member without writing any state.
func Inspect(path, expectedDigest string) (Result, error) {
	digest, err := archiveDigest(path)
	if err != nil {
		return Result{}, err
	}
	if expectedDigest != "" && expectedDigest != digest {
		return Result{}, fmt.Errorf("Package digest differs; expected %s", expectedDigest)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	if len(contents) > maxArchiveBytes {
		return Result{}, fmt.Errorf("Package exceeds %d bytes", maxArchiveBytes)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(contents))
	if err != nil {
		return Result{}, fmt.Errorf("read Package gzip: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	files := map[string][]byte{}
	var uncompressedSize int64
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read Package member: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !safePath(header.Name) || header.Size < 0 || header.Size > maxPackageMemberSize || header.Mode&0o111 != 0 {
			return Result{}, fmt.Errorf("Package has unsafe member %q", header.Name)
		}
		if _, duplicate := files[header.Name]; duplicate {
			return Result{}, fmt.Errorf("Package has duplicate member %q", header.Name)
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return Result{}, fmt.Errorf("read Package member %q", header.Name)
		}
		if !utf8.Valid(data) {
			return Result{}, fmt.Errorf("Package member %q is not valid UTF-8 text", header.Name)
		}
		uncompressedSize += header.Size
		if uncompressedSize > maxArchiveBytes {
			return Result{}, fmt.Errorf("Package uncompressed contents exceed %d bytes", maxArchiveBytes)
		}
		files[header.Name] = data
	}
	manifestBytes, found := files["package.toml"]
	if !found {
		return Result{}, fmt.Errorf("Package has no package.toml")
	}
	var decoded manifest
	decoder := toml.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoded.SchemaVersion != 1 || len(decoded.Artifacts) == 0 {
		return Result{}, fmt.Errorf("invalid Package manifest")
	}
	expected := map[string]struct{}{"package.toml": {}}
	result := Result{Digest: digest}
	for index, entry := range decoded.Artifacts {
		if !validSelector(entry.Selector) || !validDigest(entry.Digest) || len(entry.Members) == 0 {
			return Result{}, fmt.Errorf("invalid Package Artifact declaration")
		}
		for _, declared := range entry.Members {
			name := packagePath(index, declared.Path)
			data, found := files[name]
			if !found || !safePath(declared.Path) || int64(len(data)) != declared.Size || sha(data) != declared.Digest {
				return Result{}, fmt.Errorf("Package member verification failed for %q", name)
			}
			expected[name] = struct{}{}
		}
		result.Artifacts = append(result.Artifacts, artifact.Revision{Selector: entry.Selector, Digest: entry.Digest})
	}
	if len(expected) != len(files) {
		return Result{}, fmt.Errorf("Package contains undeclared members")
	}
	return result, nil
}

// Import verifies a Package then stores its immutable Skill revisions under an
// isolated caller-chosen Namespace. It never changes main or a Harness.
func Import(root, path, namespace string) ([]artifact.Revision, error) {
	prepared, cleanup, err := prepareImport(root, path, namespace)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	imported := make([]artifact.Revision, 0, len(prepared))
	store := artifact.NewStore(root)
	for _, item := range prepared {
		revision, err := store.CaptureSkill(item.source, artifact.CaptureOptions{Namespace: namespace, Name: item.name, ExplicitName: true})
		if err != nil {
			return nil, err
		}
		imported = append(imported, revision)
	}
	return imported, nil
}

// CheckImport performs the complete read-only package-import preflight.
func CheckImport(root, path, namespace string) ([]artifact.Revision, error) {
	prepared, cleanup, err := prepareImport(root, path, namespace)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	revisions := make([]artifact.Revision, 0, len(prepared))
	for _, item := range prepared {
		revisions = append(revisions, item.revision)
	}
	return revisions, nil
}

type preparedImport struct {
	name     string
	source   string
	revision artifact.Revision
}

func prepareImport(root, path, namespace string) ([]preparedImport, func(), error) {
	if namespace == "" || strings.Contains(namespace, "/") {
		return nil, nil, fmt.Errorf("Package import requires a valid Namespace")
	}
	result, err := Inspect(path, "")
	if err != nil {
		return nil, nil, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(contents))
	if err != nil {
		return nil, nil, err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	files := map[string][]byte{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			return nil, nil, err
		}
		files[header.Name] = data
	}
	staging, err := os.MkdirTemp("", "brigsby-package-import-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	store := artifact.NewStore(root)
	prepared := make([]preparedImport, 0, len(result.Artifacts))
	seen := map[string]struct{}{}
	for index, packaged := range result.Artifacts {
		parts := strings.Split(packaged.Selector, "/")
		destination := namespace + "/skills/" + parts[2]
		if _, duplicate := seen[destination]; duplicate {
			cleanup()
			return nil, nil, fmt.Errorf("BLOCKED: Package declares duplicate destination %s", destination)
		}
		seen[destination] = struct{}{}
		if existing, err := store.Selected(destination); err == nil && existing.Digest != packaged.Digest {
			cleanup()
			return nil, nil, fmt.Errorf("BLOCKED: imported Artifact %s conflicts with selected %s; choose another Namespace", destination, existing.Digest)
		} else if err != nil && !os.IsNotExist(err) {
			cleanup()
			return nil, nil, err
		}
		source := filepath.Join(staging, parts[2])
		for archivePath, data := range files {
			prefix := fmt.Sprintf("artifacts/%03d/", index)
			if !strings.HasPrefix(archivePath, prefix) {
				continue
			}
			relative := strings.TrimPrefix(archivePath, prefix)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(source, relative)), 0o700); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.WriteFile(filepath.Join(source, relative), data, 0o600); err != nil {
				cleanup()
				return nil, nil, err
			}
		}
		revision, err := store.PlanCaptureSkill(source, artifact.CaptureOptions{Namespace: namespace, Name: parts[2], ExplicitName: true})
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if revision.Digest != packaged.Digest {
			cleanup()
			return nil, nil, fmt.Errorf("imported Revision digest differs")
		}
		prepared = append(prepared, preparedImport{name: parts[2], source: source, revision: revision})
	}
	return prepared, cleanup, nil
}

type treeMember struct {
	path     string
	contents []byte
}

func readTree(root string) ([]treeMember, error) {
	var members []treeMember
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Artifact contains unsafe member %s", relative)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		members = append(members, treeMember{path: filepath.ToSlash(relative), contents: contents})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].path < members[j].path })
	return members, nil
}

func writeArchive(path string, manifest []byte, files map[string][]byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.ModTime = time.Unix(0, 0)
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	items := make([]string, 0, len(files)+1)
	items = append(items, "package.toml")
	for name := range files {
		items = append(items, name)
	}
	sort.Strings(items)
	for _, name := range items {
		contents := manifest
		if name != "package.toml" {
			contents = files[name]
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		if _, err := tarWriter.Write(contents); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func packagePath(index int, path string) string { return fmt.Sprintf("artifacts/%03d/%s", index, path) }
func sha(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "sha256-" + hex.EncodeToString(sum[:])
}
func archiveDigest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxArchiveBytes {
		return "", fmt.Errorf("Package exceeds %d bytes", maxArchiveBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha(contents), nil
}
func validDigest(value string) bool {
	return len(value) == len("sha256-")+64 && strings.HasPrefix(value, "sha256-")
}
func validSelector(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 3 && parts[1] == "skills" && parts[0] != "" && parts[2] != ""
}
func safePath(path string) bool {
	return path != "" && !strings.HasPrefix(path, "/") && path != "." && !strings.HasPrefix(path, "../") && !strings.Contains(path, "/../")
}
