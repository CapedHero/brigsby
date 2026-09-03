# Brigsby workflows

This is supporting reference for the single `brigsby` skill. Exact command
syntax lives in the installed CLI's `--help` output. Brigsby manages two kinds
of content, each with its own command group: `brigsby skill …` and
`brigsby instruction …`.

## Inspect a machine

Discover candidate Harness installations, link the specific installation the
Caller wants Brigsby to manage, then inspect status. Discovery and status do
not claim, overwrite, or synchronize content. When a machine also carries
Skills Brigsby does not manage, the default status keeps the report to
Projections and their Drift. Use `--unowned` to inspect only those paths, or
`--all` to combine both views.

## Keep local content

When a user-owned Harness Skill should become canonical, add that explicit
local path with `brigsby skill add`. One add may take several paths, and a
path that is a directory of Skill directories captures each of them. For a
structured global Instruction set (an `AGENTS.md` index plus an
`instructions.toml` declaration and its docs), use `brigsby instruction add`.
Do not silently join different content to a same-named entry — repeat `--name`
to join a history, or choose another name or Namespace. Adding captures a
copy: the source path is not drift-tracked until a later sync records it as a
projection, so treat "kept" and "synchronized" as separate steps.

## Synchronize canonical content

Sync only the requested scope. Select with `--skill namespace/name` and
`--instruction namespace/name`; with neither, `brigsby sync` projects
everything in `main`. A clean direct sync projects canonical content after
Brigsby preflight; a dry run is the no-write preview. A blocked sync is
information, not an invitation to merge: preserve the local content by adding
it, or restore canonical content only with the exact targeted force action
given by the CLI.

## Share a Package

Create a text-only Package from explicitly selected Skills
(`brigsby package create --skill namespace/name … --output <path>`). A
recipient inspects it first, then imports it into an isolated Namespace. Import
never activates a Harness. Promote an exact received Revision to `main` with
`brigsby skill promote` only when the Caller wants it eligible for ordinary
sync; the imported copy remains intact with its origin.

## Recover a change

Inspect the relevant Recovery operation before restoring. Restore is itself a
new recoverable operation. If current paths no longer match the expected
state, stop and show the conflict rather than forcing a restoration.
