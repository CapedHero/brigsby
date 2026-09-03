package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/CapedHero/brigsby/internal/artifact"
	"github.com/CapedHero/brigsby/internal/harness"
	"github.com/CapedHero/brigsby/internal/lifecycle"
	"github.com/CapedHero/brigsby/internal/portable"
	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
)

var errMachineInvalid = errors.New("invalid machine output request")

// domainError is an actionable failure discovered while executing a valid
// command. Unlike Cobra argument and flag errors, it should not be buried
// beneath root help text.
type domainError struct{ err error }

func (err domainError) Error() string { return err.err.Error() }

func (err domainError) Unwrap() error { return err.err }

func domainErrorf(format string, arguments ...any) error {
	return domainError{err: fmt.Errorf(format, arguments...)}
}

func asDomainError(err error) error {
	if err == nil {
		return nil
	}
	return domainError{err: err}
}

// stateError identifies unreadable canonical state. It is distinct from a
// request error and from projection drift: the caller did not change the
// projected content, Brigsby's own recorded source cannot be trusted.
type stateError struct {
	err     error
	context map[string]any
}

// partialError means a valid lifecycle command changed some state but left one
// persisted recovery batch that can restore the entire pre-command snapshot.
type partialError struct {
	batchID string
	err     error
}

func (err partialError) Error() string {
	return fmt.Sprintf("PARTIAL: lifecycle batch %s can be restored with 'brigsby recovery restore %s': %v", err.batchID, err.batchID, err.err)
}
func (err partialError) Unwrap() error { return err.err }

func (err stateError) Error() string { return err.err.Error() }

func (err stateError) Unwrap() error { return err.err }

func stateErrorf(format string, arguments ...any) error {
	return stateError{err: fmt.Errorf(format, arguments...)}
}

func isStateError(err error) bool {
	var state stateError
	return errors.As(err, &state)
}

func projectionStateErrorf(format string, harnessID string, projection harness.Projection, arguments ...any) error {
	return stateError{err: fmt.Errorf(format, arguments...), context: projectionProblemFields(harnessID, projection)}
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

// resolveVersion is memoized: newRootCommand runs once per alias invocation and
// on the benchmarked help path, so the debug.ReadBuildInfo read must not repeat.
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
	root := newRootCommand(stdout, stderr)

	if wantsPlainText(arguments) {
		root.SetArgs(arguments)
		if err := root.Execute(); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}

	var commandOutput bytes.Buffer
	root.SetOut(&commandOutput)
	root.SetArgs(arguments)
	err := root.Execute()
	jqExpression, _ := root.PersistentFlags().GetString("jq")

	// stdout always carries exactly one JSON envelope -- the machine contract.
	result := machineResult(commandOutput.String(), err)
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
	root.SetOut(stderr)
	if helpErr := root.Help(); helpErr != nil {
		fmt.Fprintln(stderr, helpErr)
	}
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

func isBlockedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BLOCKED:")
}

func isDomainError(err error) bool {
	var domain domainError
	return isPartialError(err) || isBlockedError(err) || isStateError(err) || errors.As(err, &domain)
}

