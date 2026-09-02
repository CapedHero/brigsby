// Package artifact owns immutable canonical Artifact revisions.
package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/pelletier/go-toml/v2"
)

const (
	maxFiles = 100
	maxBytes = 1 << 20
)

// Revision identifies one immutable captured payload.
type Revision struct {
	Selector string
	Digest   string
}

// Origin identifies the imported revision from which a main selection was
// promoted. Empty fields mean the selection was captured locally.
type Origin struct {
	Selector string
	Revision string
}

// CaptureOptions selects the destination identity.
type CaptureOptions struct {
	Namespace    string
	Name         string
	ExplicitName bool
}

// Store owns a canonical BRIGSBY_HOME tree.
type Store struct{ root string }

// NewStore creates a no-write canonical Artifact store.
func NewStore(root string) Store { return Store{root: root} }

type rootManifest struct {
	SchemaVersion int `toml:"schema_version"`
}

type artifactManifest struct {
	SchemaVersion  int    `toml:"schema_version"`
	Namespace      string `toml:"namespace"`
	Kind           string `toml:"kind"`
	Name           string `toml:"name"`
	Selected       string `toml:"selected_revision"`
	OriginSelector string `toml:"origin_selector,omitempty"`
	OriginRevision string `toml:"origin_revision,omitempty"`
}

type revisionManifest struct {
	SchemaVersion int      `toml:"schema_version"`
	Kind          string   `toml:"kind"`
	Digest        string   `toml:"digest"`
	Files         []string `toml:"files"`
}

type namespaceManifest struct {
	SchemaVersion int    `toml:"schema_version"`
	Namespace     string `toml:"namespace"`
	Prefix        string `toml:"prefix"`
}

// RenderedSkill is one temporary, target-facing Skill tree. Call Cleanup when
// the Recovery-backed projection plan is no longer needed.
type RenderedSkill struct {
	Revision  Revision
	Name      string
	FilesPath string
	Cleanup   func()
}

type instructionSourceManifest struct {
	SchemaVersion int                   `toml:"schema_version"`
	Index         string                `toml:"index"`
	Documents     []instructionDocument `toml:"document"`
}

type instructionDocument struct {
	ID         string   `toml:"id"`
	Path       string   `toml:"path"`
	Harness    string   `toml:"harness,omitempty"`
	Supersedes []string `toml:"supersedes,omitempty"`
}

// RenderedInstructions contains the native root file and copied document tree
// for one Harness. Call Cleanup once its projection plans are finished.
type RenderedInstructions struct {
	Revision Revision
	RootPath string
	TreePath string
	Cleanup  func()
}

type member struct {
	relative string
	bytes    []byte
	mode     fs.FileMode
}

// CaptureSkill validates and copies a local Skill tree into an immutable,
// digest-addressed canonical revision. No canonical path is created until the
// complete source tree has passed validation.
func (s Store) CaptureSkill(source string, options CaptureOptions) (Revision, error) {
	revision, members, err := s.planCaptureSkill(source, options)
	if err != nil {
		return Revision{}, err
	}
	parts := strings.Split(revision.Selector, "/")
	if err := s.commitKind("skills", parts[0], parts[2], revision, members, options.ExplicitName); err != nil {
		return Revision{}, err
	}
	return revision, nil
}

// PlanCaptureSkill validates a Skill capture without writing canonical state.
func (s Store) PlanCaptureSkill(source string, options CaptureOptions) (Revision, error) {
	revision, _, err := s.planCaptureSkill(source, options)
	return revision, err
}

func (s Store) planCaptureSkill(source string, options CaptureOptions) (Revision, []member, error) {
	if !filepath.IsAbs(s.root) {
		return Revision{}, nil, fmt.Errorf("canonical root must be an absolute path")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return Revision{}, nil, fmt.Errorf("resolve source: %w", err)
	}
	if within(absSource, s.root) {
		return Revision{}, nil, fmt.Errorf("source may not be inside BRIGSBY_HOME")
	}
	members, err := readSkill(absSource)
	if err != nil {
		return Revision{}, nil, err
	}
	namespace := options.Namespace
	if namespace == "" {
		namespace = "main"
	}
	name := options.Name
	if name == "" {
		name = filepath.Base(absSource)
	}
	if !kebab(namespace) || !kebab(name) {
		return Revision{}, nil, fmt.Errorf("namespace and Skill name must be lower-case ASCII kebab-case")
	}
	digest := digest("skills", members)
	selector := namespace + "/skills/" + name
	revision := Revision{Selector: selector, Digest: digest}
	return revision, members, nil
}

