package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CapedHero/brigsby/internal/artifact"
	"github.com/CapedHero/brigsby/internal/harness"
	"github.com/CapedHero/brigsby/internal/portable"
	"github.com/CapedHero/brigsby/internal/recovery"
	"github.com/spf13/cobra"
)

var errReportedProblems = errors.New("reported problems")

const version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	root := newRootCommand(stdout, stderr)
	root.SetArgs(arguments)
	if err := root.Execute(); err != nil {
		if errors.Is(err, errReportedProblems) {
			return 1
		}
		if jsonFields, _ := root.PersistentFlags().GetString("json"); jsonFields != "" {
			state := "invalid"
			if strings.Contains(err.Error(), "BLOCKED:") {
				state = "blocked"
			}
			_ = json.NewEncoder(stdout).Encode(map[string]any{
				"command":  strings.Join(arguments, " "),
				"state":    state,
				"problems": []map[string]string{{"message": err.Error()}},
			})
			return 1
		}
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr)
		root.SetOut(stderr)
		if helpErr := root.Help(); helpErr != nil {
			fmt.Fprintln(stderr, helpErr)
		}
		return 2
	}
	return 0
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "brigsby",
		Short:         "Brigsby manages AI coding-agent Artifacts safely.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version,
		RunE: func(command *cobra.Command, arguments []string) error {
			return command.Help()
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("brigsby {{.Version}}\n")
	root.PersistentFlags().String("json", "", "emit provisional machine-readable JSON (for example, --json all)")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the development version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "brigsby %s\n", version)
			return err
		},
	})
	root.AddCommand(newHarnessCommand())
	root.AddCommand(newArtifactCommand())
	root.AddCommand(newNamespaceCommand())
	root.AddCommand(newPackageCommand())
	root.AddCommand(newRecoveryCommand())
	return root
}