func isPartialError(err error) bool {
	var partial partialError
	return errors.As(err, &partial)
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

// machineResult turns one command execution into the canonical envelope. A
// command that ran to completion has already written its own
// {state,problems,result} object to output via emitResult; this parses it and
// guarantees those keys are present. Only a failure that
// happened before the command could emit -- an unknown command, a bad flag, or
// a domain/BLOCKED error returned from RunE -- needs an envelope synthesised
// here.
func machineResult(output string, commandErr error) map[string]any {
	result := map[string]any{
		"state":    "clean",
		"problems": []any{},
		"result":   nil,
	}
	if commandErr == nil && json.Unmarshal([]byte(output), &result) == nil {
		if _, found := result["state"]; !found {
			result["state"] = "clean"
		}
		for _, key := range []string{"problems", "result"} {
			if _, found := result[key]; !found {
				if key == "problems" {
					result[key] = []any{}
				} else {
					result[key] = nil
				}
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
	case isStateError(commandErr):
		code = "state_error"
	case isDomainError(commandErr):
		code = "domain_error"
	}
	result["state"] = state
	if commandErr != nil {
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
	} else if trimmed := strings.TrimSpace(output); trimmed != "" {
		result["result"] = map[string]any{"output": trimmed}
	}
	return result
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
	if recoveryIDs, found := result["recovery_ids"]; found {
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

// emitResult is the ordinary way a command reports success: a canonical
// envelope with no problems. run() re-reads it, applies --jq, and prints the
// sorted, pretty result.
func emitResult(out io.Writer, _ string, state string, result any) error {
	problems := []any{}
	if state == "applied" {
		problems = append(problems, recoveryGCProblems()...)
	}
	return emitEnvelope(out, state, problems, result)
}

// recoveryGCProblems applies retention after a committed mutation. Cleanup can
// fail independently of the mutation, so it is reported without retracting an
// already-applied result or hiding the recovery ID needed to undo it.
func recoveryGCProblems() []any {
	root, err := brigsbyHome()
	if err != nil {
		return []any{map[string]any{"code": "gc_failed", "message": err.Error()}}
	}
	if _, _, err := pruneRecovery(root, time.Now()); err != nil {
		return []any{map[string]any{"code": "gc_failed", "message": err.Error()}}
	}
	return nil
}

// pruneRecovery enforces one 16 MiB budget across ordinary Recovery bundles
// and multi-path lifecycle batches, rather than granting each store its own
// independent allowance.
func pruneRecovery(root string, now time.Time) (recovery.PruneResult, recovery.PruneResult, error) {
	policy := recovery.DefaultRetention()
	ordinary, err := recovery.New(root).Prune(policy, now)
	if err != nil {
		return recovery.PruneResult{}, recovery.PruneResult{}, fmt.Errorf("garbage collect Recovery: %w", err)
	}
	used, err := recoverableBytes(filepath.Join(root, "recovery"))
	if err != nil {
		return recovery.PruneResult{}, recovery.PruneResult{}, fmt.Errorf("measure Recovery retention: %w", err)
	}
	lifecyclePolicy := policy
	lifecyclePolicy.MaxBytes -= used
	if lifecyclePolicy.MaxBytes < 0 {
		lifecyclePolicy.MaxBytes = 0
	}
	lifecycleResult, err := lifecycle.New(root).Prune(lifecyclePolicy, now)
	if err != nil {
		return recovery.PruneResult{}, recovery.PruneResult{}, fmt.Errorf("garbage collect lifecycle: %w", err)
	}
	return ordinary, lifecycleResult, nil
}

func recoverableBytes(root string) (int64, error) {
	var bytes int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			bytes += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return bytes, err
}

// emitEnvelope is emitResult for the few commands (harness status) that report
// their own problem list alongside a result.
func emitEnvelope(out io.Writer, state string, problems, result any) error {
	return encodeCanonicalJSON(out, map[string]any{
		"state":    state,
		"problems": problems,
		"result":   result,
	})
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

// projectionStatusKey is the stable, kind-qualified key for a projection in
// the status result. A Skill and Instruction may share a display reference.
func projectionStatusKey(key string) string {
	return kindWord(key) + ":" + artifact.DisplayRef(key)
}

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

func contentAddLong(word string) string {
	if word == "instruction" {
		return "Capture one local Instruction set as an immutable canonical revision. The path is a directory holding an AGENTS.md index, an instructions.toml declaration, and every declared Instruction doc."
	}
	return "Capture one or more local Skill directories as immutable canonical revisions. Each path is a directory containing root-level SKILL.md, or a directory whose immediate subdirectories each contain one. Paths may be relative or absolute."
}

// mutationState maps a --dry-run flag to the envelope state a mutation reports.
func mutationState(dryRun bool) string {
	if dryRun {
		return "planned"
	}
	return "applied"
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "brigsby",
		Short:         "Brigsby manages AI coding-agent Artifacts safely.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       resolveVersion(),
		RunE: func(command *cobra.Command, arguments []string) error {
			return command.Help()
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("brigsby {{.Version}}\n")
	root.PersistentFlags().String("jq", "", "filter the JSON result with a jq expression")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the Brigsby version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return emitResult(command.OutOrStdout(), "version", "clean", map[string]any{"version": resolveVersion()})
		},
	})
	root.AddCommand(newHarnessCommand())
	root.AddCommand(newContentCommand("skill", artifact.KindSkill))
	root.AddCommand(newContentCommand("instruction", artifact.KindInstruction))
	root.AddCommand(newNamespaceCommand())
	root.AddCommand(newPackageCommand())
	root.AddCommand(newRecoveryCommand())
	root.AddCommand(newGCCommand())
	root.AddCommand(newStatusRootAlias())
	root.AddCommand(newSyncCommand())
	return root
}

func newGCCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{Use: "gc", Short: "Remove expired Brigsby recovery data.", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		root, err := brigsbyHome()
		if err != nil {
			return err
		}
		if dryRun {
			return emitResult(command.OutOrStdout(), "gc", "planned", map[string]any{"retention_days": 30})
		}
		result, lifecycleResult, err := pruneRecovery(root, time.Now())
		if err != nil {
			return err
		}
		removed := make([]string, len(result.Removed))
		for index, operation := range result.Removed {
			removed[index] = operation.ID
		}
		removed = append(removed, recoveryOperationIDs(lifecycleResult.Removed)...)
		return emitResult(command.OutOrStdout(), "gc", "applied", map[string]any{"reclaimed_bytes": result.ReclaimedBytes + lifecycleResult.ReclaimedBytes, "removed": removed})
	}}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return command
}

func newRootAlias(name string, target []string, short string, configure func(*cobra.Command, *[]string)) *cobra.Command {
	alias := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			aliased := newRootCommand(command.OutOrStdout(), command.ErrOrStderr())
			aliasedArguments := append(append([]string{}, target...), arguments...)
			if configure != nil {
				configure(command, &aliasedArguments)
			}
			aliased.SetArgs(aliasedArguments)
			return aliased.Execute()
		},
	}
	return alias
}

func newStatusRootAlias() *cobra.Command {
	var harnessID string
	var managed, unowned, all bool
	alias := newRootAlias("status", []string{"harness", "status"}, "Report linked Harness state.", func(command *cobra.Command, arguments *[]string) {
		if command.Flags().Changed("harness") {
			*arguments = append(*arguments, "--harness", harnessID)
		}
		if managed {
			*arguments = append(*arguments, "--managed")
		}
		if unowned {
			*arguments = append(*arguments, "--unowned")
		}
		if all {
			*arguments = append(*arguments, "--all")
		}
	})
	alias.Flags().StringVar(&harnessID, "harness", "", "filter by linked Harness installation ID")
	alias.Flags().BoolVar(&managed, "managed", false, "deprecated: managed status is the default")
	alias.Flags().BoolVar(&unowned, "unowned", false, "report only Unowned paths")
	alias.Flags().BoolVar(&all, "all", false, "include managed Projections, Drift, and Unowned paths")
	_ = alias.Flags().MarkHidden("managed")
	return alias
}