// CaptureInstructions validates a structured global Instruction set and stores
// it as an immutable digest Revision.
func (s Store) CaptureInstructions(source string, options CaptureOptions) (Revision, error) {
	if !filepath.IsAbs(s.root) {
		return Revision{}, fmt.Errorf("canonical root must be an absolute path")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return Revision{}, fmt.Errorf("resolve source: %w", err)
	}
	if within(absSource, s.root) {
		return Revision{}, fmt.Errorf("source may not be inside BRIGSBY_HOME")
	}
	members, _, err := readInstructions(absSource)
	if err != nil {
		return Revision{}, err
	}
	namespace := options.Namespace
	if namespace == "" {
		namespace = "main"
	}
	name := options.Name
	if name == "" {
		name = filepath.Base(absSource)
	}
	if !kebab(namespace) || !kebab(name) {
		return Revision{}, fmt.Errorf("namespace and Instruction name must be lower-case ASCII kebab-case")
	}
	digest := digest("instructions", members)
	revision := Revision{Selector: namespace + "/instructions/" + name, Digest: digest}
	if err := s.commitKind("instructions", namespace, name, revision, members, options.ExplicitName); err != nil {
		return Revision{}, err
	}
	return revision, nil
}

// ListOptions filters the canonical Artifact inventory.
type ListOptions struct {
	Namespace string
	Kind      string
}

// List returns selected Revisions for stored Artifacts, sorted by selector.
// A missing artifacts tree is an empty library.
func (s Store) List(options ListOptions) ([]Revision, error) {
	if options.Namespace != "" && !kebab(options.Namespace) {
		return nil, fmt.Errorf("namespace must be lower-case ASCII kebab-case")
	}
	if options.Kind != "" && options.Kind != "skills" && options.Kind != "instructions" {
		return nil, fmt.Errorf("unsupported Artifact kind %q", options.Kind)
	}
	artifactsRoot := filepath.Join(s.root, "artifacts")
	namespaces, err := os.ReadDir(artifactsRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Artifacts: %w", err)
	}
	var revisions []Revision
	for _, namespace := range namespaces {
		if !namespace.IsDir() || options.Namespace != "" && namespace.Name() != options.Namespace {
			continue
		}
		kinds, err := os.ReadDir(filepath.Join(artifactsRoot, namespace.Name()))
		if err != nil {
			return nil, fmt.Errorf("read Namespace: %w", err)
		}
		for _, kind := range kinds {
			if !kind.IsDir() || options.Kind != "" && kind.Name() != options.Kind {
				continue
			}
			if kind.Name() != "skills" && kind.Name() != "instructions" {
				continue
			}
			names, err := os.ReadDir(filepath.Join(artifactsRoot, namespace.Name(), kind.Name()))
			if err != nil {
				return nil, fmt.Errorf("read Artifact kind: %w", err)
			}
			for _, name := range names {
				if !name.IsDir() {
					continue
				}
				selector := namespace.Name() + "/" + kind.Name() + "/" + name.Name()
				revision, err := s.Selected(selector)
				if err != nil {
					return nil, fmt.Errorf("read Artifact %s: %w", selector, err)
				}
				revisions = append(revisions, revision)
			}
		}
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Selector < revisions[j].Selector })
	return revisions, nil
}

// Selected returns the current selected Revision for a canonical selector.
func (s Store) Selected(selector string) (Revision, error) {
	manifest, err := s.artifactManifest(selector)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Selector: selector, Digest: manifest.Selected}, nil
}

// Origin returns the provenance recorded for the current selected revision.
func (s Store) Origin(selector string) (Origin, error) {
	manifest, err := s.artifactManifest(selector)
	if err != nil {
		return Origin{}, err
	}
	return Origin{Selector: manifest.OriginSelector, Revision: manifest.OriginRevision}, nil
}

