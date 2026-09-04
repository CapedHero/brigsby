package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CapedHero/brigsby/internal/artifact"
	"github.com/CapedHero/brigsby/internal/harness"
	"github.com/CapedHero/brigsby/internal/lifecycle"
	"github.com/CapedHero/brigsby/internal/portable"
	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/CapedHero/brigsby/internal/temp"
	"github.com/itchyny/gojq"
	"github.com/spf13/pflag"
)

var errMachineInvalid = errors.New("invalid machine output request")

// domainError is an actionable failure discovered while executing a valid
// command. Unlike argument and flag errors, it should not be buried beneath
// root help text.
type domainError struct{ err error }

func (err domainError) Error() string { return err.err.Error() }

func (err domainError) Unwrap() error { return err.err }

func domainErrorf(format string, arguments ...any) error {
	return domainError{err: fmt.Errorf(format, arguments...)}
}

type stateError struct {
	err     error
	context map[string]any
}

func (err stateError) Error() string { return err.err.Error() }
func (err stateError) Unwrap() error { return err.err }
func stateErrorf(format string, arguments ...any) error {
	return stateError{err: fmt.Errorf(format, arguments...)}
}

func projectionStateErrorf(format string, harnessID string, projection harness.Projection, arguments ...any) error {
	return stateError{err: fmt.Errorf(format, arguments...), context: map[string]any{
		"harness": harnessID,
		"kind":    kindWord(projection.Artifact),
		"path":    projection.Path,
		"ref":     artifact.DisplayRef(projection.Artifact),
	}}
}

func isStateError(err error) bool { var state stateError; return errors.As(err, &state) }

type partialError struct {
	batchID string
	err     error
}

func (err partialError) Error() string {
	return fmt.Sprintf("PARTIAL: lifecycle batch %s: %v", err.batchID, err.err)
}
func (err partialError) Unwrap() error { return err.err }

func tryNode(word, kind string) *node {
	return command("Copy one selected canonical "+word+" to unlinked Harnesses as a tracked temp.", "<namespace/name>", 1, 1, func() (*pflag.FlagSet, leaf) {
		set := newFlagSet("brigsby " + word + " try")
		var targets []string
		set.StringArrayVar(&targets, "to", nil, "discoverable Harness name (repeatable)")
		id := set.String("id", "", "explicit temp ID")
		revisionDigest := set.String("revision", "", "stored Revision digest")
		dryRun := set.Bool("dry-run", false, "preview without writing")
		return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
			if len(targets) == 0 {
				return outcome{}, domainErrorf("try requires at least one --to <harness>")
			}
			key, err := artifact.Key(kind, pos[0])
			if err != nil {
				return outcome{}, asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return outcome{}, err
			}
			store := artifact.NewStore(root)
			revision, err := store.Selected(key)
			if err != nil {
				return outcome{}, fmt.Errorf("read selected %s: %w", word, err)
			}
			selectedDigest := revision.Digest
			if *revisionDigest != "" {
				revision, err = store.VerifyRevision(key, *revisionDigest)
				if err != nil {
					return outcome{}, fmt.Errorf("verify requested %s Revision: %w", word, err)
				}
			}
			candidates, err := discoverBuiltinCandidates()
			if err != nil {
				return outcome{}, err
			}
			registry := temp.New(root)
			planned := []map[string]any{}
			type item struct {
				record  temp.Record
				plans   []recovery.Plan
				cleanup func()
			}
			items := []item{}
			for _, name := range targets {
				var candidate harness.Candidate
				found := false
				for _, candidate0 := range candidates {
					if candidate0.candidate.Name == name && candidate0.found {
						candidate, found = candidate0.candidate, true
						break
					}
				}
				if !found {
					return outcome{}, fmt.Errorf("BLOCKED: discoverable Harness %q was not found", name)
				}
				tempID := *id
				if tempID == "" {
					tempID = name + "-" + strings.ReplaceAll(artifact.DisplayRef(key), "/", "-")
				}
				if len(targets) > 1 && *id != "" {
					return outcome{}, domainErrorf("--id requires exactly one --to")
				}
				if _, err := registry.Get(tempID); err == nil {
					return outcome{}, fmt.Errorf("BLOCKED: temp ID %q already exists", tempID)
				} else if !os.IsNotExist(err) {
					return outcome{}, err
				}
				var sources, paths []string
				var cleanup func()
				if kind == artifact.KindSkill {
					if *revisionDigest != "" && revision.Digest != selectedDigest {
						prefix, err := store.Prefix(strings.Split(key, "/")[0])
						if err != nil {
							return outcome{}, err
						}
						if prefix != "" {
							return outcome{}, domainErrorf("try --revision requires the selected Revision when the Namespace has a rendered Skill prefix")
						}
						_, files, err := store.RevisionContentFilesPath(key, revision.Digest)
						if err != nil {
							return outcome{}, err
						}
						sources = []string{files}
						paths = []string{filepath.Join(candidate.SkillsPath, refName(pos[0]))}
						cleanup = func() {}
					} else {
						rendered, err := store.RenderSelectedSkill(key)
						if err != nil {
							return outcome{}, err
						}
						sources = []string{rendered.FilesPath}
						paths = []string{filepath.Join(candidate.SkillsPath, rendered.Name)}
						cleanup = rendered.Cleanup
					}
				} else {
					if *revisionDigest != "" && revision.Digest != selectedDigest {
						return outcome{}, domainErrorf("instruction try --revision currently requires the selected Revision")
					}
					rendered, err := store.RenderSelectedInstructions(key, candidate.Name)
					if err != nil {
						return outcome{}, err
					}
					rootPath, err := instructionRootPath(candidate)
					if err != nil {
						return outcome{}, err
					}
					sources = []string{rendered.RootPath, rendered.TreePath}
					paths = []string{rootPath, filepath.Join(candidate.InstructionsPath, "brigsby", refName(pos[0]))}
					cleanup = rendered.Cleanup
				}
				fingerprints := []string{}
				plans := []recovery.Plan{}
				for i, target := range paths {
					if f, e := recovery.Fingerprint(target); e != nil {
						return outcome{}, e
					} else if f != "absent" {
						return outcome{}, fmt.Errorf("BLOCKED: temp destination exists at %s", target)
					}
					p, e := recovery.New(root).Plan(target, sources[i])
					if e != nil {
						return outcome{}, e
					}
					plans = append(plans, p)
					fingerprints = append(fingerprints, p.ReplacementFingerprint())
				}
				items = append(items, item{temp.Record{ID: tempID, Harness: name, Paths: paths, Artifact: key, Revision: revision.Digest, Fingerprints: fingerprints, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, plans, cleanup})
				planned = append(planned, map[string]any{"id": tempID, "harness": name, "paths": paths, "revision": revision.Digest})
			}
			defer func() {
				for _, x := range items {
					if x.cleanup != nil {
						x.cleanup()
					}
				}
			}()
			if *dryRun {
				return outcome{command: word + " try", state: "planned", result: planned}, nil
			}
			for _, x := range items {
				for _, plan := range x.plans {
					if _, err := recovery.New(root).Apply(plan); err != nil {
						return outcome{}, err
					}
				}
				if err := registry.Add(x.record); err != nil {
					return outcome{}, err
				}
			}
			return outcome{command: word + " try", state: "applied", result: planned}, nil
		}
	})
}