func newPackageCommand() *cobra.Command {
	packageCommand := &cobra.Command{Use: "package", Short: "Create and inspect portable text-only Packages.", Long: "Create and inspect portable text-only Packages. In v1 a Package carries Skills only; name them with --skill namespace/name."}
	var output, expect string
	var replace bool
	var skillRefs []string
	create := &cobra.Command{
		Use:   "create --skill <namespace/name> --output <new-path>",
		Short: "Create a portable Package from selected Skills.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			if output == "" {
				return domainErrorf("package create requires --output <absolute-path>")
			}
			if len(skillRefs) == 0 {
				return domainErrorf("package create requires at least one --skill <namespace/name>")
			}
			keys := make([]string, 0, len(skillRefs))
			for _, ref := range skillRefs {
				key, err := artifact.Key(artifact.KindSkill, ref)
				if err != nil {
					return asDomainError(err)
				}
				keys = append(keys, key)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			result, err := portable.Create(root, keys, output, replace, expect)
			if err != nil {
				return err
			}
			return emitResult(command.OutOrStdout(), "package create", "applied", map[string]any{
				"digest": result.Digest,
				"skills": len(result.Artifacts),
				"output": output,
			})
		},
	}
	create.Flags().StringArrayVar(&skillRefs, "skill", nil, "selected Skill reference namespace/name (repeatable)")
	create.Flags().StringVar(&output, "output", "", "absolute output archive path")
	create.Flags().BoolVar(&replace, "replace", false, "replace an existing output guarded by --expect")
	create.Flags().StringVar(&expect, "expect", "", "expected existing-output fingerprint")
	packageCommand.AddCommand(create)
	var expectedDigest string
	inspect := &cobra.Command{
		Use:   "inspect <archive>",
		Short: "Verify a Package without writing.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := portable.Inspect(arguments[0], expectedDigest)
			if err != nil {
				return asDomainError(err)
			}
			skills := make([]map[string]any, 0, len(result.Artifacts))
			for _, revision := range result.Artifacts {
				skills = append(skills, contentRef(revision.Selector, revision.Digest))
			}
			return emitResult(command.OutOrStdout(), "package inspect", "clean", map[string]any{
				"digest": result.Digest,
				"skills": skills,
			})
		},
	}
	inspect.Flags().StringVar(&expectedDigest, "expect-digest", "", "expected Package digest")
	packageCommand.AddCommand(inspect)
	var namespace string
	var importDryRun bool
	importCommand := &cobra.Command{
		Use: "import <archive>", Short: "Verify and inertly import a Package.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			importArtifact := portable.Import
			if importDryRun {
				importArtifact = portable.CheckImport
			}
			revisions, err := importArtifact(root, arguments[0], namespace)
			if err != nil {
				return asDomainError(err)
			}
			imported := make([]map[string]any, 0, len(revisions))
			for _, revision := range revisions {
				imported = append(imported, contentRef(revision.Selector, revision.Digest))
			}
			return emitResult(command.OutOrStdout(), "package import", mutationState(importDryRun), map[string]any{"imported": imported})
		},
	}
	importCommand.Flags().StringVar(&namespace, "namespace", "", "isolated destination Namespace")
	importCommand.Flags().BoolVar(&importDryRun, "dry-run", false, "preview import without writing")
	packageCommand.AddCommand(importCommand)
	return packageCommand
}

func newNamespaceCommand() *cobra.Command {
	namespaceCommand := &cobra.Command{Use: "namespace", Short: "Configure Namespace rendering rules."}
	var dryRun bool
	setPrefix := &cobra.Command{
		Use:   "set-prefix <namespace> <prefix>",
		Short: "Set a Recovery-backed target-facing Skill prefix.",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, arguments []string) error {
			if err := artifact.ValidatePrefix(arguments[0], arguments[1]); err != nil {
				return err
			}
			result := map[string]any{"namespace": arguments[0], "prefix": arguments[1]}
			if !dryRun {
				root, err := brigsbyHome()
				if err != nil {
					return err
				}
				if err := artifact.NewStore(root).SetPrefix(arguments[0], arguments[1]); err != nil {
					return fmt.Errorf("set Namespace prefix: %w", err)
				}
			}
			return emitResult(command.OutOrStdout(), "namespace set-prefix", mutationState(dryRun), result)
		},
	}
	setPrefix.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	namespaceCommand.AddCommand(setPrefix)
	return namespaceCommand
}