func (s Store) artifactManifest(selector string) (artifactManifest, error) {
	parts := strings.Split(selector, "/")
	if len(parts) != 3 || (parts[1] != "skills" && parts[1] != "instructions") || !kebab(parts[0]) || !kebab(parts[2]) {
		return artifactManifest{}, fmt.Errorf("invalid Artifact selector %q", selector)
	}
	contents, err := os.ReadFile(filepath.Join(s.root, "artifacts", parts[0], parts[1], parts[2], "artifact.toml"))
	if err != nil {
		return artifactManifest{}, err
	}
	var manifest artifactManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return artifactManifest{}, fmt.Errorf("parse Artifact manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Namespace != parts[0] || manifest.Kind != parts[1] || manifest.Name != parts[2] || !strings.HasPrefix(manifest.Selected, "sha256-") || (manifest.OriginSelector == "") != (manifest.OriginRevision == "") {
		return artifactManifest{}, fmt.Errorf("invalid Artifact manifest for %q", selector)
	}
	return manifest, nil
}

// SelectedFilesPath returns the immutable runtime tree for the selected Skill.
// Callers must treat it as read-only canonical content.
func (s Store) SelectedFilesPath(selector string) (Revision, string, error) {
	revision, err := s.Selected(selector)
	if err != nil {
		return Revision{}, "", err
	}
	if err := s.verifySkillRevision(selector, revision.Digest); err != nil {
		return Revision{}, "", err
	}
	parts := strings.Split(selector, "/")
	return revision, filepath.Join(s.root, "artifacts", parts[0], "skills", parts[2], "revisions", revision.Digest, "files"), nil
}

// RenderSelectedInstructions produces a native root plus a complete copied
// document tree for one Harness without altering canonical bytes.
func (s Store) RenderSelectedInstructions(selector, harnessName string) (RenderedInstructions, error) {
	revision, filesPath, err := s.selectedInstructionFilesPath(selector)
	if err != nil {
		return RenderedInstructions{}, err
	}
	members, manifest, err := readInstructions(filesPath)
	if err != nil {
		return RenderedInstructions{}, err
	}
	parts := strings.Split(selector, "/")
	selected := selectedInstructionDocuments(manifest, harnessName)
	directory, err := os.MkdirTemp(s.root, ".render-instructions-")
	if err != nil {
		return RenderedInstructions{}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	tree := filepath.Join(directory, "brigsby", parts[2])
	for _, document := range selected {
		for _, member := range members {
			if member.relative != document.Path {
				continue
			}
			destination := filepath.Join(tree, filepath.FromSlash(member.relative))
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				cleanup()
				return RenderedInstructions{}, err
			}
			if err := os.WriteFile(destination, member.bytes, member.mode.Perm()); err != nil {
				cleanup()
				return RenderedInstructions{}, err
			}
		}
	}
	var index []byte
	for _, member := range members {
		if member.relative == manifest.Index {
			index = member.bytes
			break
		}
	}
	var rendered strings.Builder
	rendered.Write(index)
	if len(index) > 0 && index[len(index)-1] != '\n' {
		rendered.WriteByte('\n')
	}
	rendered.WriteString("\n## Brigsby Instruction documents\n")
	for _, document := range selected {
		fmt.Fprintf(&rendered, "- Read `brigsby/%s/%s`.\n", parts[2], document.Path)
	}
	root := filepath.Join(directory, "root.md")
	if err := os.WriteFile(root, []byte(rendered.String()), 0o600); err != nil {
		cleanup()
		return RenderedInstructions{}, err
	}
	return RenderedInstructions{Revision: revision, RootPath: root, TreePath: tree, Cleanup: cleanup}, nil
}

// SetPrefix changes one Namespace's target-facing Skill prefix through
// Recovery. A non-empty prefix must end in a hyphen so rendered names remain
// unambiguous kebab-case identifiers.
func (s Store) SetPrefix(namespace, prefix string) error {
	if err := ValidatePrefix(namespace, prefix); err != nil {
		return err
	}
	if err := s.ensureRoot(); err != nil {
		return err
	}
	directory := filepath.Join(s.root, "artifacts", namespace)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	replacement, err := s.namespaceReplacement(namespace, prefix)
	if err != nil {
		return err
	}
	defer os.Remove(replacement)
	plan, err := recovery.New(s.root).Plan(filepath.Join(directory, "namespace.toml"), replacement)
	if err != nil {
		return fmt.Errorf("plan Namespace prefix: %w", err)
	}
	if _, err := recovery.New(s.root).Apply(plan); err != nil {
		return fmt.Errorf("apply Namespace prefix: %w", err)
	}
	return nil
}