func newPackageCommand() *cobra.Command {
	packageCommand := &cobra.Command{Use: "package", Short: "Create and inspect portable text-only Packages."}
	var output, expect string
	var replace bool
	create := &cobra.Command{
		Use:   "create <artifact-selector>...",
		Short: "Create a portable Package from selected Skills.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if output == "" {
				return fmt.Errorf("package create requires --output <absolute-path>")
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			result, err := portable.Create(root, arguments, output, replace, expect)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "PACKAGE %s artifacts=%d output=%s\n", result.Digest, len(result.Artifacts), output)
			return err
		},
	}
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
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "PACKAGE %s artifacts=%d\n", result.Digest, len(result.Artifacts))
			return err
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
			if importDryRun {
				revisions, err := portable.CheckImport(root, arguments[0], namespace)
				if err != nil {
					return err
				}
				for _, revision := range revisions {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "PLANNED import %s %s\n", revision.Selector, revision.Digest); err != nil {
						return err
					}
				}
				return nil
			}
			revisions, err := portable.Import(root, arguments[0], namespace)
			if err != nil {
				return err
			}
			for _, revision := range revisions {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "IMPORTED %s %s\n", revision.Selector, revision.Digest); err != nil {
					return err
				}
			}
			return nil
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
			if dryRun {
				_, err := fmt.Fprintf(command.OutOrStdout(), "PLANNED prefix %s %s\n", arguments[0], arguments[1])
				return err
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			if err := artifact.NewStore(root).SetPrefix(arguments[0], arguments[1]); err != nil {
				return fmt.Errorf("set Namespace prefix: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "PREFIX %s %s\n", arguments[0], arguments[1])
			return err
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
			for _, record := range records {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "RECOVERY %s %s %s\n", record.ID, record.State, record.Target); err != nil {
					return err
				}
			}
			return nil
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
			_, err = fmt.Fprintf(command.OutOrStdout(), "RECOVERY %s\nstate=%s\ntarget=%s\ntarget_fingerprint=%s\nreplacement_fingerprint=%s\n", record.ID, record.State, record.Target, record.TargetFingerprint, record.ReplacementFingerprint)
			return err
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
			if restoreDryRun {
				_, err = fmt.Fprintf(command.OutOrStdout(), "PLANNED restore %s target=%s\n", record.ID, record.Target)
				return err
			}
			operation, err := service.Restore(arguments[0])
			if err != nil {
				return fmt.Errorf("restore Recovery operation: %w", err)
			}
			if err := harness.NewRegistry(root).ForgetProjection(record.Target); err != nil {
				return fmt.Errorf("forget Projection: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "RESTORED %s recovery=%s\n", arguments[0], operation.ID)
			return err
		},
	}
	restore.Flags().StringVar(&restoreExpect, "expect", "", "expected current target fingerprint")
	restore.Flags().BoolVar(&restoreDryRun, "dry-run", false, "preview without writing")
	recoveryCommand.AddCommand(restore)
	return recoveryCommand
}

func newArtifactCommand() *cobra.Command {
	artifactCommand := &cobra.Command{
		Use:   "artifact",
		Short: "Capture and inspect canonical Artifacts.",
	}
	var namespace, name, kind string
	add := &cobra.Command{
		Use:   "add <path>",
		Short: "Capture a local Artifact as an immutable canonical revision.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if kind != "skills" && kind != "instructions" {
				return fmt.Errorf("unsupported Artifact kind %q", kind)
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			options := artifact.CaptureOptions{Namespace: namespace, Name: name, ExplicitName: command.Flags().Changed("name")}
			var revision artifact.Revision
			if kind == "instructions" {
				revision, err = store.CaptureInstructions(arguments[0], options)
			} else {
				revision, err = store.CaptureSkill(arguments[0], options)
			}
			if err != nil {
				return fmt.Errorf("capture Artifact: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "CAPTURED %s %s\n", revision.Selector, revision.Digest)
			return err
		},
	}
	add.Flags().StringVar(&namespace, "namespace", "main", "destination Namespace")
	add.Flags().StringVar(&name, "name", "", "canonical Artifact name")
	add.Flags().StringVar(&kind, "kind", "skills", "Artifact kind: skills or instructions")
	artifactCommand.AddCommand(add)
	var listNamespace, listKind string
	list := &cobra.Command{
		Use:   "list",
		Short: "List canonical Artifacts and their selected Revisions.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			revisions, err := artifact.NewStore(root).List(artifact.ListOptions{Namespace: listNamespace, Kind: listKind})
			if err != nil {
				return fmt.Errorf("list Artifacts: %w", err)
			}
			for _, revision := range revisions {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "ARTIFACT %s %s\n", revision.Selector, revision.Digest); err != nil {
					return err
				}
			}
			return nil
		},
	}
	list.Flags().StringVar(&listNamespace, "namespace", "", "filter by Namespace")
	list.Flags().StringVar(&listKind, "kind", "", "filter by Artifact kind")
	artifactCommand.AddCommand(list)
	var selectRevision string
	var selectDryRun bool
	selectCommand := &cobra.Command{
		Use:   "select <namespace/skills/name>",
		Short: "Select an already stored Skill Revision.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if selectRevision == "" {
				return fmt.Errorf("select requires --revision sha256-<hex>")
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			if selectDryRun {
				revision, err := store.VerifyRevision(arguments[0], selectRevision)
				if err != nil {
					return fmt.Errorf("select Artifact: %w", err)
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "PLANNED select %s %s\n", revision.Selector, revision.Digest)
				return err
			}
			revision, err := store.Select(arguments[0], selectRevision)
			if err != nil {
				return fmt.Errorf("select Artifact: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "SELECTED %s %s\n", revision.Selector, revision.Digest)
			return err
		},
	}
	selectCommand.Flags().StringVar(&selectRevision, "revision", "", "stored Revision digest")
	selectCommand.Flags().BoolVar(&selectDryRun, "dry-run", false, "preview without writing")
	artifactCommand.AddCommand(selectCommand)
	var promoteRevision string
	var promoteDryRun bool
	promote := &cobra.Command{
		Use: "promote <namespace/skills/name>", Short: "Promote one imported Skill Revision to main.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if promoteRevision == "" {
				return fmt.Errorf("promote requires --revision sha256-<hex>")
			}
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			if promoteDryRun {
				revision, err := store.VerifyRevision(arguments[0], promoteRevision)
				if err != nil {
					return fmt.Errorf("promote Artifact: %w", err)
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "PLANNED promote main/skills/%s %s origin=%s\n", strings.Split(arguments[0], "/")[2], revision.Digest, revision.Selector)
				return err
			}
			revision, err := store.Promote(arguments[0], promoteRevision)
			if err != nil {
				return fmt.Errorf("promote Artifact: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "PROMOTED %s %s origin=%s@%s\n", revision.Selector, revision.Digest, arguments[0], revision.Digest)
			return err
		},
	}
	promote.Flags().StringVar(&promoteRevision, "revision", "", "imported Revision digest")
	promote.Flags().BoolVar(&promoteDryRun, "dry-run", false, "preview without writing")
	artifactCommand.AddCommand(promote)
	artifactCommand.AddCommand(&cobra.Command{
		Use:   "show <namespace/skills/name>",
		Short: "Show the selected revision of a canonical Skill.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			root, err := brigsbyHome()
			if err != nil {
				return err
			}
			store := artifact.NewStore(root)
			revision, err := store.Selected(arguments[0])
			if err != nil {
				return fmt.Errorf("show Artifact: %w", err)
			}
			origin, err := store.Origin(arguments[0])
			if err != nil {
				return fmt.Errorf("show Artifact origin: %w", err)
			}
			if origin.Selector != "" {
				_, err = fmt.Fprintf(command.OutOrStdout(), "SELECTED %s %s origin=%s@%s\n", revision.Selector, revision.Digest, origin.Selector, origin.Revision)
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "SELECTED %s %s\n", revision.Selector, revision.Digest)
			return err
		},
	})
	return artifactCommand
}