func newRecoveryCommand() *cobra.Command {
	recoveryCommand := &cobra.Command{
		Use:   "recovery",
		Short: "Inspect and restore Recovery operations.",
	}
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List Recovery operation bundles.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			records, err := recovery.New(root).List()
			if err != nil {
				return fmt.Errorf("list Recovery operations: %w", err)
			}
			if limit > 0 && len(records) > limit {
				records = records[:limit]
			}
			operations := make([]map[string]any, 0, len(records))
			for _, record := range records {
				operations = append(operations, map[string]any{
					"id":     record.ID,
					"state":  record.State,
					"target": record.Target,
				})
			}
			return emitResult(command.OutOrStdout(), "recovery list", "clean", map[string]any{"operations": operations})
		},
	}
	list.Flags().IntVar(&limit, "limit", 0, "maximum operations to list")
	recoveryCommand.AddCommand(list)
	recoveryCommand.AddCommand(&cobra.Command{
		Use:   "show <recovery-id>",
		Short: "Inspect one Recovery operation bundle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			record, err := recovery.New(root).Show(arguments[0])
			if err != nil {
				return fmt.Errorf("show Recovery operation: %w", err)
			}
			return emitResult(command.OutOrStdout(), "recovery show", "clean", map[string]any{
				"operation": map[string]any{
					"id":                      record.ID,
					"state":                   record.State,
					"target":                  record.Target,
					"target_fingerprint":      record.TargetFingerprint,
					"replacement_fingerprint": record.ReplacementFingerprint,
				},
			})
		},
	})
	var restoreExpect string
	var restoreDryRun bool
	restore := &cobra.Command{
		Use:   "restore <recovery-id>",
		Short: "Restore one Recovery operation's preimage.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			if lifecycle.New(root).Exists(arguments[0]) {
				if restoreDryRun {
					return emitResult(command.OutOrStdout(), "recovery restore", "planned", map[string]any{"restored": map[string]any{"recovery_id": arguments[0]}})
				}
				if err := lifecycle.New(root).Restore(arguments[0]); err != nil {
					return fmt.Errorf("restore lifecycle batch: %w", err)
				}
				return emitResult(command.OutOrStdout(), "recovery restore", "applied", map[string]any{"restored": map[string]any{"recovery_id": arguments[0]}})
			}
			service := recovery.New(root)
			record, err := service.Show(arguments[0])
			if err != nil {
				return fmt.Errorf("show Recovery operation: %w", err)
			}
			current, err := recovery.Fingerprint(record.Target)
			if err != nil {
				return fmt.Errorf("fingerprint restore target: %w", err)
			}
			if restoreExpect != "" && restoreExpect != current {
				return fmt.Errorf("BLOCKED: target fingerprint changed or --expect is missing; expected %s", current)
			}
			restored := map[string]any{"recovery_id": arguments[0], "target": record.Target}
			if restoreDryRun {
				return emitResult(command.OutOrStdout(), "recovery restore", "planned", map[string]any{"restored": restored})
			}
			operation, err := service.Restore(arguments[0])
			if err != nil {
				return fmt.Errorf("restore Recovery operation: %w", err)
			}
			if err := harness.NewRegistry(root).ForgetProjection(record.Target); err != nil {
				return fmt.Errorf("forget Projection: %w", err)
			}
			restored["operation_id"] = operation.ID
			return emitResult(command.OutOrStdout(), "recovery restore", "applied", map[string]any{"restored": restored})
		},
	}
	restore.Flags().StringVar(&restoreExpect, "expect", "", "expected current target fingerprint")
	restore.Flags().BoolVar(&restoreDryRun, "dry-run", false, "preview without writing")
	recoveryCommand.AddCommand(restore)
	return recoveryCommand
}