func tempsNode(word, kind string) *node {
	return group("Inspect and clean tracked temporary "+word+" copies.", map[string]*node{
		"list": command("List tracked temporary copies.", "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " temps list")
			harnessName := set.String("harness", "", "filter by Harness")
			namespace := set.String("namespace", "", "filter by Namespace")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, e := brigsbyHome()
				if e != nil {
					return outcome{}, e
				}
				records, e := temp.New(root).List()
				if e != nil {
					return outcome{}, e
				}
				result := []map[string]any{}
				for _, r := range records {
					if artifact.KeyKind(r.Artifact) != kind || (*harnessName != "" && r.Harness != *harnessName) || (*namespace != "" && !strings.HasPrefix(r.Artifact, *namespace+"/")) {
						continue
					}
					states := []string{}
					for i, p := range r.Paths {
						f, e := recovery.ContentFingerprint(p)
						if e != nil {
							return outcome{}, e
						}
						s := "unchanged"
						if f == "absent" {
							s = "missing"
						} else if f != r.Fingerprints[i] {
							s = "drifted"
						}
						states = append(states, s)
					}
					result = append(result, map[string]any{"id": r.ID, "harness": r.Harness, "ref": artifact.DisplayRef(r.Artifact), "revision": r.Revision, "paths": r.Paths, "state": states})
				}
				return outcome{command: word + " temps list", state: "clean", result: map[string]any{"temps": result}}, nil
			}
		}),
		"show": command("Show one tracked temporary copy.", "<temp-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " temps show")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, e := brigsbyHome()
				if e != nil {
					return outcome{}, e
				}
				r, e := temp.New(root).Get(pos[0])
				if os.IsNotExist(e) {
					return outcome{}, domainErrorf("temp %q was not found", pos[0])
				}
				if e != nil {
					return outcome{}, e
				}
				if artifact.KeyKind(r.Artifact) != kind {
					return outcome{}, domainErrorf("temp %q is not a %s", pos[0], word)
				}
				return outcome{command: word + " temps show", state: "clean", result: map[string]any{"id": r.ID, "harness": r.Harness, "ref": artifact.DisplayRef(r.Artifact), "revision": r.Revision, "paths": r.Paths, "created_at": r.CreatedAt}}, nil
			}
		}),
		"detach": command("Forget one temp without changing its files.", "<temp-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " temps detach")
			dry := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, e := brigsbyHome()
				if e != nil {
					return outcome{}, e
				}
				r, e := temp.New(root).Get(pos[0])
				if e != nil {
					return outcome{}, e
				}
				if artifact.KeyKind(r.Artifact) != kind {
					return outcome{}, domainErrorf("temp %q is not a %s", pos[0], word)
				}
				if !*dry {
					e = temp.New(root).Remove(r.ID)
					if e != nil {
						return outcome{}, e
					}
				}
				return outcome{command: word + " temps detach", state: mutationState(*dry), result: map[string]any{"detached": r.ID, "paths": r.Paths}}, nil
			}
		}),
		"promote": command("Convert one unchanged temp into a managed Projection.", "<temp-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " temps promote")
			dry := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, e := brigsbyHome()
				if e != nil {
					return outcome{}, e
				}
				tr := temp.New(root)
				r, e := tr.Get(pos[0])
				if e != nil {
					return outcome{}, e
				}
				if artifact.KeyKind(r.Artifact) != kind {
					return outcome{}, domainErrorf("temp %q is not a %s", pos[0], word)
				}
				linked, e := harness.NewRegistry(root).List()
				if e != nil {
					return outcome{}, e
				}
				var candidate harness.Candidate
				found := false
				for _, x := range linked {
					if x.ID == r.Harness {
						candidate = x
						found = true
					}
				}
				if !found {
					return outcome{}, fmt.Errorf("BLOCKED: harness_unlinked %q; run 'brigsby harness link %s'", r.Harness, r.Harness)
				}
				for i, p := range r.Paths {
					f, e := recovery.ContentFingerprint(p)
					if e != nil {
						return outcome{}, e
					}
					if f == "absent" || f != r.Fingerprints[i] {
						return outcome{}, fmt.Errorf("BLOCKED: temp %q has drifted or is missing", r.ID)
					}
				}
				if !*dry {
					reg := harness.NewRegistry(root)
					for i, p := range r.Paths {
						if e := reg.RecordProjection(harness.Projection{HarnessID: candidate.ID, Path: p, Artifact: r.Artifact, Revision: r.Revision, Fingerprint: r.Fingerprints[i]}); e != nil {
							return outcome{}, e
						}
					}
					if e := tr.Remove(r.ID); e != nil {
						return outcome{}, e
					}
				}
				return outcome{command: word + " temps promote", state: mutationState(*dry), result: map[string]any{"promoted": r.ID, "paths": r.Paths}}, nil
			}
		}),
		"delete": command("Remove tracked temps safely.", "<temp-id>", 0, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " temps delete")
			all := set.Bool("all", false, "remove every matching temp")
			purge := set.Bool("purge", false, "remove without Recovery")
			dry := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if *all == (len(pos) == 1) {
					return outcome{}, domainErrorf("delete requires exactly one temp ID or --all")
				}
				root, e := brigsbyHome()
				if e != nil {
					return outcome{}, e
				}
				rs, e := temp.New(root).List()
				if e != nil {
					return outcome{}, e
				}
				chosen := []temp.Record{}
				for _, r := range rs {
					if artifact.KeyKind(r.Artifact) == kind && (*all || r.ID == pos[0]) {
						chosen = append(chosen, r)
					}
				}
				if len(chosen) == 0 {
					return outcome{}, domainErrorf("no matching temps")
				}
				removed := []string{}
				blocked := []map[string]any{}
				for _, r := range chosen {
					clean := true
					for i, p := range r.Paths {
						f, e := recovery.ContentFingerprint(p)
						if e != nil {
							return outcome{}, e
						}
						if f != "absent" && f != r.Fingerprints[i] {
							clean = false
						}
					}
					if !clean {
						blocked = append(blocked, map[string]any{"id": r.ID, "code": "temp_drift"})
						continue
					}
					if !*dry {
						for _, p := range r.Paths {
							if *purge {
								if e := os.RemoveAll(p); e != nil {
									return outcome{}, e
								}
							} else {
								plan, e := recovery.PlanRemoval(p)
								if e != nil {
									return outcome{}, e
								}
								if _, e = recovery.New(root).Apply(plan); e != nil {
									return outcome{}, e
								}
							}
						}
						if e := temp.New(root).Remove(r.ID); e != nil {
							return outcome{}, e
						}
					}
					removed = append(removed, r.ID)
				}
				state := mutationState(*dry)
				if len(blocked) > 0 {
					state = "blocked"
				}
				return outcome{command: word + " temps delete", state: state, problems: blocked, result: map[string]any{"deleted": removed}}, nil
			}
		}),
	})
}

func asDomainError(err error) error {
	if err == nil {
		return nil
	}
	return domainError{err: err}
}

// version is the release identity. It stays "dev" for an ordinary `go build`
// or `go run` from a checkout and is overridden at release build time with
// -ldflags "-X main.version=<tag>" (the Homebrew formula does this).
var version = "dev"