func newHarnessCommand() *cobra.Command {
	harnessCommand := &cobra.Command{
		Use:   "harness",
		Short: "Discover, link, inspect, and synchronize Harnesses.",
	}
	var harnessName string
	discover := &cobra.Command{
		Use:   "discover",
		Short: "Discover supported user-level Harness installations.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			candidates, err := discoverPersonalCandidates()
			if err != nil {
				return err
			}
			matched := false
			for _, candidate := range candidates {
				if harnessName != "" && candidate.candidate.Name != harnessName {
					continue
				}
				matched = true
				if !candidate.found {
					continue
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "CANDIDATE %s %s %s\n", candidate.candidate.ID, candidate.candidate.Name, candidate.candidate.SkillsPath); err != nil {
					return err
				}
			}
			if harnessName != "" && !matched {
				return fmt.Errorf("unsupported harness %q", harnessName)
			}
			return nil
		},
	}
	discover.Flags().StringVar(&harnessName, "harness", "", "filter by supported Harness")
	harnessCommand.AddCommand(discover)
	harnessCommand.AddCommand(&cobra.Command{
		Use:   "link <candidate-id>",
		Short: "Link a discovered Harness installation.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			candidates, err := discoverPersonalCandidates()
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
			_, err = fmt.Fprintf(command.OutOrStdout(), "LINKED %s %s\n", candidate.candidate.ID, candidate.candidate.SkillsPath)
			return err
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
			if unlinkDryRun {
				_, err = fmt.Fprintf(command.OutOrStdout(), "PLANNED unlink %s\n", arguments[0])
				return err
			}
			if err := registry.Unlink(arguments[0]); err != nil {
				return fmt.Errorf("unlink Harness: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "UNLINKED %s\n", arguments[0])
			return err
		},
	}
	unlink.Flags().BoolVar(&unlinkDryRun, "dry-run", false, "preview without writing")
	harnessCommand.AddCommand(unlink)
	var statusHarness string
	status := &cobra.Command{
		Use:   "status",
		Short: "Show linked Harness Projections, Drift, and Unowned paths.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			jsonFields, err := command.Root().PersistentFlags().GetString("json")
			if err != nil {
				return err
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
			jsonMode := jsonFields != ""
			var jsonProblems []map[string]any
			driftCount, unownedCount := 0, 0
			write := func(format string, args ...any) error {
				if jsonMode {
					return nil
				}
				_, err := fmt.Fprintf(command.OutOrStdout(), format, args...)
				return err
			}
			for _, candidate := range linked {
				if err := write("LINKED %s %s %s\n", candidate.ID, candidate.Name, candidate.SkillsPath); err != nil {
					return err
				}
				owned := map[string]struct{}{}
				for _, projection := range projections {
					if projection.HarnessID != candidate.ID {
						continue
					}
					owned[projection.Path] = struct{}{}
					current, err := recovery.Fingerprint(projection.Path)
					if err != nil {
						return fmt.Errorf("fingerprint Projection: %w", err)
					}
					selected, err := artifact.NewStore(root).Selected(projection.Artifact)
					if err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("read selected Artifact: %w", err)
					}
					if err == nil && current == projection.Fingerprint && selected.Digest == projection.Revision {
						if err := write("PROJECTION %s %s %s\n", projection.Artifact, projection.Revision, projection.Path); err != nil {
							return err
						}
						continue
					}
					driftCount++
					jsonProblems = append(jsonProblems, map[string]any{
						"id":      fmt.Sprintf("drift-%02d", driftCount),
						"code":    "projection_drift",
						"message": fmt.Sprintf("Drift %s %s", projection.Artifact, projection.Path),
					})
					if err := write("DRIFT %s %s\n", projection.Artifact, projection.Path); err != nil {
						return err
					}
				}
				unowned, err := unownedSkills(candidate.SkillsPath, owned)
				if err != nil {
					return err
				}
				for _, path := range unowned {
					unownedCount++
					jsonProblems = append(jsonProblems, map[string]any{
						"id":      fmt.Sprintf("unowned-%02d", unownedCount),
						"code":    "unowned_path",
						"message": fmt.Sprintf("Unowned path %s", path),
					})
					if err := write("UNOWNED %s\n", path); err != nil {
						return err
					}
				}
			}
			if jsonMode {
				state := "clean"
				if driftCount > 0 {
					state = "drifted"
				} else if unownedCount > 0 {
					state = "blocked"
				}
				if jsonProblems == nil {
					jsonProblems = []map[string]any{}
				}
				if err := json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{
					"command":  "harness status",
					"state":    state,
					"problems": jsonProblems,
				}); err != nil {
					return err
				}
			}
			if driftCount > 0 || unownedCount > 0 {
				return errReportedProblems
			}
			return nil
		},
	}
	status.Flags().StringVar(&statusHarness, "harness", "", "filter by linked Harness installation ID")
	harnessCommand.AddCommand(status)
	var linkedIDs, selectors []string
	var expect string
	var force, dryRun bool
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Safely project selected canonical Skills to linked Harnesses.",
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
			targets, err := preflightSync(root, registry, linked, linkedIDs, selectors, force, expect)
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
				if target.plan.TargetFingerprint() == target.plan.ReplacementFingerprint() {
					continue
				}
				operation, err := recovery.New(root).Apply(target.plan)
				if err != nil {
					return partialSyncError(operations, target, err)
				}
				operations = append(operations, operation)
				if err := registry.RecordProjection(harness.Projection{HarnessID: target.harness.ID, Path: target.path, Artifact: target.revision.Selector, Revision: target.revision.Digest, Fingerprint: target.plan.ReplacementFingerprint()}); err != nil {
					return partialSyncError(operations, target, fmt.Errorf("record Projection: %w", err))
				}
			}
			if len(operations) == 0 {
				return writeSyncResults(command, "clean", targets, nil)
			}
			return writeSyncResults(command, "applied", targets, operations)
		},
	}
	sync.Flags().StringSliceVar(&linkedIDs, "harness", nil, "linked Harness installation ID (repeatable)")
	sync.Flags().StringSliceVar(&selectors, "artifact", nil, "selected canonical Skill selector (repeatable)")
	sync.Flags().BoolVar(&force, "force", false, "replace one blocked target when guarded by --expect")
	sync.Flags().StringVar(&expect, "expect", "", "expected target fingerprint from a blocked sync")
	sync.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	harnessCommand.AddCommand(sync)
	return harnessCommand
}

