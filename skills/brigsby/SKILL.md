---
name: brigsby
description: Manage Brigsby Skills and global Instructions when a Caller needs to inspect linked Harnesses, add or synchronize content, resolve drift, share a Package, or restore a Brigsby change.
---

# Brigsby

Use Brigsby to manage text-based Skills and global Instructions across linked
Harnesses. It gives each kind its own command group — `brigsby skill …` and
`brigsby instruction …` — and there is no umbrella `artifact` command. Its
leading loop is **observe → choose → act → verify**.

## Establish the interface

1. Run `brigsby --help` when this task needs a command or capability you do
   not already know. Before a mutation, package operation, recovery, or any
   uncertain option, also read the relevant nested help (for example
   `brigsby skill add --help`, `brigsby harness sync --help`).
2. If Brigsby is absent, the needed command is unavailable, or the help does
   not support the request, stop before mutation. State the exact gap and ask
   the Caller to install, upgrade, or choose another approach. On macOS the
   Caller can install with `brew install CapedHero/brigsby/brigsby`, or with
   `go install github.com/CapedHero/brigsby/cmd/brigsby@latest`.

Completion: the intended operation is supported by the installed CLI, and its
current syntax is known.

## Choose the operation

1. Treat a question or investigation as read-only. Use the inventory/status
   command from current CLI help (`brigsby status`, `brigsby skill list`,
   `brigsby instruction list`, `brigsby skill show`).
2. A Skill and a global Instruction set are different things: capture a Skill
   with `brigsby skill add <dir-with-SKILL.md>`, and a structured Instruction
   set with `brigsby instruction add <dir-with-instructions.toml>`. Let CLI
   help confirm which group the request needs.
3. Use a dry run when the Caller asks for a preview or when the desired change
   is unclear.
4. An explicit, narrow request to sync or import authorizes the
   corresponding direct command. Brigsby's preflight, Recovery capture, and
   post-write verification protect that operation.
5. Pause and show the situation for approval when intent or scope is
   ambiguous; more than one plausible Skill, Instruction, or Harness is
   involved; a force replacement, package-output replacement, restore, or
   unknown capability is required.

Completion: the command matches the Caller's explicit intent, or the Caller
has been given the concrete choice needed to continue.

## Act and verify

1. Run the selected command using the syntax just confirmed by CLI help. A
   reference is `namespace/name` (default namespace `main`); the kind comes
   from the command group, or from `--skill` / `--instruction` on
   `brigsby harness sync` and `brigsby package create`.
2. Every command result is one pretty-printed, key-sorted JSON envelope on
   stdout (`--help` and `--version` are the only plain-text output). Read
   `state` and `problems` as authoritative; a failure also echoes one line to
   stderr, which is a convenience, not the contract. Use `--jq` only when the
   Caller needs a smaller slice of the envelope.
3. For a blocked result, present the CLI's ready-to-run actions. Do not invent
   a merge or alter selectors/fingerprints. Ask for direction when both
   “preserve local” and “restore canonical” are plausible.
4. Read the result state. For every mutation, report the changed scope and the
   Recovery ID when one was created. A failed verification remains failed until
   the Caller chooses an explicit restore or next action.

Completion: report a short auditable outcome: inspected or changed scope,
state, Recovery ID if any, and exact next action if blocked.

## Workflow reference

Read [workflows.md](references/workflows.md) when selecting a Brigsby path.
It explains the lifecycle without duplicating version-sensitive CLI flags.