// ValidatePrefix validates a persistent Namespace rendering rule without
// changing canonical state.
func ValidatePrefix(namespace, prefix string) error {
	if !kebab(namespace) || !validPrefix(prefix) {
		return fmt.Errorf("invalid Namespace prefix")
	}
	return nil
}

// Prefix returns a Namespace's target-facing prefix. A missing declaration
// means rendered Skill names remain unchanged.
func (s Store) Prefix(namespace string) (string, error) {
	if !kebab(namespace) {
		return "", fmt.Errorf("invalid Namespace %q", namespace)
	}
	contents, err := os.ReadFile(filepath.Join(s.root, "artifacts", namespace, "namespace.toml"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var manifest namespaceManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return "", fmt.Errorf("parse Namespace manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Namespace != namespace || !validPrefix(manifest.Prefix) {
		return "", fmt.Errorf("invalid Namespace manifest for %q", namespace)
	}
	return manifest.Prefix, nil
}

// RenderSelectedSkill returns copied target-facing files when its Namespace
// has a prefix. The rendered name matches both the target directory and the
// required frontmatter name. Without a prefix it returns canonical files.
func (s Store) RenderSelectedSkill(selector string) (RenderedSkill, error) {
	revision, filesPath, err := s.SelectedFilesPath(selector)
	if err != nil {
		return RenderedSkill{}, err
	}
	parts := strings.Split(selector, "/")
	prefix, err := s.Prefix(parts[0])
	if err != nil {
		return RenderedSkill{}, err
	}
	if prefix == "" {
		return RenderedSkill{Revision: revision, Name: parts[2], FilesPath: filesPath, Cleanup: func() {}}, nil
	}
	name := prefix + parts[2]
	if !kebab(name) {
		return RenderedSkill{}, fmt.Errorf("rendered Skill name %q is invalid", name)
	}
	members, err := readSkill(filesPath)
	if err != nil {
		return RenderedSkill{}, err
	}
	for index := range members {
		if members[index].relative != "SKILL.md" {
			continue
		}
		contents, err := renderFrontmatterName(members[index].bytes, parts[2], name)
		if err != nil {
			return RenderedSkill{}, err
		}
		members[index].bytes = contents
		break
	}
	directory, err := os.MkdirTemp(s.root, ".render-")
	if err != nil {
		return RenderedSkill{}, err
	}
	for _, member := range members {
		destination := filepath.Join(directory, filepath.FromSlash(member.relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			os.RemoveAll(directory)
			return RenderedSkill{}, err
		}
		if err := os.WriteFile(destination, member.bytes, member.mode.Perm()); err != nil {
			os.RemoveAll(directory)
			return RenderedSkill{}, err
		}
	}
	return RenderedSkill{Revision: revision, Name: name, FilesPath: directory, Cleanup: func() { _ = os.RemoveAll(directory) }}, nil
}

// VerifyRevision checks that a stored Skill Revision matches its digest.
func (s Store) VerifyRevision(selector, revisionDigest string) (Revision, error) {
	if !strings.HasPrefix(revisionDigest, "sha256-") || len(revisionDigest) != len("sha256-")+64 {
		return Revision{}, fmt.Errorf("invalid Revision digest %q", revisionDigest)
	}
	if _, err := s.Selected(selector); err != nil {
		return Revision{}, err
	}
	parts := strings.Split(selector, "/")
	var err error
	if len(parts) == 3 && parts[1] == "instructions" {
		err = s.verifyInstructionRevision(selector, revisionDigest)
	} else {
		err = s.verifySkillRevision(selector, revisionDigest)
	}
	if err != nil {
		return Revision{}, err
	}
	return Revision{Selector: selector, Digest: revisionDigest}, nil
}

// Select verifies an already stored Revision and makes it selected through a
// Recovery-backed replacement of only the mutable Artifact pointer. It does
// not recopy Skill content.
func (s Store) Select(selector, revisionDigest string) (Revision, error) {
	revision, err := s.VerifyRevision(selector, revisionDigest)
	if err != nil {
		return Revision{}, err
	}
	current, err := s.Selected(selector)
	if err != nil {
		return Revision{}, err
	}
	if current.Digest == revisionDigest {
		return current, nil
	}
	parts := strings.Split(selector, "/")
	artifactDir := filepath.Join(s.root, "artifacts", parts[0], parts[1], parts[2])
	replacement, err := s.selectionReplacement(parts[0], parts[1], parts[2], revisionDigest)
	if err != nil {
		return Revision{}, err
	}
	defer os.Remove(replacement)
	plan, err := recovery.New(s.root).Plan(filepath.Join(artifactDir, "artifact.toml"), replacement)
	if err != nil {
		return Revision{}, fmt.Errorf("plan Artifact selection: %w", err)
	}
	if _, err := recovery.New(s.root).Apply(plan); err != nil {
		return Revision{}, fmt.Errorf("apply Artifact selection: %w", err)
	}
	selected, err := s.Selected(selector)
	if err != nil {
		return Revision{}, fmt.Errorf("verify selected Artifact: %w", err)
	}
	if selected != revision {
		return Revision{}, fmt.Errorf("selected Artifact differs after apply")
	}
	return revision, nil
}

// Promote copies one verified imported Skill revision into main while retaining
// the source revision unchanged in its imported Namespace.
func (s Store) Promote(selector, revisionDigest string) (Revision, error) {
	parts := strings.Split(selector, "/")
	if len(parts) != 3 || parts[0] == "main" || parts[1] != "skills" {
		return Revision{}, fmt.Errorf("promotion requires an imported Skill selector")
	}
	if _, err := s.VerifyRevision(selector, revisionDigest); err != nil {
		return Revision{}, err
	}
	source := filepath.Join(s.root, "artifacts", parts[0], "skills", parts[2], "revisions", revisionDigest, "files")
	members, err := readSkill(source)
	if err != nil {
		return Revision{}, err
	}
	revision := Revision{Selector: "main/skills/" + parts[2], Digest: revisionDigest}
	artifactDir, err := s.storeKind("skills", "main", parts[2], revision, members, true)
	if err != nil {
		return Revision{}, err
	}
	replacement, err := s.selectionReplacement("main", "skills", parts[2], revision.Digest, selector, revisionDigest)
	if err != nil {
		return Revision{}, err
	}
	defer os.Remove(replacement)
	plan, err := recovery.New(s.root).Plan(filepath.Join(artifactDir, "artifact.toml"), replacement)
	if err != nil {
		return Revision{}, fmt.Errorf("plan promoted Artifact selection: %w", err)
	}
	if _, err := recovery.New(s.root).Apply(plan); err != nil {
		return Revision{}, fmt.Errorf("apply promoted Artifact selection: %w", err)
	}
	if selected, err := s.Selected(revision.Selector); err != nil {
		return Revision{}, fmt.Errorf("verify promoted Artifact selection: %w", err)
	} else if selected != revision {
		return Revision{}, fmt.Errorf("promoted Artifact selection differs after apply")
	}
	return revision, nil
}

func (s Store) selectionReplacement(namespace, kind, name, selected string, origin ...string) (string, error) {
	manifest := artifactManifest{SchemaVersion: 1, Namespace: namespace, Kind: kind, Name: name, Selected: selected}
	if len(origin) == 2 {
		manifest.OriginSelector = origin[0]
		manifest.OriginRevision = origin[1]
	}
	encoded, err := toml.Marshal(manifest)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(s.root, ".artifact-select-")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		os.Remove(path)
		return "", err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s Store) namespaceReplacement(namespace, prefix string) (string, error) {
	encoded, err := toml.Marshal(namespaceManifest{SchemaVersion: 1, Namespace: namespace, Prefix: prefix})
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(s.root, ".namespace-prefix-")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		os.Remove(path)
		return "", err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s Store) verifySkillRevision(selector, revisionDigest string) error {
	parts := strings.Split(selector, "/")
	if len(parts) != 3 || parts[1] != "skills" {
		return fmt.Errorf("invalid Skill selector %q", selector)
	}
	path := filepath.Join(s.root, "artifacts", parts[0], "skills", parts[2], "revisions", revisionDigest, "files")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect Revision files: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Revision files are not a directory")
	}
	manifestContents, err := os.ReadFile(filepath.Join(filepath.Dir(path), "revision.toml"))
	if err != nil {
		return fmt.Errorf("read Revision manifest: %w", err)
	}
	var manifest revisionManifest
	decoder := toml.NewDecoder(bytes.NewReader(manifestContents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("parse Revision manifest: %w", err)
	}
	members, err := readSkill(path)
	if err != nil {
		return fmt.Errorf("validate Revision files: %w", err)
	}
	paths := make([]string, len(members))
	for index, item := range members {
		paths[index] = item.relative
	}
	if manifest.SchemaVersion != 1 || manifest.Kind != "skills" || manifest.Digest != revisionDigest || !sameStrings(manifest.Files, paths) || digest("skills", members) != revisionDigest {
		return fmt.Errorf("Revision integrity check failed for %q", selector)
	}
	return nil
}

func (s Store) selectedInstructionFilesPath(selector string) (Revision, string, error) {
	parts := strings.Split(selector, "/")
	if len(parts) != 3 || parts[1] != "instructions" {
		return Revision{}, "", fmt.Errorf("invalid Instruction selector %q", selector)
	}
	revision, err := s.Selected(selector)
	if err != nil {
		return Revision{}, "", err
	}
	path := filepath.Join(s.root, "artifacts", parts[0], "instructions", parts[2], "revisions", revision.Digest, "files")
	if err := s.verifyInstructionRevision(selector, revision.Digest); err != nil {
		return Revision{}, "", err
	}
	return revision, path, nil
}

func (s Store) verifyInstructionRevision(selector, revisionDigest string) error {
	parts := strings.Split(selector, "/")
	if len(parts) != 3 || parts[1] != "instructions" {
		return fmt.Errorf("invalid Instruction selector %q", selector)
	}
	path := filepath.Join(s.root, "artifacts", parts[0], "instructions", parts[2], "revisions", revisionDigest, "files")
	members, _, err := readInstructions(path)
	if err != nil {
		return fmt.Errorf("validate Revision files: %w", err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(path), "revision.toml"))
	if err != nil {
		return err
	}
	var manifest revisionManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	paths := make([]string, len(members))
	for index, item := range members {
		paths[index] = item.relative
	}
	if manifest.SchemaVersion != 1 || manifest.Kind != "instructions" || manifest.Digest != revisionDigest || !sameStrings(manifest.Files, paths) || digest("instructions", members) != revisionDigest {
		return fmt.Errorf("Revision integrity check failed for %q", selector)
	}
	return nil
}

func (s Store) commitKind(kind, namespace, name string, revision Revision, members []member, explicitName bool) error {
	artifactDir, err := s.storeKind(kind, namespace, name, revision, members, explicitName)
	if err != nil {
		return err
	}
	return s.writeArtifact(kind, artifactDir, namespace, name, revision.Digest)
}

// storeKind creates or verifies an immutable revision but does not alter the
// mutable selected-revision pointer.
func (s Store) storeKind(kind, namespace, name string, revision Revision, members []member, explicitName bool) (string, error) {
	if err := s.ensureRoot(); err != nil {
		return "", err
	}
	artifactDir := filepath.Join(s.root, "artifacts", namespace, kind, name)
	if existing, err := s.Selected(revision.Selector); err == nil && existing.Digest != revision.Digest && !explicitName {
		return "", fmt.Errorf("Artifact %q already has a different selected revision; repeat with --name %q to add to its history", revision.Selector, name)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(artifactDir, "revisions"), 0o700); err != nil {
		return "", err
	}
	revisionDir := filepath.Join(artifactDir, "revisions", revision.Digest)
	if _, err := os.Stat(revisionDir); err == nil {
		return artifactDir, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Join(artifactDir, "revisions"), ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	filesDir := filepath.Join(staging, "files")
	for _, item := range members {
		destination := filepath.Join(filesDir, filepath.FromSlash(item.relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(destination, item.bytes, item.mode.Perm()); err != nil {
			return "", err
		}
	}
	paths := make([]string, len(members))
	for i, item := range members {
		paths[i] = item.relative
	}
	encoded, err := toml.Marshal(revisionManifest{SchemaVersion: 1, Kind: kind, Digest: revision.Digest, Files: paths})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, "revision.toml"), encoded, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(staging, revisionDir); err != nil {
		return "", err
	}
	return artifactDir, nil
}

func (s Store) ensureRoot() error {
	manifestPath := filepath.Join(s.root, "brigsby.toml")
	contents, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(s.root, 0o700); err != nil {
			return err
		}
		return os.WriteFile(manifestPath, []byte("schema_version = 1\n"), 0o600)
	}
	if err != nil {
		return err
	}
	var manifest rootManifest
	decoder := toml.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.SchemaVersion != 1 {
		if err != nil {
			return fmt.Errorf("parse canonical root manifest: %w", err)
		}
		return fmt.Errorf("unsupported canonical root schema %d", manifest.SchemaVersion)
	}
	return nil
}

func (s Store) writeArtifact(kind, dir, namespace, name, selected string) error {
	encoded, err := toml.Marshal(artifactManifest{SchemaVersion: 1, Namespace: namespace, Kind: kind, Name: name, Selected: selected})
	if err != nil {
		return err
	}
	temporary := filepath.Join(dir, "artifact.toml.tmp")
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, "artifact.toml"))
}

func readSkill(source string) ([]member, error) {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Skill source must be a directory: %s", source)
	}
	var members []member
	total := 0
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == ".DS_Store" {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Skill source contains symlink %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Skill source contains non-regular file %s", relative)
		}
		if len(members) == maxFiles {
			return fmt.Errorf("Skill source exceeds %d files", maxFiles)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(contents) {
			return fmt.Errorf("Skill source contains non-UTF-8 file %s", relative)
		}
		total += len(contents)
		if total > maxBytes {
			return fmt.Errorf("Skill source exceeds %d bytes", maxBytes)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		members = append(members, member{relative: filepath.ToSlash(relative), bytes: contents, mode: info.Mode()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].relative < members[j].relative })
	if len(members) == 0 || members[0].relative != "SKILL.md" {
		found := false
		for _, item := range members {
			if item.relative == "SKILL.md" {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("Skill source must contain root-level SKILL.md")
		}
	}
	return members, nil
}

func readInstructions(source string) ([]member, instructionSourceManifest, error) {
	members, err := readTextTree(source, "Instruction")
	if err != nil {
		return nil, instructionSourceManifest{}, err
	}
	byPath := make(map[string][]byte, len(members))
	for _, member := range members {
		byPath[member.relative] = member.bytes
	}
	manifestContents, found := byPath["instructions.toml"]
	if !found {
		return nil, instructionSourceManifest{}, fmt.Errorf("Instruction source requires instructions.toml; expected a structured Instruction set with AGENTS.md and declared Instruction docs")
	}
	var manifest instructionSourceManifest
	decoder := toml.NewDecoder(bytes.NewReader(manifestContents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, instructionSourceManifest{}, fmt.Errorf("parse Instruction manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Index != "AGENTS.md" || byPath[manifest.Index] == nil || len(manifest.Documents) == 0 {
		return nil, instructionSourceManifest{}, fmt.Errorf("invalid Instruction manifest")
	}
	ids := map[string]instructionDocument{}
	for _, document := range manifest.Documents {
		if !kebab(document.ID) || document.Path == "" || !safeInstructionPath(document.Path) || byPath[document.Path] == nil || (document.Harness != "" && !supportedInstructionHarness(document.Harness)) {
			return nil, instructionSourceManifest{}, fmt.Errorf("invalid Instruction document declaration")
		}
		if _, duplicate := ids[document.ID]; duplicate {
			return nil, instructionSourceManifest{}, fmt.Errorf("duplicate Instruction document ID %q", document.ID)
		}
		ids[document.ID] = document
		if document.Harness == "" && len(document.Supersedes) > 0 {
			return nil, instructionSourceManifest{}, fmt.Errorf("shared Instruction document may not supersede another document")
		}
	}
	for _, document := range manifest.Documents {
		for _, superseded := range document.Supersedes {
			referenced, found := ids[superseded]
			if !kebab(superseded) || !found || referenced.Harness != "" {
				return nil, instructionSourceManifest{}, fmt.Errorf("invalid superseded Instruction document ID")
			}
		}
	}
	return members, manifest, nil
}

func selectedInstructionDocuments(manifest instructionSourceManifest, harnessName string) []instructionDocument {
	superseded := map[string]struct{}{}
	for _, document := range manifest.Documents {
		if document.Harness == harnessName {
			for _, id := range document.Supersedes {
				superseded[id] = struct{}{}
			}
		}
	}
	selected := make([]instructionDocument, 0, len(manifest.Documents))
	for _, document := range manifest.Documents {
		if document.Harness != "" && document.Harness != harnessName {
			continue
		}
		if document.Harness == "" {
			if _, skip := superseded[document.ID]; skip {
				continue
			}
		}
		selected = append(selected, document)
	}
	return selected
}

func supportedInstructionHarness(name string) bool {
	return name == "codex" || name == "claude" || name == "opencode"
}

func safeInstructionPath(path string) bool {
	return !strings.HasPrefix(path, "/") && path != "instructions.toml" && path != "AGENTS.md" && !strings.HasPrefix(path, "../") && !strings.Contains(path, "/../")
}

func readTextTree(source, label string) ([]member, error) {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s source must be a directory: %s", label, source)
	}
	var members []member
	total := 0
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == ".DS_Store" {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("%s source contains unsafe member %s", label, relative)
		}
		if entry.IsDir() {
			return nil
		}
		if len(members) == maxFiles {
			return fmt.Errorf("%s source exceeds %d files", label, maxFiles)
		}
		contents, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(contents) {
			return fmt.Errorf("%s source contains non-UTF-8 file %s", label, relative)
		}
		total += len(contents)
		if total > maxBytes {
			return fmt.Errorf("%s source exceeds %d bytes", label, maxBytes)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		members = append(members, member{relative: filepath.ToSlash(relative), bytes: contents, mode: info.Mode()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].relative < members[j].relative })
	return members, nil
}

func digest(kind string, members []member) string {
	hash := sha256.New()
	write := func(value string) { fmt.Fprintf(hash, "%d:%s", len(value), value) }
	write("brigsby-revision-v1")
	write(kind)
	for _, item := range members {
		write(item.relative)
		fmt.Fprintf(hash, "%d:", len(item.bytes))
		hash.Write(item.bytes)
	}
	return "sha256-" + hex.EncodeToString(hash.Sum(nil))
}

func kebab(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func validPrefix(value string) bool {
	if value == "" {
		return true
	}
	if !strings.HasSuffix(value, "-") {
		return false
	}
	return kebab(strings.TrimSuffix(value, "-"))
}

func renderFrontmatterName(contents []byte, expected, rendered string) ([]byte, error) {
	text := string(contents)
	newline := "\n"
	if strings.HasPrefix(text, "---\r\n") {
		newline = "\r\n"
	} else if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("prefixed Skill requires YAML frontmatter with name and description")
	}
	lines := strings.SplitAfter(text, newline)
	if len(lines) < 3 || strings.TrimSuffix(lines[0], newline) != "---" {
		return nil, fmt.Errorf("prefixed Skill has invalid YAML frontmatter")
	}
	nameIndex, descriptionFound, closed := -1, false, false
	for index := 1; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], newline)
		if line == "---" {
			closed = true
			break
		}
		if strings.HasPrefix(line, "name: ") {
			if nameIndex != -1 || !kebab(strings.TrimPrefix(line, "name: ")) {
				return nil, fmt.Errorf("prefixed Skill has invalid name frontmatter")
			}
			nameIndex = index
		}
		if strings.HasPrefix(line, "description: ") && strings.TrimSpace(strings.TrimPrefix(line, "description: ")) != "" {
			descriptionFound = true
		}
	}
	if !closed || nameIndex == -1 || !descriptionFound {
		return nil, fmt.Errorf("prefixed Skill requires YAML frontmatter with name and description")
	}
	if got := strings.TrimPrefix(strings.TrimSuffix(lines[nameIndex], newline), "name: "); got != expected {
		return nil, fmt.Errorf("prefixed Skill name %q does not match canonical Artifact name %q", got, expected)
	}
	lines[nameIndex] = "name: " + rendered + newline
	return []byte(strings.Join(lines, "")), nil
}

func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