type syncTarget struct {
	harness  harness.Candidate
	revision artifact.Revision
	path     string
	plan     recovery.Plan
	cleanup  func()
	removal  bool
}

func preflightSync(root string, registry harness.Registry, linked []harness.Candidate, requestedHarnesses, requestedArtifacts []string, force bool, expect string) (targets []syncTarget, err error) {
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
		return nil, fmt.Errorf("sync selects multiple global Instruction Artifacts; narrow --artifact to one so Brigsby never merges native root files")
	}
	seenTargets := make(map[string]struct{})
	for _, selector := range requestedArtifacts {
		parts := strings.Split(selector, "/")
		if len(parts) == 3 && parts[1] == "instructions" {
			if _, err := store.Selected(selector); err != nil {
				return nil, fmt.Errorf("read selected Artifact: %w", err)
			}
			var instructionTargets []syncTarget
			var changed []string
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
					if plan.TargetFingerprint() != "absent" && plan.TargetFingerprint() != plan.ReplacementFingerprint() {
						changed = append(changed, plan.TargetFingerprint())
					}
					instructionTargets = append(instructionTargets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: item.target, plan: plan, cleanup: rendered.Cleanup})
				}
			}
			if len(changed) > 0 {
				expected := strings.Join(changed, ",")
				if len(selectedHarnesses) != 1 || len(requestedArtifacts) != 1 || !force || expect != expected {
					return nil, fmt.Errorf("BLOCKED: Instruction Projection differs from %s; rerun with --force --expect %s", renderedInstructionPaths(instructionTargets), expected)
				}
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
			if plan.TargetFingerprint() != "absent" && plan.TargetFingerprint() != plan.ReplacementFingerprint() {
				if len(selectedHarnesses) != 1 || len(requestedArtifacts) != 1 {
					return nil, fmt.Errorf("BLOCKED: %s differs from %s; narrow --harness and --artifact to one target before force sync", target, rendered.Revision.Selector)
				}
				if !force {
					kind := "Unowned path"
					for _, projection := range projections {
						if projection.HarnessID == targetHarness.ID && projection.Path == target {
							kind = "Drift"
							break
						}
					}
					return nil, fmt.Errorf("BLOCKED: %s %s differs from %s; keep with 'brigsby artifact add %s' or rerun with --force --expect %s", kind, target, rendered.Revision.Selector, target, plan.TargetFingerprint())
				}
				if expect != plan.TargetFingerprint() {
					return nil, fmt.Errorf("BLOCKED: target fingerprint changed or --expect is missing; expected %s", plan.TargetFingerprint())
				}
			}
			targets = append(targets, syncTarget{harness: targetHarness, revision: rendered.Revision, path: target, plan: plan, cleanup: rendered.Cleanup})
			for _, projection := range projections {
				if projection.HarnessID != targetHarness.ID || projection.Artifact != rendered.Revision.Selector || projection.Path == target {
					continue
				}
				current, err := recovery.Fingerprint(projection.Path)
				if err != nil {
					return nil, fmt.Errorf("fingerprint previous Projection: %w", err)
				}
				if current != projection.Fingerprint {
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

func renderedInstructionPaths(targets []syncTarget) string {
	paths := make([]string, len(targets))
	for index, target := range targets {
		paths[index] = target.path
	}
	return strings.Join(paths, ",")
}

func instructionRootPath(candidate harness.Candidate) (string, error) {
	if !filepath.IsAbs(candidate.InstructionsPath) {
		return "", fmt.Errorf("linked Harness %q has no instruction location; unlink and relink it", candidate.ID)
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
	jsonFields, err := command.Root().PersistentFlags().GetString("json")
	if err != nil {
		return err
	}
	if jsonFields != "" {
		results := make([]map[string]string, 0, len(targets))
		for _, target := range targets {
			operation := "project"
			if target.removal {
				operation = "remove_previous_projection"
			}
			results = append(results, map[string]string{"harness": target.harness.ID, "artifact": target.revision.Selector, "revision": target.revision.Digest, "target": target.path, "operation": operation})
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{
			"command":  "harness sync",
			"state":    state,
			"problems": []any{},
			"result":   results,
			"preview":  map[string]any{"recovery_ids": recoveryOperationIDs(operations)},
		})
	}
	operationIndex := 0
	for _, target := range targets {
		switch state {
		case "clean":
			_, err = fmt.Fprintf(command.OutOrStdout(), "CLEAN %s %s\n", target.revision.Selector, target.revision.Digest)
		case "planned":
			_, err = fmt.Fprintf(command.OutOrStdout(), "PLANNED %s %s target=%s\n", target.revision.Selector, target.revision.Digest, target.path)
		default:
			if !target.removal && target.plan.TargetFingerprint() == target.plan.ReplacementFingerprint() {
				_, err = fmt.Fprintf(command.OutOrStdout(), "CLEAN %s %s\n", target.revision.Selector, target.revision.Digest)
				break
			}
			recoveryID := operations[operationIndex].ID
			operationIndex++
			_, err = fmt.Fprintf(command.OutOrStdout(), "APPLIED %s %s recovery=%s\n", target.revision.Selector, target.revision.Digest, recoveryID)
		}
		if err != nil {
			return err
		}
	}
	return nil
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

type discoveredCandidate struct {
	candidate harness.Candidate
	found     bool
}

func discoverPersonalCandidates() ([]discoveredCandidate, error) {
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
		{ID: "codex-personal", Name: "codex", SkillsPath: filepath.Join(home, ".agents", "skills"), InstructionsPath: filepath.Join(home, ".codex")},
		{ID: "claude-personal", Name: "claude", SkillsPath: filepath.Join(home, ".claude", "skills"), InstructionsPath: filepath.Join(home, ".claude")},
		{ID: "opencode-personal", Name: "opencode", SkillsPath: filepath.Join(configHome, "opencode", "skills"), InstructionsPath: filepath.Join(configHome, "opencode")},
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