// newContentCommand builds the `skill` or `instruction` command group. word is
// the Caller-facing singular ("skill"/"instruction"); kind is the internal
// artifact kind. The two groups carry the same verbs; only `add`'s arity and a
// few help strings differ.
func newContentCommand(word, kind string) *cobra.Command {
	plural := word + "s"
	titlePlural := strings.ToUpper(word[:1]) + word[1:] + "s"
	group := &cobra.Command{
		Use:   word,
		Short: fmt.Sprintf("Capture and inspect canonical %s.", titlePlural),
		Long:  fmt.Sprintf("Capture and inspect canonical %s. A reference is namespace/name, for example main/release-notes; the default namespace is main.", titlePlural),
	}

	var namespace, name string
	addArgs := cobra.MinimumNArgs(1)
	addUse := "add <path>..."
	if kind == artifact.KindInstruction {
		addArgs = cobra.ExactArgs(1)
		addUse = "add <path>"
	}
	add := &cobra.Command{
		Use:   addUse,
		Short: fmt.Sprintf("Capture one or more local %s as immutable canonical revisions.", titlePlural),
		Long:  contentAddLong(word),
		Args:  addArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			if command.Flags().Changed("name") && len(arguments) != 1 {
				return domainErrorf("--name cannot rename %d sources at once; add them one path at a time", len(arguments))
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			options := artifact.CaptureOptions{Namespace: namespace, Name: name, ExplicitName: command.Flags().Changed("name")}

			if kind == artifact.KindInstruction {
				revision, err := store.CaptureInstructions(arguments[0], options)
				if err != nil {
					return domainErrorf("capture %s %s: %w", word, arguments[0], err)
				}
				return emitResult(command.OutOrStdout(), word+" add", "applied", map[string]any{
					"revisions": []map[string]any{contentRef(revision.Selector, revision.Digest)},
				})
			}

			sources, err := expandSkillSources(arguments)
			if err != nil {
				return asDomainError(err)
			}
			if len(sources) > 1 && command.Flags().Changed("name") {
				return domainErrorf("--name cannot rename %d Skills at once; add them one path at a time", len(sources))
			}
			captured := make([]artifact.Revision, 0, len(sources))
			for _, source := range sources {
				revision, err := store.CaptureSkill(source, options)
				if err != nil {
					if len(captured) > 0 {
						return domainErrorf("capture %s %s (after capturing %s): %w", word, source, capturedRefs(captured), err)
					}
					return domainErrorf("capture %s %s: %w", word, source, err)
				}
				captured = append(captured, revision)
			}
			noteUntrackedSources(command, root, captured, sources)
			revisions := make([]map[string]any, 0, len(captured))
			for _, revision := range captured {
				revisions = append(revisions, contentRef(revision.Selector, revision.Digest))
			}
			return emitResult(command.OutOrStdout(), word+" add", "applied", map[string]any{"revisions": revisions})
		},
	}
	add.Flags().StringVar(&namespace, "namespace", "main", "destination Namespace")
	add.Flags().StringVar(&name, "name", "", fmt.Sprintf("canonical %s name (single source only)", word))
	group.AddCommand(add)

	var listNamespace string
	list := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List canonical %s and their selected Revisions.", titlePlural),
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			revisions, err := artifact.NewStore(root).List(artifact.ListOptions{Namespace: listNamespace, Kind: kind})
			if err != nil {
				return fmt.Errorf("list %s: %w", plural, err)
			}
			entries := make([]map[string]any, 0, len(revisions))
			for _, revision := range revisions {
				entries = append(entries, contentRef(revision.Selector, revision.Digest))
			}
			return emitResult(command.OutOrStdout(), word+" list", "clean", map[string]any{plural: entries})
		},
	}
	list.Flags().StringVar(&listNamespace, "namespace", "", "filter by Namespace")
	group.AddCommand(list)

	var selectRevision string
	var selectDryRun bool
	selectCommand := &cobra.Command{
		Use:   "select <namespace/name>",
		Short: fmt.Sprintf("Select an already stored %s Revision.", word),
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if selectRevision == "" {
				return domainErrorf("select requires --revision sha256-<hex>")
			}
			key, err := artifact.Key(kind, arguments[0])
			if err != nil {
				return asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			resolve := store.Select
			if selectDryRun {
				resolve = store.VerifyRevision
			}
			revision, err := resolve(key, selectRevision)
			if err != nil {
				return fmt.Errorf("select %s: %w", word, err)
			}
			return emitResult(command.OutOrStdout(), word+" select", mutationState(selectDryRun), map[string]any{
				"selected": contentRef(revision.Selector, revision.Digest),
			})
		},
	}
	selectCommand.Flags().StringVar(&selectRevision, "revision", "", "stored Revision digest")
	selectCommand.Flags().BoolVar(&selectDryRun, "dry-run", false, "preview without writing")
	group.AddCommand(selectCommand)

	var promoteRevision string
	var promoteDryRun bool
	promote := &cobra.Command{
		Use:   "promote <namespace/name>",
		Short: fmt.Sprintf("Promote one imported %s Revision to main.", word),
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			key, err := artifact.Key(kind, arguments[0])
			if err != nil {
				return asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			revisionDigest := promoteRevision
			if revisionDigest == "" {
				revisionDigest, err = soleRevision(store, key)
				if err != nil {
					return err
				}
			}
			resolve := store.Promote
			if promoteDryRun {
				resolve = store.VerifyRevision
			}
			revision, err := resolve(key, revisionDigest)
			if err != nil {
				return fmt.Errorf("promote %s: %w", word, err)
			}
			return emitResult(command.OutOrStdout(), word+" promote", mutationState(promoteDryRun), map[string]any{
				"promoted": map[string]any{
					"kind":   word,
					"ref":    "main/" + refName(arguments[0]),
					"digest": revision.Digest,
					"origin": map[string]any{"ref": artifact.DisplayRef(key), "revision": revision.Digest},
				},
			})
		},
	}
	promote.Flags().StringVar(&promoteRevision, "revision", "", "imported Revision digest")
	promote.Flags().BoolVar(&promoteDryRun, "dry-run", false, "preview without writing")
	group.AddCommand(promote)

	var demoteDeleteProjections, demotePurge, demoteDryRun bool
	demote := &cobra.Command{
		Use:   "demote <main/name>",
		Short: fmt.Sprintf("Move one active %s to archive.", word),
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			key, err := artifact.Key(kind, arguments[0])
			if err != nil {
				return asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			if !strings.HasPrefix(key, "main/") {
				return domainErrorf("demote requires main/%s", refName(arguments[0]))
			}
			store, registry := artifact.NewStore(root), harness.NewRegistry(root)
			if _, err := store.Selected(key); err != nil {
				return err
			}
			projections, err := registry.ListProjections()
			if err != nil {
				return err
			}
			var owned []harness.Projection
			for _, projection := range projections {
				if projection.Artifact == key {
					owned = append(owned, projection)
				}
			}
			if demoteDeleteProjections && !demotePurge {
				for _, projection := range owned {
					matches, err := projectionFingerprintMatches(projection.Path, projection.Fingerprint)
					if err != nil {
						return err
					}
					if !matches && !mustBeMissing(projection.Path) {
						return domainErrorf("BLOCKED: Drift %s; rerun with --purge to delete it permanently", projection.Path)
					}
				}
			}
			archiveKey := "archive/" + kind + "/" + refName(arguments[0])
			archivePath, err := store.ArtifactPath(archiveKey)
			if err != nil {
				return err
			}
			if _, err := os.Stat(archivePath); err == nil {
				return domainErrorf("archive %s already exists", "archive/"+refName(arguments[0]))
			} else if !os.IsNotExist(err) {
				return err
			}
			if demotePurge && !demoteDeleteProjections {
				return domainErrorf("--purge requires --delete-projections")
			}
			if demoteDryRun {
				targets := []string{mustArtifactPath(store, key), archivePath, filepath.Join(root, "projections.toml")}
				if demoteDeleteProjections {
					targets = append(targets, projectionPaths(owned)...)
				}
				return emitResult(command.OutOrStdout(), word+" demote", "planned", map[string]any{"delete_projections": demoteDeleteProjections, "from": arguments[0], "projections": len(owned), "purge": demotePurge, "recovery": demoteDeleteProjections && !demotePurge, "targets": targets, "to": "archive/" + refName(arguments[0])})
			}
			var batch lifecycle.Batch
			apply := func() error {
				if _, err := store.Demote(key); err != nil {
					return err
				}
				if demoteDeleteProjections {
					for _, projection := range owned {
						if err := os.RemoveAll(projection.Path); err != nil {
							return err
						}
					}
				}
				_, err := registry.ForgetArtifact(key)
				return err
			}
			if demoteDeleteProjections && !demotePurge {
				targets := append([]lifecycle.Target{{Path: mustArtifactPath(store, key)}, {Path: archivePath}, {Path: filepath.Join(root, "projections.toml")}}, lifecycleTargets(owned)...)
				var err error
				batch, err = lifecycle.New(root).Apply(targets, apply)
				if err != nil {
					return lifecyclePartialError(batch, err)
				}
			} else if err := apply(); err != nil {
				return err
			}
			result := map[string]any{"from": arguments[0], "to": "archive/" + refName(arguments[0]), "projections": len(owned), "delete_projections": demoteDeleteProjections, "purge": demotePurge}
			if batch.ID != "" {
				result["recovery_id"] = batch.ID
			}
			return emitResult(command.OutOrStdout(), word+" demote", "applied", result)
		},
	}
	demote.Flags().BoolVar(&demoteDeleteProjections, "delete-projections", false, "delete linked Harness copies")
	demote.Flags().BoolVar(&demotePurge, "purge", false, "delete linked Harness copies without Recovery")
	demote.Flags().BoolVar(&demoteDryRun, "dry-run", false, "preview without writing")
	group.AddCommand(demote)

	var deletePurge, deleteDryRun bool
	deleteCommand := &cobra.Command{
		Use: "delete <namespace/name>", Short: fmt.Sprintf("Delete one %s and its managed projections.", word), Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			key, err := artifact.Key(kind, arguments[0])
			if err != nil {
				return asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store, registry := artifact.NewStore(root), harness.NewRegistry(root)
			if _, err := store.Selected(key); err != nil {
				return err
			}
			path, err := store.ArtifactPath(key)
			if err != nil {
				return err
			}
			projections, err := registry.ListProjections()
			if err != nil {
				return err
			}
			owned := []harness.Projection{}
			for _, projection := range projections {
				if projection.Artifact == key {
					owned = append(owned, projection)
					if !deletePurge {
						matches, err := projectionFingerprintMatches(projection.Path, projection.Fingerprint)
						if err != nil {
							return err
						}
						if !matches && !mustBeMissing(projection.Path) {
							return domainErrorf("BLOCKED: Drift %s; rerun with --purge to delete it permanently", projection.Path)
						}
					}
				}
			}
			if deleteDryRun {
				targets := append([]string{path, filepath.Join(root, "projections.toml")}, projectionPaths(owned)...)
				return emitResult(command.OutOrStdout(), word+" delete", "planned", map[string]any{"deleted": arguments[0], "projections": len(owned), "purge": deletePurge, "recovery": !deletePurge, "targets": targets})
			}
			apply := func() error {
				if err := os.RemoveAll(path); err != nil {
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
			var batch lifecycle.Batch
			if deletePurge {
				if err := apply(); err != nil {
					return err
				}
			} else {
				targets := append([]lifecycle.Target{{Path: path}, {Path: filepath.Join(root, "projections.toml")}}, lifecycleTargets(owned)...)
				batch, err = lifecycle.New(root).Apply(targets, apply)
				if err != nil {
					return lifecyclePartialError(batch, err)
				}
			}
			result := map[string]any{"deleted": arguments[0], "projections": len(owned), "purge": deletePurge}
			if batch.ID != "" {
				result["recovery_id"] = batch.ID
			}
			return emitResult(command.OutOrStdout(), word+" delete", "applied", result)
		},
	}
	deleteCommand.Flags().BoolVar(&deletePurge, "purge", false, "delete without Recovery")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "preview without writing")
	group.AddCommand(deleteCommand)

	var showFiles bool
	show := &cobra.Command{
		Use:   "show <namespace/name>",
		Short: fmt.Sprintf("Show metadata for the selected canonical %s revision.", word),
		Long:  fmt.Sprintf("Show metadata for the selected canonical %s revision. Pass --files to print the selected canonical text files.", word),
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			key, err := artifact.Key(kind, arguments[0])
			if err != nil {
				return asDomainError(err)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			revision, err := store.Selected(key)
			if err != nil {
				return fmt.Errorf("show %s: %w", word, err)
			}
			origin, err := store.Origin(key)
			if err != nil {
				return fmt.Errorf("show %s origin: %w", word, err)
			}
			revisionResult := contentRef(revision.Selector, revision.Digest)
			if origin.Selector != "" {
				revisionResult["origin"] = map[string]any{"ref": artifact.DisplayRef(origin.Selector), "revision": origin.Revision}
			}
			result := map[string]any{"revision": revisionResult}
			if showFiles {
				_, filesPath, err := store.SelectedContentFilesPath(key)
				if err != nil {
					return fmt.Errorf("show %s files: %w", word, err)
				}
				files, err := artifactFiles(filesPath)
				if err != nil {
					return fmt.Errorf("read %s files: %w", word, err)
				}
				result["files"] = files
			}
			return emitResult(command.OutOrStdout(), word+" show", "clean", result)
		},
	}
	show.Flags().BoolVar(&showFiles, "files", false, "print selected canonical text files")
	group.AddCommand(show)
	return group
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

func newHarnessCommand() *cobra.Command {
	harnessCommand := &cobra.Command{
		Use:   "harness",
		Short: "Discover, link, and inspect Harnesses.",
		RunE: func(command *cobra.Command, arguments []string) error {
			if len(arguments) > 0 {
				return fmt.Errorf("unknown command %q for %q", arguments[0], command.CommandPath())
			}
			return command.Help()
		},
	}
	var harnessName string
	discover := &cobra.Command{
		Use:   "discover",
		Short: "Discover supported user-level Harness installations.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			candidates, err := discoverBuiltinCandidates()
			if err != nil {
				return err
			}
			matched := false
			found := []map[string]any{}
			for _, candidate := range candidates {
				if harnessName != "" && candidate.candidate.Name != harnessName {
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
			if harnessName != "" && !matched {
				return fmt.Errorf("unsupported harness %q", harnessName)
			}
			return emitResult(command.OutOrStdout(), "harness discover", "clean", map[string]any{"candidates": found})
		},
	}
	discover.Flags().StringVar(&harnessName, "harness", "", "filter by supported Harness")
	harnessCommand.AddCommand(discover)
	harnessCommand.AddCommand(&cobra.Command{
		Use:   "link <candidate-id>",
		Short: "Link a discovered Harness installation.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			candidates, err := discoverBuiltinCandidates()
			if err != nil {
				return err
			}
			var candidate discoveredCandidate
			foundCandidate := false
			for _, item := range candidates {
				if item.candidate.ID == arguments[0] {
					candidate = item
					foundCandidate = true
					break
				}
			}
			if !foundCandidate {
				return fmt.Errorf("unknown Harness candidate %q; run 'brigsby harness discover'", arguments[0])
			}
			if !candidate.found {
				return fmt.Errorf("%s candidate %q is not currently installed at %s", candidate.candidate.Name, candidate.candidate.ID, candidate.candidate.SkillsPath)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			if err := harness.NewRegistry(root).Link(candidate.candidate); err != nil {
				return fmt.Errorf("link Harness: %w", err)
			}
			return emitResult(command.OutOrStdout(), "harness link", "applied", map[string]any{
				"linked": map[string]any{
					"id":          candidate.candidate.ID,
					"skills_path": candidate.candidate.SkillsPath,
				},
			})
		},
	})
	var unlinkDryRun bool
	unlink := &cobra.Command{
		Use:   "unlink <linked-installation-id>",
		Short: "Remove a linked Harness association without deleting its files.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			registry := harness.NewRegistry(root)
			linked, err := registry.List()
			if err != nil {
				return fmt.Errorf("read linked Harnesses: %w", err)
			}
			found := false
			for _, candidate := range linked {
				if candidate.ID == arguments[0] {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("linked Harness %q was not found", arguments[0])
			}
			if !unlinkDryRun {
				if err := registry.Unlink(arguments[0]); err != nil {
					return fmt.Errorf("unlink Harness: %w", err)
				}
			}
			return emitResult(command.OutOrStdout(), "harness unlink", mutationState(unlinkDryRun), map[string]any{"unlinked": arguments[0]})
		},
	}
	unlink.Flags().BoolVar(&unlinkDryRun, "dry-run", false, "preview without writing")
	harnessCommand.AddCommand(unlink)
	var statusHarness string
	var statusManaged, statusUnowned, statusAll bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show managed Harness Projections and Drift.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			if statusUnowned && (statusManaged || statusAll) {
				return domainErrorf("--unowned cannot be combined with --managed or --all")
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			registry := harness.NewRegistry(root)
			linked, err := registry.List()
			if err != nil {
				return fmt.Errorf("read linked Harnesses: %w", err)
			}
			if statusHarness != "" {
				filtered := linked[:0]
				for _, candidate := range linked {
					if candidate.ID == statusHarness {
						filtered = append(filtered, candidate)
					}
				}
				if len(filtered) == 0 {
					return fmt.Errorf("linked Harness %q was not found", statusHarness)
				}
				linked = filtered
			}
			projections, err := registry.ListProjections()
			if err != nil {
				return fmt.Errorf("read Projections: %w", err)
			}

			harnessesResult := map[string]any{}
			problems := []map[string]any{}
			driftCount, missingCount, staleCount, unownedCount := 0, 0, 0, 0

			showManaged := !statusUnowned
			showUnowned := statusUnowned || statusAll
			for _, candidate := range linked {
				harnessResult := map[string]any{
					"name":        candidate.Name,
					"projections": map[string]any{},
					"skills_path": candidate.SkillsPath,
				}
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
						return projectionStateErrorf("inspect projected %s: %w", candidate.ID, projection, displayContent(projection.Artifact), err)
					}
					matches := false
					if !missing {
						matches, err = projectionFingerprintMatches(projection.Path, projection.Fingerprint)
						if err != nil {
							return projectionStateErrorf("fingerprint projected %s: %w", candidate.ID, projection, displayContent(projection.Artifact), err)
						}
					}
					selected, _, err := artifact.NewStore(root).SelectedContentFilesPath(projection.Artifact)
					if err != nil {
						return projectionStateErrorf("canonical %s is unavailable: %w", candidate.ID, projection, displayContent(projection.Artifact), err)
					}
					projectionKey := projectionStatusKey(projection.Artifact)
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
					case matches:
						staleCount++
						entry["status"] = "stale"
						message := fmt.Sprintf("Projected %s in %s at %s is stale but unchanged.", displayContent(projection.Artifact), candidate.ID, projection.Path)
						problem := projectionProblem(fmt.Sprintf("stale-%02d", staleCount), "projection_stale", message, candidate.ID, projection)
						if artifact.KeyKind(projection.Artifact) == artifact.KindSkill {
							problem["remedy"] = projectionRemedy(projection, candidate.ID)
						} else {
							problem["message"] = message + " Protected instruction files need review before a force sync."
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
					projectionResult[projectionKey] = entry
				}
				if !showUnowned {
					continue
				}
				unowned, err := unownedSkills(candidate.SkillsPath, owned)
				if err != nil {
					return err
				}
				unownedResult := map[string]any{}
				for _, path := range unowned {
					unownedCount++
					problem := map[string]any{
						"id":      fmt.Sprintf("unowned-%02d", unownedCount),
						"code":    "unowned_path",
						"harness": candidate.ID,
						"kind":    "skill",
						"message": fmt.Sprintf("Skill path %s in %s is not managed by Brigsby.", path, candidate.ID),
						"path":    path,
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
			return emitEnvelope(command.OutOrStdout(), state, problems, map[string]any{
				"harnesses": harnessesResult,
			})
		},
	}
	status.Flags().StringVar(&statusHarness, "harness", "", "filter by linked Harness installation ID")
	status.Flags().BoolVar(&statusManaged, "managed", false, "deprecated: managed status is the default")
	status.Flags().BoolVar(&statusUnowned, "unowned", false, "report only Unowned paths")
	status.Flags().BoolVar(&statusAll, "all", false, "include managed Projections, Drift, and Unowned paths")
	_ = status.Flags().MarkHidden("managed")
	harnessCommand.AddCommand(status)
	return harnessCommand
}

func newSyncCommand() *cobra.Command {
	var linkedIDs, skillRefs, instructionRefs []string
	var force, dryRun bool
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Safely project selected canonical content to linked Harnesses.",
		Long:  "Safely project selected canonical content to linked Harnesses. Select with --skill namespace/name and --instruction namespace/name; with neither, every selection in main is projected.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			registry := harness.NewRegistry(root)
			linked, err := registry.List()
			if err != nil {
				return fmt.Errorf("read linked Harnesses: %w", err)
			}
			var selectors []string
			for _, group := range []struct {
				kind string
				refs []string
			}{{artifact.KindSkill, skillRefs}, {artifact.KindInstruction, instructionRefs}} {
				for _, ref := range group.refs {
					key, err := artifact.Key(group.kind, ref)
					if err != nil {
						return asDomainError(err)
					}
					selectors = append(selectors, key)
				}
			}
			targets, err := preflightSync(root, registry, linked, linkedIDs, selectors, force)
			if err != nil {
				return err
			}
			defer cleanupSyncTargets(targets)
			if dryRun {
				return writeSyncResults(command, "planned", targets, nil)
			}
			var operations []recovery.Operation
			for _, target := range targets {
				if target.removal {
					operation, err := recovery.New(root).Apply(target.plan)
					if err != nil {
						return partialSyncError(operations, target, err)
					}
					operations = append(operations, operation)
					if err := registry.ForgetProjection(target.path); err != nil {
						return partialSyncError(operations, target, fmt.Errorf("forget migrated Projection: %w", err))
					}
					continue
				}
				if target.plan.TargetFingerprint() != target.plan.ReplacementFingerprint() {
					operation, err := recovery.New(root).Apply(target.plan)
					if err != nil {
						return partialSyncError(operations, target, err)
					}
					operations = append(operations, operation)
				}
				contentFingerprint, err := recovery.ContentFingerprint(target.path)
				if err != nil {
					return partialSyncError(operations, target, fmt.Errorf("fingerprint projected content: %w", err))
				}
				if err := registry.RecordProjection(harness.Projection{HarnessID: target.harness.ID, Path: target.path, Artifact: target.revision.Selector, Revision: target.revision.Digest, Fingerprint: contentFingerprint}); err != nil {
					return partialSyncError(operations, target, fmt.Errorf("record Projection: %w", err))
				}
			}
			if len(operations) == 0 {
				return writeSyncResults(command, "clean", targets, nil)
			}
			return writeSyncResults(command, "applied", targets, operations)
		},
	}
	sync.Flags().StringArrayVar(&linkedIDs, "harness", nil, "linked Harness installation ID (repeatable)")
	sync.Flags().StringArrayVar(&skillRefs, "skill", nil, "selected Skill reference namespace/name (repeatable)")
	sync.Flags().StringArrayVar(&instructionRefs, "instruction", nil, "selected Instruction reference namespace/name (repeatable)")
	sync.Flags().BoolVar(&force, "force", false, "replace a single narrowed target that differs from canonical content")
	sync.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return sync
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

func projectionMissing(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func projectionProblem(id, code, message, harnessID string, projection harness.Projection) map[string]any {
	problem := projectionProblemFields(harnessID, projection)
	problem["code"] = code
	problem["id"] = id
	problem["message"] = message
	return problem
}

func projectionProblemFields(harnessID string, projection harness.Projection) map[string]any {
	return map[string]any{
		"harness": harnessID,
		"kind":    kindWord(projection.Artifact),
		"path":    projection.Path,
		"ref":     artifact.DisplayRef(projection.Artifact),
	}
}

// projectionRemedy returns a POSIX-shell command for an action the caller may
// copy, paste, or run with a shell tool. It deliberately does not claim to be
// an argv contract for direct process execution.
type remedy struct {
	Command string `json:"command"`
}

func projectionRemedy(projection harness.Projection, harnessID string) remedy {
	flag := "--skill"
	if artifact.KeyKind(projection.Artifact) == artifact.KindInstruction {
		flag = "--instruction"
	}
	return remedy{
		Command: fmt.Sprintf("brigsby sync %s %s --harness %s", flag, artifact.DisplayRef(projection.Artifact), harnessID),
	}
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

func writeSyncResults(command *cobra.Command, state string, targets []syncTarget, operations []recovery.Operation) error {
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
	problems := []any{}
	if state == "applied" {
		problems = append(problems, recoveryGCProblems()...)
	}
	envelope := map[string]any{
		"state":    state,
		"problems": problems,
		"result":   results,
	}
	if len(operations) > 0 {
		envelope["recovery_ids"] = recoveryOperationIDs(operations)
	}
	return encodeCanonicalJSON(command.OutOrStdout(), envelope)
}

func recoveryOperationIDs(operations []recovery.Operation) []string {
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.ID
	}
	return ids
}

func mustBeMissing(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

func projectionPaths(projections []harness.Projection) []string {
	paths := make([]string, len(projections))
	for index, projection := range projections {
		paths[index] = projection.Path
	}
	return paths
}

func lifecycleTargets(projections []harness.Projection) []lifecycle.Target {
	targets := make([]lifecycle.Target, len(projections))
	for index, projection := range projections {
		targets[index] = lifecycle.Target{Path: projection.Path}
	}
	return targets
}

func mustArtifactPath(store artifact.Store, key string) string {
	path, _ := store.ArtifactPath(key)
	return path
}

func lifecyclePartialError(batch lifecycle.Batch, cause error) error {
	if batch.ID == "" {
		return cause
	}
	return partialError{batchID: batch.ID, err: cause}
}

func removeWithRecovery(root, path string) (recovery.Operation, error) {
	plan, err := recovery.PlanRemoval(path)
	if err != nil {
		return recovery.Operation{}, err
	}
	return recovery.New(root).Apply(plan)
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
func noteUntrackedSources(command *cobra.Command, root string, captured []artifact.Revision, sources []string) {
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
			fmt.Fprintf(command.ErrOrStderr(),
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