// pseudoVersionTail matches the trailing "<14-digit timestamp>-<12-hex>" of a
// Go module pseudo-version, which VCS stamping writes into Main.Version for an
// ordinary `go build` from a checkout. The timestamp is preceded by "-" when no
// tag is reachable (v0.0.0-20060102150405-abcdef012345) and by "." when one is
// (v1.2.3-0.20060102150405-abcdef012345), so match the signature itself.
var pseudoVersionTail = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}$`)

// pickVersion resolves the reported version from the two inputs that can carry
// a release identity: the linker-injected package variable (set by the
// Homebrew formula and any other tagged build), and the module version
// recorded by `go install <module>@<tag>` (biVersion/biOK from
// debug.ReadBuildInfo). Anything that is not a clean release tag -- "(devel)"
// from `go run`, the empty string, or a commit pseudo-version from a local
// `go build` -- falls back to "dev".
func pickVersion(ldflag, biVersion string, biOK bool) string {
	if ldflag != "dev" {
		return ldflag
	}
	if biOK && isReleaseVersion(biVersion) {
		return biVersion
	}
	return "dev"
}

// isReleaseVersion reports whether v came from installing a published tag
// rather than from a local build tree.
func isReleaseVersion(v string) bool {
	if strings.HasSuffix(v, "+dirty") {
		return false
	}
	v, _, _ = strings.Cut(v, "+") // drop "+incompatible" build metadata
	if v == "" || v == "(devel)" {
		return false
	}
	return !pseudoVersionTail.MatchString(v)
}

// resolveVersion is memoized: it is consulted on every invocation, including the
// benchmarked help path, so the debug.ReadBuildInfo read must not repeat.
var resolveVersion = sync.OnceValue(func() string {
	biVersion, biOK := "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		biVersion, biOK = info.Main.Version, true
	}
	return pickVersion(version, biVersion, biOK)
})

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one CLI invocation. Every command result is emitted to stdout as
// a single pretty-printed, alphabetically key-sorted JSON envelope so that the
// only audience that has to parse Brigsby is an agent or a script. The
// exceptions are the conventional plain-text affordances: `--help`, `-h`, the
// `help` subcommand, a bare invocation, and `--version` print human text,
// exactly as every peer CLI does. (`brigsby version` still emits the envelope.)
func run(arguments []string, stdout, stderr io.Writer) int {
	if wantsPlainText(arguments) {
		return runPlainText(arguments, stdout, stderr)
	}

	rest, jqExpression := extractJQ(arguments)
	rest = expandAlias(rest)
	var out outcome
	var err error
	if len(rest) > 1 && rest[0] == "harness" && rest[1] == "sync" {
		err = fmt.Errorf("unknown command %q for %q", "sync", "brigsby harness")
	} else {
		out, err = rootNode.route("brigsby", rest, stderr)
	}

	// stdout always carries exactly one JSON envelope -- the machine contract.
	result := envelopeFrom(out, rest, err)
	if outputErr := writeMachineResult(stdout, result, jqExpression); outputErr != nil {
		return 1
	}
	if err == nil {
		return 0
	}
	// stderr carries a one-line human diagnostic on failure. It is never parsed;
	// a caller keys off the exit code and the stdout envelope's state/problems.
	if isDomainError(err) {
		fmt.Fprintln(stderr, err)
		return 3
	}
	fmt.Fprintln(stderr, err)
	fmt.Fprintln(stderr)
	fmt.Fprint(stderr, helpText())
	return 2
}

// wantsPlainText reports whether the invocation is a conventional plain-text
// affordance (help or version) rather than a command result.
func wantsPlainText(arguments []string) bool {
	if len(arguments) == 0 {
		return true
	}
	if arguments[0] == "help" {
		return true
	}
	for _, argument := range arguments {
		if argument == "--help" || argument == "-h" || argument == "--version" {
			return true
		}
	}
	return false
}

// runPlainText serves the human affordances: `--version` prints the version
// line; everything else (`help`, `--help`, `-h`, a bare invocation) prints the
// usage for the deepest command named on the line, or the root when none is.
func runPlainText(arguments []string, stdout, stderr io.Writer) int {
	rest := arguments
	forceHelp := false
	if len(rest) > 0 && rest[0] == "help" {
		forceHelp = true
		rest = rest[1:]
	}
	var path []string
	for _, argument := range rest {
		switch {
		case argument == "--help" || argument == "-h":
			forceHelp = true
		case argument == "--version":
			if !forceHelp {
				fmt.Fprintf(stdout, "brigsby %s\n", resolveVersion())
				return 0
			}
		case strings.HasPrefix(argument, "-"):
			// ignore other flags when resolving the help target
		default:
			path = append(path, argument)
		}
	}
	target, used := rootNode.find(path)
	fmt.Fprint(stdout, renderHelp(target, used))
	return 0
}

// extractJQ removes the global --jq flag (--jq <expr> or --jq=<expr>) from
// anywhere in the argument list and returns the cleaned arguments alongside the
// expression. It is the hand-rolled stand-in for the persistent flag cobra
// parsed.
func extractJQ(arguments []string) ([]string, string) {
	cleaned := make([]string, 0, len(arguments))
	expression := ""
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--jq":
			if index+1 < len(arguments) {
				expression = arguments[index+1]
				index++
			}
		case strings.HasPrefix(argument, "--jq="):
			expression = strings.TrimPrefix(argument, "--jq=")
		default:
			cleaned = append(cleaned, argument)
		}
	}
	return cleaned, expression
}

// expandAlias rewrites the two root-level convenience aliases to their real
// command path before routing. `brigsby status` and `brigsby sync` are exactly
// `brigsby harness status` and `brigsby harness sync`.
func expandAlias(arguments []string) []string {
	if len(arguments) == 0 {
		return arguments
	}
	switch arguments[0] {
	case "status":
		return append([]string{"harness", "status"}, arguments[1:]...)
	}
	return arguments
}

func isBlockedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BLOCKED:")
}

func isPartialError(err error) bool {
	var partial partialError
	return errors.As(err, &partial)
}

func isDomainError(err error) bool {
	var domain domainError
	return isPartialError(err) || isBlockedError(err) || isStateError(err) || errors.As(err, &domain)
}

// canonicalValue re-serialises any value so that every nested object becomes a
// map[string]any (whose keys encoding/json emits in alphabetical order) and
// every exotic concrete type collapses to a plain JSON kind. It is the single
// choke point that makes Brigsby's output deterministic and sorted at every
// depth.
func canonicalValue(v any) (any, error) {
	compact, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(compact, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// encodeCanonicalJSON writes v as pretty-printed, alphabetically key-sorted
// JSON followed by a newline. Every byte Brigsby prints as a result goes
// through here.
func encodeCanonicalJSON(w io.Writer, v any) error {
	normalized, err := canonicalValue(v)
	if err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(pretty, '\n'))
	return err
}

// envelopeFrom turns one command execution into the canonical envelope. A
// command that ran to completion returns a populated outcome; this guarantees
// the three core keys are present. Only a failure -- an unknown command, a bad flag,
// or a domain/BLOCKED error returned from a leaf -- needs an envelope
// synthesised here.
func envelopeFrom(out outcome, arguments []string, commandErr error) map[string]any {
	result := map[string]any{
		"state":    "clean",
		"problems": []any{},
		"result":   nil,
	}
	if commandErr == nil {
		if out.state != "" {
			result["state"] = out.state
		}
		if out.problems != nil {
			result["problems"] = out.problems
		}
		result["result"] = out.result
		if preview, ok := out.preview.(map[string]any); ok {
			if recoveryIDs, ok := preview["recovery_ids"]; ok {
				result["recovery_ids"] = recoveryIDs
			}
		}
		return result
	}
	code, state := "invalid_request", "invalid"
	switch {
	case isPartialError(commandErr):
		code, state = "partial", "partial"
	case isBlockedError(commandErr):
		code, state = "blocked", "blocked"
	case isDomainError(commandErr):
		code = "domain_error"
	}
	if isStateError(commandErr) {
		code = "state_error"
	}
	result["state"] = state
	problem := map[string]any{"code": code, "message": commandErr.Error()}
	var stateErr stateError
	if errors.As(commandErr, &stateErr) {
		for key, value := range stateErr.context {
			problem[key] = value
		}
	}
	result["problems"] = []map[string]any{problem}
	var partial partialError
	if errors.As(commandErr, &partial) {
		result["result"] = map[string]any{"recovery_id": partial.batchID}
	}
	return result
}

// machineResult is retained as the direct envelope helper used by lifecycle
// tests and callers that need to classify a command failure without routing it.
func machineResult(_ string, commandErr error) map[string]any {
	return envelopeFrom(outcome{}, nil, commandErr)
}

func lifecyclePartialError(batch lifecycle.Batch, cause error) error {
	if batch.ID == "" {
		return cause
	}
	return partialError{batchID: batch.ID, err: cause}
}

func commandName(arguments []string) string {
	arguments = commandArguments(arguments)
	if len(arguments) == 0 {
		return "brigsby"
	}
	switch arguments[0] {
	case "status":
		return "harness status"
	case "sync":
		return "harness sync"
	}
	command := []string{arguments[0]}
	if len(arguments) > 1 && !strings.HasPrefix(arguments[1], "-") {
		switch arguments[0] {
		case "harness", "skill", "instruction", "namespace", "package", "recovery":
			command = append(command, arguments[1])
		}
	}
	return strings.Join(command, " ")
}

func commandArguments(arguments []string) []string {
	for len(arguments) > 0 {
		argument := arguments[0]
		if argument == "--jq" {
			if len(arguments) < 2 {
				return nil
			}
			arguments = arguments[2:]
			continue
		}
		if strings.HasPrefix(argument, "--jq=") {
			arguments = arguments[1:]
			continue
		}
		break
	}
	return arguments
}

// writeMachineResult emits the final envelope. Without --jq it is the whole
// {state,problems,result} object; with --jq it is each value
// the expression yields, every one of them still pretty-printed and key-sorted.
func writeMachineResult(stdout io.Writer, result map[string]any, jqExpression string) error {
	envelope := map[string]any{
		"state":    result["state"],
		"problems": result["problems"],
		"result":   result["result"],
	}
	if recoveryIDs, ok := result["recovery_ids"]; ok {
		envelope["recovery_ids"] = recoveryIDs
	}
	if jqExpression == "" {
		return encodeCanonicalJSON(stdout, envelope)
	}
	input, err := canonicalValue(envelope)
	if err == nil {
		var query *gojq.Query
		query, err = gojq.Parse(jqExpression)
		if err == nil {
			var code *gojq.Code
			if code, err = gojq.Compile(query); err == nil {
				iterator := code.Run(input)
				for {
					value, ok := iterator.Next()
					if !ok {
						return nil
					}
					if valueErr, isError := value.(error); isError {
						err = valueErr
						break
					}
					if encodeErr := encodeCanonicalJSON(stdout, value); encodeErr != nil {
						return encodeErr
					}
				}
			}
		}
	}
	if encodeErr := encodeCanonicalJSON(stdout, map[string]any{
		"state":    "invalid",
		"problems": []map[string]string{{"code": "invalid_request", "message": fmt.Sprintf("invalid --jq expression: %v", err)}},
		"result":   nil,
	}); encodeErr != nil {
		return encodeErr
	}
	return errMachineInvalid
}

// contentRef is the canonical shape for a Skill or Instruction Revision wherever
// a result names one: a lower-case, sorted {digest, kind, ref} object. key is
// the internal store key "namespace/kind/name"; ref renders as "namespace/name".
func contentRef(key, digest string) map[string]any {
	return map[string]any{"digest": digest, "kind": kindWord(key), "ref": artifact.DisplayRef(key)}
}

// kindWord maps an internal key's kind to its Caller-facing singular.
func kindWord(key string) string {
	if artifact.KeyKind(key) == artifact.KindInstruction {
		return "instruction"
	}
	return "skill"
}

// displayContent renders "<kind> <namespace/name>" for an internal key, for use
// in human diagnostic strings.
func displayContent(key string) string {
	return kindWord(key) + " " + artifact.DisplayRef(key)
}

// projectionStatusKey prevents a Skill and an Instruction with the same
// namespace/name from colliding in a harness's keyed Projection inventory.
func projectionStatusKey(key string) string {
	return kindWord(key) + ":" + artifact.DisplayRef(key)
}

func projectionMissing(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func projectionProblem(id, code, message, harnessID string, projection harness.Projection) map[string]any {
	return map[string]any{"id": id, "code": code, "message": message, "harness": harnessID, "kind": kindWord(projection.Artifact), "ref": artifact.DisplayRef(projection.Artifact), "path": projection.Path}
}

type remedy struct {
	Command string `json:"command"`
}

func projectionRemedy(projection harness.Projection, harnessID string) remedy {
	flag := "--skill"
	if artifact.KeyKind(projection.Artifact) == artifact.KindInstruction {
		flag = "--instruction"
	}
	return remedy{Command: fmt.Sprintf("brigsby sync %s %s --harness %s", flag, artifact.DisplayRef(projection.Artifact), harnessID)}
}

func lifecycleTargets(projections []harness.Projection) []lifecycle.Target {
	targets := make([]lifecycle.Target, len(projections))
	for index, projection := range projections {
		targets[index] = lifecycle.Target{Path: projection.Path}
	}
	return targets
}

func projectionPaths(projections []harness.Projection) []string {
	paths := make([]string, len(projections))
	for index, projection := range projections {
		paths[index] = projection.Path
	}
	return paths
}

func mustBeMissing(path string) bool { _, err := os.Lstat(path); return os.IsNotExist(err) }

// refName returns the trailing name segment of a "namespace/name" reference.
func refName(ref string) string {
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		return ref[slash+1:]
	}
	return ref
}

// capturedRefs renders a list of Revisions as their "namespace/name" references.
func capturedRefs(revisions []artifact.Revision) string {
	refs := make([]string, len(revisions))
	for index, revision := range revisions {
		refs[index] = artifact.DisplayRef(revision.Selector)
	}
	return strings.Join(refs, ", ")
}

// mutationState maps a --dry-run flag to the envelope state a mutation reports.
func mutationState(dryRun bool) string {
	if dryRun {
		return "planned"
	}
	return "applied"
}

// outcome is what a leaf returns on success. run() serialises it into the
// canonical envelope exactly once; problems/preview stay nil unless a leaf sets
// them.
type outcome struct {
	command  string
	state    string
	problems any
	result   any
	preview  any
}

// leaf executes one resolved command. pos holds the positional arguments left
// after flag parsing; stderr carries human advisories that never enter stdout.
type leaf func(path string, pos []string, stderr io.Writer) (outcome, error)

// node is one entry in the command tree: a group (subs populated) or an
// executable command (factory populated). factory returns a fresh flag set plus
// the closure that runs once the set is parsed, so a single definition serves
// both execution and `--help` rendering.
type node struct {
	short   string
	argHint string
	subs    map[string]*node
	factory func() (*pflag.FlagSet, leaf)
	minPos  int
	maxPos  int // -1 means unbounded
}

// usageError is an invalid invocation: an unknown command, a bad flag, or the
// wrong number of positional arguments. run() prints it above the root usage
// and exits 2, matching the old cobra behaviour.
type usageError struct{ message string }

func (err usageError) Error() string { return err.message }

func usageErrorf(format string, arguments ...any) error {
	return usageError{message: fmt.Sprintf(format, arguments...)}
}

func newFlagSet(path string) *pflag.FlagSet {
	set := pflag.NewFlagSet(path, pflag.ContinueOnError)
	set.SetOutput(io.Discard) // errors are formatted by route, never by pflag
	set.Usage = func() {}
	return set
}

func group(short string, subs map[string]*node) *node {
	return &node{short: short, subs: subs}
}

func command(short, argHint string, minPos, maxPos int, factory func() (*pflag.FlagSet, leaf)) *node {
	return &node{short: short, argHint: argHint, minPos: minPos, maxPos: maxPos, factory: factory}
}

// aliasOf clones a target command so it can be reached under a second name for
// help and discovery; execution is redirected earlier by expandAlias.
func aliasOf(short string, target *node) *node {
	clone := *target
	clone.short = short
	return &clone
}

func (n *node) route(path string, args []string, stderr io.Writer) (outcome, error) {
	if n.factory != nil {
		set, fn := n.factory()
		if err := set.Parse(args); err != nil {
			return outcome{}, usageErrorf("%s: %v", path, err)
		}
		pos := set.Args()
		if n.maxPos == 0 && len(pos) > 0 {
			return outcome{}, usageErrorf("unknown command %q for %q", pos[0], path)
		}
		if len(pos) < n.minPos || (n.maxPos > 0 && len(pos) > n.maxPos) {
			return outcome{}, usageErrorf("%q: %s", path, arityText(n.minPos, n.maxPos))
		}
		return fn(path, pos, stderr)
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return outcome{}, usageErrorf("%q: subcommand required (one of: %s)", path, strings.Join(sortedKeys(n.subs), ", "))
	}
	child, ok := n.subs[args[0]]
	if !ok {
		return outcome{}, usageErrorf("unknown command %q for %q", args[0], path)
	}
	return child.route(path+" "+args[0], args[1:], stderr)
}

// find walks as far down the tree as the leading path segments match, returning
// the node reached and the segments consumed.
func (n *node) find(path []string) (*node, []string) {
	current := n
	used := make([]string, 0, len(path))
	for _, segment := range path {
		next, ok := current.subs[segment]
		if !ok {
			break
		}
		current = next
		used = append(used, segment)
	}
	return current, used
}

func sortedKeys(m map[string]*node) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func arityText(minPos, maxPos int) string {
	switch {
	case maxPos < 0:
		return fmt.Sprintf("expected at least %d argument(s)", minPos)
	case minPos == maxPos:
		return fmt.Sprintf("expected exactly %d argument(s)", minPos)
	default:
		return fmt.Sprintf("expected %d to %d arguments", minPos, maxPos)
	}
}

// renderHelp produces the plain-text usage for one node. The `Usage:` line
// keeps the shape peer CLIs (and the first-party Skill contract) expect:
// "brigsby <path> [flags]" for a command, "brigsby [flags]" plus a "[command]"
// line for the runnable root, and "brigsby <path> [command]" for a group.
func renderHelp(n *node, path []string) string {
	name := strings.Join(append([]string{"brigsby"}, path...), " ")
	var b strings.Builder
	if n.short != "" {
		b.WriteString(n.short + "\n\n")
	}
	b.WriteString("Usage:\n")
	switch {
	case n.factory != nil:
		line := "  " + name
		if n.argHint != "" {
			line += " " + n.argHint
		}
		b.WriteString(line + " [flags]\n")
	case len(path) == 0:
		b.WriteString("  " + name + " [flags]\n  " + name + " [command]\n")
	default:
		b.WriteString("  " + name + " [command]\n")
	}
	if len(n.subs) > 0 {
		b.WriteString("\nAvailable Commands:\n")
		for _, key := range sortedKeys(n.subs) {
			fmt.Fprintf(&b, "  %-13s %s\n", key, n.subs[key].short)
		}
	}
	b.WriteString("\nFlags:\n")
	if n.factory != nil {
		set, _ := n.factory()
		b.WriteString(set.FlagUsages())
	}
	if len(path) == 0 {
		b.WriteString("      --jq string   filter the JSON result with a jq expression\n")
	}
	b.WriteString("  -h, --help   help for " + name + "\n")
	if len(path) == 0 {
		b.WriteString("      --version   version for brigsby\n")
	}
	return b.String()
}

func helpText() string { return renderHelp(rootNode, nil) }

var rootNode = buildRootNode()

func buildRootNode() *node {
	harnessGroup := harnessNode()
	return &node{
		short: "Brigsby manages AI coding-agent Artifacts safely.",
		subs: map[string]*node{
			"version":     versionNode(),
			"harness":     harnessGroup,
			"skill":       contentNode("skill", artifact.KindSkill),
			"instruction": contentNode("instruction", artifact.KindInstruction),
			"namespace":   namespaceNode(),
			"package":     packageNode(),
			"recovery":    recoveryNode(),
			"status":      aliasOf("Report linked Harness state (alias for harness status).", harnessGroup.subs["status"]),
			"sync":        aliasOf("Project selected canonical content (alias for harness sync).", harnessGroup.subs["sync"]),
		},
	}
}

func versionNode() *node {
	return command("Print the Brigsby version", "", 0, 0, func() (*pflag.FlagSet, leaf) {
		set := newFlagSet("brigsby version")
		return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
			return outcome{command: "version", state: "clean", result: map[string]any{"version": resolveVersion()}}, nil
		}
	})
}

func namespaceNode() *node {
	return group("Configure Namespace rendering rules.", map[string]*node{
		"set-prefix": command("Set a Recovery-backed target-facing Skill prefix.", "<namespace> <prefix>", 2, 2, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby namespace set-prefix")
			dryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if err := artifact.ValidatePrefix(pos[0], pos[1]); err != nil {
					return outcome{}, err
				}
				result := map[string]any{"namespace": pos[0], "prefix": pos[1]}
				if !*dryRun {
					root, err := brigsbyHome()
					if err != nil {
						return outcome{}, err
					}
					if err := artifact.NewStore(root).SetPrefix(pos[0], pos[1]); err != nil {
						return outcome{}, fmt.Errorf("set Namespace prefix: %w", err)
					}
				}
				return outcome{command: "namespace set-prefix", state: mutationState(*dryRun), result: result}, nil
			}
		}),
	})
}

func packageNode() *node {
	return group("Create and inspect portable text-only Packages. In v1 a Package carries Skills only; name them with --skill namespace/name.", map[string]*node{
		"create": command("Create a portable Package from selected Skills.", "--skill <namespace/name> --output <new-path>", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby package create")
			var skillRefs []string
			set.StringArrayVar(&skillRefs, "skill", nil, "selected Skill reference namespace/name (repeatable)")
			output := set.String("output", "", "absolute output archive path")
			replace := set.Bool("replace", false, "replace an existing output guarded by --expect")
			expect := set.String("expect", "", "expected existing-output fingerprint")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if *output == "" {
					return outcome{}, domainErrorf("package create requires --output <absolute-path>")
				}
				if len(skillRefs) == 0 {
					return outcome{}, domainErrorf("package create requires at least one --skill <namespace/name>")
				}
				keys := make([]string, 0, len(skillRefs))
				for _, ref := range skillRefs {
					key, err := artifact.Key(artifact.KindSkill, ref)
					if err != nil {
						return outcome{}, asDomainError(err)
					}
					keys = append(keys, key)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				result, err := portable.Create(root, keys, *output, *replace, *expect)
				if err != nil {
					return outcome{}, err
				}
				return outcome{command: "package create", state: "applied", result: map[string]any{
					"digest": result.Digest,
					"skills": len(result.Artifacts),
					"output": *output,
				}}, nil
			}
		}),
		"inspect": command("Verify a Package without writing.", "<archive>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby package inspect")
			expectedDigest := set.String("expect-digest", "", "expected Package digest")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				result, err := portable.Inspect(pos[0], *expectedDigest)
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				skills := make([]map[string]any, 0, len(result.Artifacts))
				for _, revision := range result.Artifacts {
					skills = append(skills, contentRef(revision.Selector, revision.Digest))
				}
				return outcome{command: "package inspect", state: "clean", result: map[string]any{
					"digest": result.Digest,
					"skills": skills,
				}}, nil
			}
		}),
		"import": command("Verify and inertly import a Package.", "<archive>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby package import")
			namespace := set.String("namespace", "", "isolated destination Namespace")
			importDryRun := set.Bool("dry-run", false, "preview import without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				importArtifact := portable.Import
				if *importDryRun {
					importArtifact = portable.CheckImport
				}
				revisions, err := importArtifact(root, pos[0], *namespace)
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				imported := make([]map[string]any, 0, len(revisions))
				for _, revision := range revisions {
					imported = append(imported, contentRef(revision.Selector, revision.Digest))
				}
				return outcome{command: "package import", state: mutationState(*importDryRun), result: map[string]any{"imported": imported}}, nil
			}
		}),
	})
}

func recoveryNode() *node {
	return group("Inspect and restore Recovery operations.", map[string]*node{
		"list": command("List Recovery operation bundles.", "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby recovery list")
			limit := set.Int("limit", 0, "maximum operations to list")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				records, err := recovery.New(root).List()
				if err != nil {
					return outcome{}, fmt.Errorf("list Recovery operations: %w", err)
				}
				if *limit > 0 && len(records) > *limit {
					records = records[:*limit]
				}
				operations := make([]map[string]any, 0, len(records))
				for _, record := range records {
					operations = append(operations, map[string]any{
						"id":     record.ID,
						"state":  record.State,
						"target": record.Target,
					})
				}
				return outcome{command: "recovery list", state: "clean", result: map[string]any{"operations": operations}}, nil
			}
		}),
		"show": command("Inspect one Recovery operation bundle.", "<recovery-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby recovery show")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				record, err := recovery.New(root).Show(pos[0])
				if err != nil {
					return outcome{}, fmt.Errorf("show Recovery operation: %w", err)
				}
				return outcome{command: "recovery show", state: "clean", result: map[string]any{
					"operation": map[string]any{
						"id":                      record.ID,
						"state":                   record.State,
						"target":                  record.Target,
						"target_fingerprint":      record.TargetFingerprint,
						"replacement_fingerprint": record.ReplacementFingerprint,
					},
				}}, nil
			}
		}),
		"restore": command("Restore one Recovery operation's preimage.", "<recovery-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby recovery restore")
			restoreExpect := set.String("expect", "", "expected current target fingerprint")
			restoreDryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				if lifecycle.New(root).Exists(pos[0]) {
					if *restoreDryRun {
						return outcome{state: "planned", result: map[string]any{"restored": map[string]any{"recovery_id": pos[0]}}}, nil
					}
					if err := lifecycle.New(root).Restore(pos[0]); err != nil {
						return outcome{}, fmt.Errorf("restore lifecycle batch: %w", err)
					}
					return outcome{state: "applied", result: map[string]any{"restored": map[string]any{"recovery_id": pos[0]}}}, nil
				}
				service := recovery.New(root)
				record, err := service.Show(pos[0])
				if err != nil {
					return outcome{}, fmt.Errorf("show Recovery operation: %w", err)
				}
				current, err := recovery.Fingerprint(record.Target)
				if err != nil {
					return outcome{}, fmt.Errorf("fingerprint restore target: %w", err)
				}
				if *restoreExpect != "" && *restoreExpect != current {
					return outcome{}, fmt.Errorf("BLOCKED: target fingerprint changed or --expect is missing; expected %s", current)
				}
				restored := map[string]any{"recovery_id": pos[0], "target": record.Target}
				if *restoreDryRun {
					return outcome{command: "recovery restore", state: "planned", result: map[string]any{"restored": restored}}, nil
				}
				operation, err := service.Restore(pos[0])
				if err != nil {
					return outcome{}, fmt.Errorf("restore Recovery operation: %w", err)
				}
				if err := harness.NewRegistry(root).ForgetProjection(record.Target); err != nil {
					return outcome{}, fmt.Errorf("forget Projection: %w", err)
				}
				restored["operation_id"] = operation.ID
				return outcome{command: "recovery restore", state: "applied", result: map[string]any{"restored": restored}}, nil
			}
		}),
	})
}

// contentNode builds the `skill` or `instruction` command group. word is the
// Caller-facing singular ("skill"/"instruction"); kind is the internal artifact
// kind. The two groups carry the same verbs; only `add`'s arity and a few help
// strings differ.
func contentNode(word, kind string) *node {
	plural := word + "s"
	titlePlural := strings.ToUpper(word[:1]) + word[1:] + "s"

	addMin, addMax, addHint := 1, -1, "<path>..."
	if kind == artifact.KindInstruction {
		addMin, addMax, addHint = 1, 1, "<path>"
	}

	return group(fmt.Sprintf("Capture and inspect canonical %s. A reference is namespace/name, for example main/release-notes; the default namespace is main.", titlePlural), map[string]*node{
		"try":   tryNode(word, kind),
		"temps": tempsNode(word, kind),
		"add": command(fmt.Sprintf("Capture one or more local %s as immutable canonical revisions.", titlePlural), addHint, addMin, addMax, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " add")
			var namespace, name string
			set.StringVar(&namespace, "namespace", "main", "destination Namespace")
			set.StringVar(&name, "name", "", fmt.Sprintf("canonical %s name (single source only)", word))
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if set.Changed("name") && len(pos) != 1 {
					return outcome{}, domainErrorf("--name cannot rename %d sources at once; add them one path at a time", len(pos))
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				store := artifact.NewStore(root)
				options := artifact.CaptureOptions{Namespace: namespace, Name: name, ExplicitName: set.Changed("name")}

				if kind == artifact.KindInstruction {
					revision, err := store.CaptureInstructions(pos[0], options)
					if err != nil {
						return outcome{}, domainErrorf("capture %s %s: %w", word, pos[0], err)
					}
					return outcome{command: word + " add", state: "applied", result: map[string]any{
						"revisions": []map[string]any{contentRef(revision.Selector, revision.Digest)},
					}}, nil
				}

				sources, err := expandSkillSources(pos)
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				if len(sources) > 1 && set.Changed("name") {
					return outcome{}, domainErrorf("--name cannot rename %d Skills at once; add them one path at a time", len(sources))
				}
				captured := make([]artifact.Revision, 0, len(sources))
				for _, source := range sources {
					revision, err := store.CaptureSkill(source, options)
					if err != nil {
						if len(captured) > 0 {
							return outcome{}, domainErrorf("capture %s %s (after capturing %s): %w", word, source, capturedRefs(captured), err)
						}
						return outcome{}, domainErrorf("capture %s %s: %w", word, source, err)
					}
					captured = append(captured, revision)
				}
				noteUntrackedSources(stderr, root, captured, sources)
				revisions := make([]map[string]any, 0, len(captured))
				for _, revision := range captured {
					revisions = append(revisions, contentRef(revision.Selector, revision.Digest))
				}
				return outcome{command: word + " add", state: "applied", result: map[string]any{"revisions": revisions}}, nil
			}
		}),
		"list": command(fmt.Sprintf("List canonical %s and their selected Revisions.", titlePlural), "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " list")
			listNamespace := set.String("namespace", "", "filter by Namespace")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				revisions, err := artifact.NewStore(root).List(artifact.ListOptions{Namespace: *listNamespace, Kind: kind})
				if err != nil {
					return outcome{}, fmt.Errorf("list %s: %w", plural, err)
				}
				entries := make([]map[string]any, 0, len(revisions))
				for _, revision := range revisions {
					entries = append(entries, contentRef(revision.Selector, revision.Digest))
				}
				return outcome{command: word + " list", state: "clean", result: map[string]any{plural: entries}}, nil
			}
		}),
		"select": command(fmt.Sprintf("Select an already stored %s Revision.", word), "<namespace/name>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " select")
			selectRevision := set.String("revision", "", "stored Revision digest")
			selectDryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if *selectRevision == "" {
					return outcome{}, domainErrorf("select requires --revision sha256-<hex>")
				}
				key, err := artifact.Key(kind, pos[0])
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				store := artifact.NewStore(root)
				resolve := store.Select
				if *selectDryRun {
					resolve = store.VerifyRevision
				}
				revision, err := resolve(key, *selectRevision)
				if err != nil {
					return outcome{}, fmt.Errorf("select %s: %w", word, err)
				}
				return outcome{command: word + " select", state: mutationState(*selectDryRun), result: map[string]any{
					"selected": contentRef(revision.Selector, revision.Digest),
				}}, nil
			}
		}),
		"promote": command(fmt.Sprintf("Promote one imported %s Revision to main.", word), "<namespace/name>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " promote")
			promoteRevision := set.String("revision", "", "imported Revision digest")
			promoteDryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				key, err := artifact.Key(kind, pos[0])
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				store := artifact.NewStore(root)
				revisionDigest := *promoteRevision
				if revisionDigest == "" {
					revisionDigest, err = soleRevision(store, key)
					if err != nil {
						return outcome{}, err
					}
				}
				resolve := store.Promote
				if *promoteDryRun {
					resolve = store.VerifyRevision
				}
				revision, err := resolve(key, revisionDigest)
				if err != nil {
					return outcome{}, fmt.Errorf("promote %s: %w", word, err)
				}
				return outcome{command: word + " promote", state: mutationState(*promoteDryRun), result: map[string]any{
					"promoted": map[string]any{
						"kind":   word,
						"ref":    "main/" + refName(pos[0]),
						"digest": revision.Digest,
						"origin": map[string]any{"ref": artifact.DisplayRef(key), "revision": revision.Digest},
					},
				}}, nil
			}
		}),
		"delete": command(fmt.Sprintf("Delete one %s and its managed projections.", word), "<namespace/name>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " delete")
			purge := set.Bool("purge", false, "delete without Recovery")
			dryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				key, err := artifact.Key(kind, pos[0])
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				store, registry := artifact.NewStore(root), harness.NewRegistry(root)
				if _, err := store.Selected(key); err != nil {
					return outcome{}, err
				}
				canonicalPath, err := store.ArtifactPath(key)
				if err != nil {
					return outcome{}, err
				}
				projections, err := registry.ListProjections()
				if err != nil {
					return outcome{}, err
				}
				owned := []harness.Projection{}
				for _, projection := range projections {
					if projection.Artifact == key {
						owned = append(owned, projection)
						if !*purge {
							matches, err := projectionFingerprintMatches(projection.Path, projection.Fingerprint)
							if err != nil {
								return outcome{}, err
							}
							if !matches && !mustBeMissing(projection.Path) {
								return outcome{}, domainErrorf("BLOCKED: Drift %s; rerun with --purge to delete it permanently", projection.Path)
							}
						}
					}
				}
				targets := append([]string{canonicalPath, filepath.Join(root, "projections.toml")}, projectionPaths(owned)...)
				if *dryRun {
					return outcome{state: "planned", result: map[string]any{"deleted": pos[0], "projections": len(owned), "purge": *purge, "recovery": !*purge, "targets": targets}}, nil
				}
				apply := func() error {
					if err := os.RemoveAll(canonicalPath); err != nil {
						return err
					}
					for _, projection := range owned {
						if err := os.RemoveAll(projection.Path); err != nil {
							return err
						}
					}
					_, err := registry.ForgetArtifact(key)
					return err
				}
				result := map[string]any{"deleted": pos[0], "projections": len(owned), "purge": *purge}
				if *purge {
					if err := apply(); err != nil {
						return outcome{}, err
					}
				} else {
					batch, err := lifecycle.New(root).Apply(append([]lifecycle.Target{{Path: canonicalPath}, {Path: filepath.Join(root, "projections.toml")}}, lifecycleTargets(owned)...), apply)
					if err != nil {
						return outcome{}, lifecyclePartialError(batch, err)
					}
					result["recovery_id"] = batch.ID
				}
				return outcome{state: "applied", result: result}, nil
			}
		}),
		"show": command(fmt.Sprintf("Show metadata for the selected canonical %s revision. Pass --files to print the selected canonical text files.", word), "<namespace/name>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby " + word + " show")
			showFiles := set.Bool("files", false, "print selected canonical text files")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				key, err := artifact.Key(kind, pos[0])
				if err != nil {
					return outcome{}, asDomainError(err)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				store := artifact.NewStore(root)
				revision, err := store.Selected(key)
				if err != nil {
					return outcome{}, fmt.Errorf("show %s: %w", word, err)
				}
				origin, err := store.Origin(key)
				if err != nil {
					return outcome{}, fmt.Errorf("show %s origin: %w", word, err)
				}
				revisionResult := contentRef(revision.Selector, revision.Digest)
				if origin.Selector != "" {
					revisionResult["origin"] = map[string]any{"ref": artifact.DisplayRef(origin.Selector), "revision": origin.Revision}
				}
				result := map[string]any{"revision": revisionResult}
				if *showFiles {
					_, filesPath, err := store.SelectedContentFilesPath(key)
					if err != nil {
						return outcome{}, fmt.Errorf("show %s files: %w", word, err)
					}
					files, err := artifactFiles(filesPath)
					if err != nil {
						return outcome{}, fmt.Errorf("read %s files: %w", word, err)
					}
					result["files"] = files
				}
				return outcome{command: word + " show", state: "clean", result: result}, nil
			}
		}),
	})
}

// artifactFiles reads the selected Revision's runtime tree into sorted
// {bytes, contents, path} objects. filepath.WalkDir visits lexically, so the
// slice is already ordered by path.
func artifactFiles(root string) ([]map[string]any, error) {
	files := []map[string]any{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, map[string]any{
			"path":     filepath.ToSlash(relative),
			"bytes":    len(contents),
			"contents": string(contents),
		})
		return nil
	})
	return files, err
}

func harnessNode() *node {
	return group("Discover, link, inspect, and synchronize Harnesses.", map[string]*node{
		"discover": command("Discover supported user-level Harness installations.", "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby harness discover")
			harnessName := set.String("harness", "", "filter by supported Harness")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				candidates, err := discoverBuiltinCandidates()
				if err != nil {
					return outcome{}, err
				}
				matched := false
				found := []map[string]any{}
				for _, candidate := range candidates {
					if *harnessName != "" && candidate.candidate.Name != *harnessName {
						continue
					}
					matched = true
					if !candidate.found {
						continue
					}
					found = append(found, map[string]any{
						"id":          candidate.candidate.ID,
						"harness":     candidate.candidate.Name,
						"skills_path": candidate.candidate.SkillsPath,
					})
				}
				if *harnessName != "" && !matched {
					return outcome{}, fmt.Errorf("unsupported harness %q", *harnessName)
				}
				return outcome{command: "harness discover", state: "clean", result: map[string]any{"candidates": found}}, nil
			}
		}),
		"link": command("Link a discovered Harness installation.", "<candidate-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby harness link")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				candidates, err := discoverBuiltinCandidates()
				if err != nil {
					return outcome{}, err
				}
				var candidate discoveredCandidate
				foundCandidate := false
				for _, item := range candidates {
					if item.candidate.ID == pos[0] {
						candidate = item
						foundCandidate = true
						break
					}
				}
				if !foundCandidate {
					return outcome{}, fmt.Errorf("unknown Harness candidate %q; run 'brigsby harness discover'", pos[0])
				}
				if !candidate.found {
					return outcome{}, fmt.Errorf("%s candidate %q is not currently installed at %s", candidate.candidate.Name, candidate.candidate.ID, candidate.candidate.SkillsPath)
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				if err := harness.NewRegistry(root).Link(candidate.candidate); err != nil {
					return outcome{}, fmt.Errorf("link Harness: %w", err)
				}
				return outcome{command: "harness link", state: "applied", result: map[string]any{
					"linked": map[string]any{
						"id":          candidate.candidate.ID,
						"skills_path": candidate.candidate.SkillsPath,
					},
				}}, nil
			}
		}),
		"unlink": command("Remove a linked Harness association without deleting its files.", "<linked-installation-id>", 1, 1, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby harness unlink")
			unlinkDryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				registry := harness.NewRegistry(root)
				linked, err := registry.List()
				if err != nil {
					return outcome{}, fmt.Errorf("read linked Harnesses: %w", err)
				}
				found := false
				for _, candidate := range linked {
					if candidate.ID == pos[0] {
						found = true
						break
					}
				}
				if !found {
					return outcome{}, fmt.Errorf("linked Harness %q was not found", pos[0])
				}
				if !*unlinkDryRun {
					if err := registry.Unlink(pos[0]); err != nil {
						return outcome{}, fmt.Errorf("unlink Harness: %w", err)
					}
				}
				return outcome{command: "harness unlink", state: mutationState(*unlinkDryRun), result: map[string]any{"unlinked": pos[0]}}, nil
			}
		}),
		"status": command("Show linked Harness Projections, Drift, and Unowned paths.", "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby harness status")
			statusHarness := set.String("harness", "", "filter by linked Harness installation ID")
			statusManaged := set.Bool("managed", false, "deprecated: managed status is the default")
			statusUnowned := set.Bool("unowned", false, "report only Unowned paths")
			statusAll := set.Bool("all", false, "include managed Projections, Drift, and Unowned paths")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				if *statusUnowned && (*statusManaged || *statusAll) {
					return outcome{}, domainErrorf("--unowned cannot be combined with --managed or --all")
				}
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				registry := harness.NewRegistry(root)
				linked, err := registry.List()
				if err != nil {
					return outcome{}, fmt.Errorf("read linked Harnesses: %w", err)
				}
				if *statusHarness != "" {
					filtered := linked[:0]
					for _, candidate := range linked {
						if candidate.ID == *statusHarness {
							filtered = append(filtered, candidate)
						}
					}
					if len(filtered) == 0 {
						return outcome{}, fmt.Errorf("linked Harness %q was not found", *statusHarness)
					}
					linked = filtered
				}
				projections, err := registry.ListProjections()
				if err != nil {
					return outcome{}, fmt.Errorf("read Projections: %w", err)
				}

				harnessesResult := map[string]any{}
				problems := []map[string]any{}
				driftCount, missingCount, staleCount, unownedCount := 0, 0, 0, 0
				showManaged := !*statusUnowned
				showUnowned := *statusUnowned || *statusAll

				for _, candidate := range linked {
					harnessResult := map[string]any{"name": candidate.Name, "skills_path": candidate.SkillsPath, "projections": map[string]any{}}
					harnessesResult[candidate.ID] = harnessResult
					projectionResult := harnessResult["projections"].(map[string]any)
					owned := map[string]struct{}{}
					for _, projection := range projections {
						if projection.HarnessID != candidate.ID {
							continue
						}
						owned[projection.Path] = struct{}{}
						if !showManaged {
							continue
						}
						missing, err := projectionMissing(projection.Path)
						if err != nil {
							return outcome{}, fmt.Errorf("inspect projected %s: %w", projection.Path, err)
						}
						matches := false
						if !missing {
							matches, err = projectionFingerprintMatches(projection.Path, projection.Fingerprint)
							if err != nil {
								return outcome{}, fmt.Errorf("fingerprint Projection: %w", err)
							}
						}
						selected, _, err := artifact.NewStore(root).SelectedContentFilesPath(projection.Artifact)
						if err != nil {
							return outcome{}, projectionStateErrorf("canonical %s is unavailable: %w", candidate.ID, projection, displayContent(projection.Artifact), err)
						}
						entry := map[string]any{
							"revision": projection.Revision,
							"path":     projection.Path,
						}
						switch {
						case missing:
							missingCount++
							entry["status"] = "missing"
							problem := projectionProblem(fmt.Sprintf("missing-%02d", missingCount), "projection_missing", fmt.Sprintf("Projected %s is missing from %s at %s.", displayContent(projection.Artifact), candidate.ID, projection.Path), candidate.ID, projection)
							problem["remedy"] = projectionRemedy(projection, candidate.ID)
							entry["problem"] = problem
							problems = append(problems, problem)
						case matches && selected.Digest == projection.Revision:
							entry["status"] = "projected"
						case err == nil && matches:
							staleCount++
							entry["status"] = "stale"
							problem := projectionProblem(fmt.Sprintf("stale-%02d", staleCount), "projection_stale", fmt.Sprintf("Projected %s in %s at %s is stale but unchanged.", displayContent(projection.Artifact), candidate.ID, projection.Path), candidate.ID, projection)
							if artifact.KeyKind(projection.Artifact) == artifact.KindSkill {
								problem["remedy"] = projectionRemedy(projection, candidate.ID)
							} else {
								problem["message"] = fmt.Sprintf("Projected %s in %s at %s is stale but unchanged. Protected instruction files need review before a force sync.", displayContent(projection.Artifact), candidate.ID, projection.Path)
							}
							entry["problem"] = problem
							problems = append(problems, problem)
						default:
							driftCount++
							entry["status"] = "drift"
							problem := projectionProblem(fmt.Sprintf("drift-%02d", driftCount), "projection_drift", fmt.Sprintf("Projected %s in %s at %s differs from its recorded content. Inspect it before replacing it.", displayContent(projection.Artifact), candidate.ID, projection.Path), candidate.ID, projection)
							entry["problem"] = problem
							problems = append(problems, problem)
						}
						projectionResult[projectionStatusKey(projection.Artifact)] = entry
					}
					if !showUnowned {
						continue
					}
					unowned, err := unownedSkills(candidate.SkillsPath, owned)
					if err != nil {
						return outcome{}, err
					}
					unownedResult := map[string]any{}
					for _, path := range unowned {
						unownedCount++
						problem := map[string]any{
							"id":      fmt.Sprintf("unowned-%02d", unownedCount),
							"code":    "unowned_path",
							"harness": candidate.ID,
							"kind":    "skill",
							"path":    path,
							"message": fmt.Sprintf("Skill path %s in %s is not managed by Brigsby.", path, candidate.ID),
						}
						unownedResult[path] = problem
						problems = append(problems, problem)
					}
					if len(unownedResult) > 0 {
						harnessResult["unowned"] = unownedResult
					}
				}

				state := "clean"
				switch {
				case driftCount > 0:
					state = "drifted"
				case missingCount > 0:
					state = "missing"
				case staleCount > 0:
					state = "stale"
				case unownedCount > 0:
					state = "unowned"
				}
				return outcome{
					command:  "harness status",
					state:    state,
					problems: problems,
					result:   map[string]any{"harnesses": harnessesResult},
				}, nil
			}
		}),
		"sync": command("Safely project selected canonical content to linked Harnesses. Select with --skill namespace/name and --instruction namespace/name; with neither, every selection in main is projected.", "", 0, 0, func() (*pflag.FlagSet, leaf) {
			set := newFlagSet("brigsby harness sync")
			var linkedIDs, skillRefs, instructionRefs []string
			set.StringArrayVar(&linkedIDs, "harness", nil, "linked Harness installation ID (repeatable)")
			set.StringArrayVar(&skillRefs, "skill", nil, "selected Skill reference namespace/name (repeatable)")
			set.StringArrayVar(&instructionRefs, "instruction", nil, "selected Instruction reference namespace/name (repeatable)")
			force := set.Bool("force", false, "replace a single narrowed target that differs from canonical content")
			dryRun := set.Bool("dry-run", false, "preview without writing")
			return set, func(path string, pos []string, stderr io.Writer) (outcome, error) {
				root, err := brigsbyHome()
				if err != nil {
					return outcome{}, err
				}
				registry := harness.NewRegistry(root)
				linked, err := registry.List()
				if err != nil {
					return outcome{}, fmt.Errorf("read linked Harnesses: %w", err)
				}
				var selectors []string
				for _, grp := range []struct {
					kind string
					refs []string
				}{{artifact.KindSkill, skillRefs}, {artifact.KindInstruction, instructionRefs}} {
					for _, ref := range grp.refs {
						key, err := artifact.Key(grp.kind, ref)
						if err != nil {
							return outcome{}, asDomainError(err)
						}
						selectors = append(selectors, key)
					}
				}
				targets, err := preflightSync(root, registry, linked, linkedIDs, selectors, *force)
				if err != nil {
					return outcome{}, err
				}
				defer cleanupSyncTargets(targets)
				if *dryRun {
					return syncOutcome("planned", targets, nil), nil
				}
				var operations []recovery.Operation
				for _, target := range targets {
					if target.removal {
						operation, err := recovery.New(root).Apply(target.plan)
						if err != nil {
							return outcome{}, partialSyncError(operations, target, err)
						}
						operations = append(operations, operation)
						if err := registry.ForgetProjection(target.path); err != nil {
							return outcome{}, partialSyncError(operations, target, fmt.Errorf("forget migrated Projection: %w", err))
						}
						continue
					}
					if target.plan.TargetFingerprint() != target.plan.ReplacementFingerprint() {
						operation, err := recovery.New(root).Apply(target.plan)
						if err != nil {
							return outcome{}, partialSyncError(operations, target, err)
						}
						operations = append(operations, operation)
					}
					contentFingerprint, err := recovery.ContentFingerprint(target.path)
					if err != nil {
						return outcome{}, partialSyncError(operations, target, fmt.Errorf("fingerprint projected content: %w", err))
					}
					if err := registry.RecordProjection(harness.Projection{HarnessID: target.harness.ID, Path: target.path, Artifact: target.revision.Selector, Revision: target.revision.Digest, Fingerprint: contentFingerprint}); err != nil {
						return outcome{}, partialSyncError(operations, target, fmt.Errorf("record Projection: %w", err))
					}
				}
				if len(operations) == 0 {
					return syncOutcome("clean", targets, nil), nil
				}
				return syncOutcome("applied", targets, operations), nil
			}
		}),
	})
}

type syncTarget struct {
	harness  harness.Candidate
	revision artifact.Revision
	path     string
	plan     recovery.Plan
	cleanup  func()
	removal  bool
}

func preflightSync(root string, registry harness.Registry, linked []harness.Candidate, requestedHarnesses, requestedArtifacts []string, force bool) (targets []syncTarget, err error) {
	success := false
	var cleanups []func()
	defer func() {
		if !success {
			for _, cleanup := range cleanups {
				cleanup()
			}
		}
	}()
	selectedHarnesses, err := selectHarnesses(linked, requestedHarnesses)
	if err != nil {
		return nil, err
	}
	store := artifact.NewStore(root)
	if len(requestedArtifacts) == 0 {
		available, err := store.List(artifact.ListOptions{Namespace: "main"})
		if err != nil {
			return nil, fmt.Errorf("list selected Artifacts: %w", err)
		}
		for _, revision := range available {
			requestedArtifacts = append(requestedArtifacts, revision.Selector)
		}
	}
	if len(selectedHarnesses) == 0 || len(requestedArtifacts) == 0 {
		return nil, fmt.Errorf("sync has no selected Harnesses or Artifacts")
	}
	projections, err := registry.ListProjections()
	if err != nil {
		return nil, fmt.Errorf("read Projections: %w", err)
	}
	instructionCount := 0
	for _, selector := range requestedArtifacts {
		if parts := strings.Split(selector, "/"); len(parts) == 3 && parts[1] == "instructions" {
			instructionCount++
		}
	}
	if instructionCount > 1 {
		return nil, fmt.Errorf("sync selects multiple global Instructions; narrow --instruction to one so Brigsby never merges native root files")
	}
	seenTargets := make(map[string]struct{})
	for _, selector := range requestedArtifacts {
		parts := strings.Split(selector, "/")
		if len(parts) == 3 && parts[1] == "instructions" {
			if _, err := store.Selected(selector); err != nil {
				return nil, fmt.Errorf("read selected Artifact: %w", err)
			}
			var instructionTargets []syncTarget
			changed := false
			for _, targetHarness := range selectedHarnesses {
				rendered, err := store.RenderSelectedInstructions(selector, targetHarness.Name)
				if err != nil {
					return nil, fmt.Errorf("render Instruction Projection: %w", err)
				}
				cleanups = append(cleanups, rendered.Cleanup)
				rootTarget, err := instructionRootPath(targetHarness)
				if err != nil {
					return nil, err
				}
				treeTarget := filepath.Join(targetHarness.InstructionsPath, "brigsby", parts[2])
				for _, item := range []struct{ target, source string }{{rootTarget, rendered.RootPath}, {treeTarget, rendered.TreePath}} {
					if _, duplicate := seenTargets[item.target]; duplicate {
						return nil, fmt.Errorf("sync target collision at %s", item.target)
					}
					seenTargets[item.target] = struct{}{}
					plan, err := recovery.New(root).Plan(item.target, item.source)
					if err != nil {
						return nil, fmt.Errorf("plan Instruction Projection: %w", err)
					}
					targetContent, err := recovery.ContentFingerprint(item.target)
					if err != nil {
						return nil, fmt.Errorf("fingerprint Instruction Projection content: %w", err)
					}
					replacementContent, err := recovery.ContentFingerprint(item.source)
					if err != nil {
						return nil, fmt.Errorf("fingerprint rendered Instruction content: %w", err)
					}
					if targetContent != "absent" && targetContent != replacementContent {
						changed = true
					}
					instructionTargets = append(instructionTargets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: item.target, plan: plan, cleanup: rendered.Cleanup})
				}
			}
			if changed && (len(selectedHarnesses) != 1 || len(requestedArtifacts) != 1 || !force) {
				return nil, fmt.Errorf("BLOCKED: Instruction Projection differs from %s; narrow --harness and --instruction to one target and rerun with --force", renderedInstructionPaths(instructionTargets))
			}
			targets = append(targets, instructionTargets...)
			continue
		}
		rendered, err := store.RenderSelectedSkill(selector)
		if err != nil {
			return nil, fmt.Errorf("read selected Artifact: %w", err)
		}
		cleanups = append(cleanups, rendered.Cleanup)
		name := rendered.Name
		for _, targetHarness := range selectedHarnesses {
			target := filepath.Join(targetHarness.SkillsPath, name)
			if _, duplicate := seenTargets[target]; duplicate {
				return nil, fmt.Errorf("sync target collision at %s", target)
			}
			seenTargets[target] = struct{}{}
			plan, err := recovery.New(root).Plan(target, rendered.FilesPath)
			if err != nil {
				return nil, fmt.Errorf("plan projection: %w", err)
			}
			targetContent, err := recovery.ContentFingerprint(target)
			if err != nil {
				return nil, fmt.Errorf("fingerprint projection content: %w", err)
			}
			replacementContent, err := recovery.ContentFingerprint(rendered.FilesPath)
			if err != nil {
				return nil, fmt.Errorf("fingerprint rendered Skill content: %w", err)
			}
			if targetContent != "absent" && targetContent != replacementContent {
				projection, owned := projectionFor(projections, targetHarness.ID, target)
				stale, err := ownedProjectionIsStale(projection, owned, rendered.Revision, target)
				if err != nil {
					return nil, fmt.Errorf("fingerprint previous Projection: %w", err)
				}
				if stale {
					targets = append(targets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: target, plan: plan, cleanup: rendered.Cleanup})
					continue
				}
				if len(selectedHarnesses) != 1 || len(requestedArtifacts) != 1 {
					return nil, fmt.Errorf("BLOCKED: %s differs from %s; narrow --harness and --skill to one target before force sync", target, artifact.DisplayRef(rendered.Revision.Selector))
				}
				if !force {
					kind := "Unowned path"
					for _, projection := range projections {
						if projection.HarnessID == targetHarness.ID && projection.Path == target {
							kind = "Drift"
							break
						}
					}
					return nil, fmt.Errorf("BLOCKED: %s %s differs from %s; keep with 'brigsby skill add %s --name %s' or rerun with --force", kind, target, artifact.DisplayRef(rendered.Revision.Selector), target, rendered.Name)
				}
			}
			targets = append(targets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: target, plan: plan, cleanup: rendered.Cleanup})
			for _, projection := range projections {
				if projection.HarnessID != targetHarness.ID || projection.Artifact != rendered.Revision.Selector || projection.Path == target {
					continue
				}
				matches, err := projectionFingerprintMatches(projection.Path, projection.Fingerprint)
				if err != nil {
					return nil, fmt.Errorf("fingerprint previous Projection: %w", err)
				}
				if !matches {
					return nil, fmt.Errorf("BLOCKED: previous Projection %s drifted; resolve it before migrating to %s", projection.Path, target)
				}
				removalPlan, err := recovery.PlanRemoval(projection.Path)
				if err != nil {
					return nil, fmt.Errorf("plan previous Projection removal: %w", err)
				}
				targets = append(targets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: projection.Path, plan: removalPlan, removal: true})
			}
		}
	}
	success = true
	return targets, nil
}

// projectionFingerprintMatches accepts the current content fingerprint and the
// legacy exact recovery fingerprint already persisted by earlier Brigsby
// versions. The comparison is explicit: a legacy value remains an exact
// comparison rather than being reinterpreted as content-only state.
func projectionFingerprintMatches(path, expected string) (bool, error) {
	content, err := recovery.ContentFingerprint(path)
	if err != nil {
		return false, err
	}
	if content == expected {
		return true, nil
	}
	exact, err := recovery.Fingerprint(path)
	if err != nil {
		return false, err
	}
	return exact == expected, nil
}

func projectionFor(projections []harness.Projection, harnessID, path string) (harness.Projection, bool) {
	for _, projection := range projections {
		if projection.HarnessID == harnessID && projection.Path == path {
			return projection, true
		}
	}
	return harness.Projection{}, false
}

// ownedProjectionIsStale reports whether the target is exactly the Projection
// Brigsby last wrote, but its Artifact now selects a newer Revision. That is a
// safe fast-forward; any local edit remains drift and must still be guarded.
func ownedProjectionIsStale(projection harness.Projection, owned bool, revision artifact.Revision, path string) (bool, error) {
	if !owned || projection.Artifact != revision.Selector || projection.Revision == revision.Digest {
		return false, nil
	}
	return projectionFingerprintMatches(path, projection.Fingerprint)
}

func renderedInstructionPaths(targets []syncTarget) string {
	paths := make([]string, len(targets))
	for index, target := range targets {
		paths[index] = target.path
	}
	return strings.Join(paths, ",")
}

func instructionRootPath(candidate harness.Candidate) (string, error) {
	if !filepath.IsAbs(candidate.InstructionsPath) {
		return "", fmt.Errorf("linked Harness %q has no structured Instruction location; create instructions.toml beside its native instruction root, then unlink and relink it", candidate.ID)
	}
	if candidate.Name == "claude" {
		return filepath.Join(candidate.InstructionsPath, "CLAUDE.md"), nil
	}
	return filepath.Join(candidate.InstructionsPath, "AGENTS.md"), nil
}

func cleanupSyncTargets(targets []syncTarget) {
	for _, target := range targets {
		if target.cleanup == nil {
			continue
		}
		target.cleanup()
	}
}

func selectHarnesses(linked []harness.Candidate, requested []string) ([]harness.Candidate, error) {
	if len(requested) == 0 {
		return linked, nil
	}
	byID := make(map[string]harness.Candidate, len(linked))
	for _, candidate := range linked {
		byID[candidate.ID] = candidate
	}
	selected := make([]harness.Candidate, 0, len(requested))
	seen := make(map[string]struct{})
	for _, id := range requested {
		candidate, found := byID[id]
		if !found {
			return nil, fmt.Errorf("linked Harness %q was not found", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, candidate)
	}
	return selected, nil
}

func partialSyncError(operations []recovery.Operation, target syncTarget, cause error) error {
	if len(operations) == 0 {
		return fmt.Errorf("FAILED: sync stopped at %s before applying any target: %w", target.path, cause)
	}
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.ID
	}
	return fmt.Errorf("PARTIAL: sync stopped at %s after Recovery operations %s; no automatic rollback was attempted; restore explicitly with 'brigsby recovery restore <id>': %w", target.path, strings.Join(ids, ", "), cause)
}

// syncOutcome renders the sync result envelope: one entry per projected or
// migrated target, with the applied Recovery operation IDs in preview.
func syncOutcome(state string, targets []syncTarget, operations []recovery.Operation) outcome {
	results := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		operation := "project"
		if target.removal {
			operation = "remove_previous_projection"
		}
		results = append(results, map[string]any{
			"harness":   target.harness.ID,
			"kind":      kindWord(target.revision.Selector),
			"ref":       artifact.DisplayRef(target.revision.Selector),
			"revision":  target.revision.Digest,
			"target":    target.path,
			"operation": operation,
		})
	}
	return outcome{
		command:  "harness sync",
		state:    state,
		problems: []any{},
		result:   results,
		preview:  map[string]any{"recovery_ids": recoveryOperationIDs(operations)},
	}
}

func recoveryOperationIDs(operations []recovery.Operation) []string {
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.ID
	}
	return ids
}

func unownedSkills(skillsPath string, owned map[string]struct{}) ([]string, error) {
	entries, err := os.ReadDir(skillsPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Harness skills path: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(skillsPath, entry.Name())
		if _, claimed := owned[path]; claimed {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect Skill path: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// expandSkillSources resolves each add path to one or more concrete Skill
// directories. A path that itself holds a root-level SKILL.md is one Skill; any
// other directory contributes each immediate subdirectory that holds one, so a
// Caller can capture a whole Harness skills tree in one command. Order follows
// the arguments, then lexical directory order; duplicates collapse.
func expandSkillSources(paths []string) ([]string, error) {
	seen := make(map[string]struct{})
	var sources []string
	appendSource := func(path string) {
		if _, duplicate := seen[path]; duplicate {
			return
		}
		seen[path] = struct{}{}
		sources = append(sources, path)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect Skill source %s: %w", path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("Skill source must be a directory: %s", path)
		}
		if hasSkillFile(path) {
			appendSource(filepath.Clean(path))
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("read Skill directory %s: %w", path, err)
		}
		matched := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			child := filepath.Join(path, entry.Name())
			if hasSkillFile(child) {
				appendSource(filepath.Clean(child))
				matched++
			}
		}
		if matched == 0 {
			return nil, fmt.Errorf("%s has no root-level SKILL.md and no immediate subdirectory containing one", path)
		}
	}
	return sources, nil
}

func hasSkillFile(directory string) bool {
	info, err := os.Stat(filepath.Join(directory, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

// noteUntrackedSources tells the Caller when a freshly captured Skill source
// lives inside a linked Harness's own skills path but is not yet a recorded
// Projection, so `status` will not report drift against it. `add` copies into
// canonical state; it never claims the source path. The advisory goes to
// stderr so it never enters stdout or JSON output.
func noteUntrackedSources(stderr io.Writer, root string, captured []artifact.Revision, sources []string) {
	if len(captured) != len(sources) {
		return
	}
	registry := harness.NewRegistry(root)
	linked, err := registry.List()
	if err != nil || len(linked) == 0 {
		return
	}
	projections, err := registry.ListProjections()
	if err != nil {
		return
	}
	tracked := make(map[string]struct{}, len(projections))
	for _, projection := range projections {
		tracked[projection.Path] = struct{}{}
	}
	for index, source := range sources {
		absSource, err := filepath.Abs(source)
		if err != nil {
			continue
		}
		if _, ok := tracked[absSource]; ok {
			continue
		}
		for _, candidate := range linked {
			if filepath.Dir(absSource) != filepath.Clean(candidate.SkillsPath) {
				continue
			}
			fmt.Fprintf(stderr,
				"NOTE %s sits in the %s Harness skills path but is not drift-tracked; run 'brigsby sync --skill %s --harness %s' to project and track it\n",
				absSource, candidate.Name, artifact.DisplayRef(captured[index].Selector), candidate.ID)
			break
		}
	}
}

// soleRevision resolves the Revision digest for a Skill or Instruction when the
// Caller did not pass --revision. It succeeds only when exactly one Revision is
// stored. key is the internal store key "namespace/kind/name".
func soleRevision(store artifact.Store, key string) (string, error) {
	namespace := strings.Split(key, "/")[0]
	ref := artifact.DisplayRef(key)
	digests, err := store.Revisions(key)
	if err != nil {
		if os.IsNotExist(err) {
			return "", domainErrorf("no stored Revisions for %s; run 'brigsby %s list --namespace %s'", ref, kindWord(key), namespace)
		}
		return "", fmt.Errorf("read stored Revisions: %w", err)
	}
	switch len(digests) {
	case 0:
		return "", domainErrorf("no stored Revisions for %s; run 'brigsby %s list --namespace %s'", ref, kindWord(key), namespace)
	case 1:
		return digests[0], nil
	default:
		return "", domainErrorf("%s has %d stored Revisions; pass --revision <sha256-hex> (stored: %s)", ref, len(digests), strings.Join(digests, ", "))
	}
}

type discoveredCandidate struct {
	candidate harness.Candidate
	found     bool
}

func discoverBuiltinCandidates() ([]discoveredCandidate, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find user home: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	} else if !filepath.IsAbs(configHome) {
		return nil, fmt.Errorf("XDG_CONFIG_HOME must be an absolute path")
	}
	known := []harness.Candidate{
		{ID: "codex", Name: "codex", SkillsPath: filepath.Join(home, ".agents", "skills"), InstructionsPath: structuredInstructionPath(filepath.Join(home, ".codex"))},
		{ID: "claude", Name: "claude", SkillsPath: filepath.Join(home, ".claude", "skills"), InstructionsPath: structuredInstructionPath(filepath.Join(home, ".claude"))},
		{ID: "opencode", Name: "opencode", SkillsPath: filepath.Join(configHome, "opencode", "skills"), InstructionsPath: structuredInstructionPath(filepath.Join(configHome, "opencode"))},
	}
	discovered := make([]discoveredCandidate, 0, len(known))
	for _, candidate := range known {
		info, err := os.Stat(candidate.SkillsPath)
		if os.IsNotExist(err) {
			discovered = append(discovered, discoveredCandidate{candidate: candidate})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s skills path: %w", candidate.Name, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s skills path is not a directory: %s", candidate.Name, candidate.SkillsPath)
		}
		discovered = append(discovered, discoveredCandidate{candidate: candidate, found: true})
	}
	return discovered, nil
}

func structuredInstructionPath(path string) string {
	info, err := os.Stat(filepath.Join(path, "instructions.toml"))
	if err != nil || info.IsDir() {
		return ""
	}
	return path
}

func brigsbyHome() (string, error) {
	if configured := os.Getenv("BRIGSBY_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("BRIGSBY_HOME must be an absolute path")
		}
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home: %w", err)
	}
	return filepath.Join(home, ".brigsby"), nil
}
