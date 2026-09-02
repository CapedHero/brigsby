# Brigsby workflows

This is supporting reference for the single `brigsby` skill. Exact command
syntax lives in the installed CLI's `--help` output.

## Inspect a machine

Discover candidate Harness installations, link the specific installation the
Caller wants Brigsby to manage, then inspect status. Discovery and status do
not claim, overwrite, or synchronize content.

## Keep local content

When a user-owned Harness Artifact should become canonical, add that explicit
local path as a new Artifact Revision. If its kind is ambiguous, let CLI help
determine the required kind selection. Do not silently join different content
to a same-named Artifact.

## Synchronize canonical content

Sync only the requested scope. A clean direct sync projects canonical content
after Brigsby preflight; a dry run is the no-write preview. A blocked sync is
information, not an invitation to merge: preserve the local content by adding
it, or restore canonical content only with the exact targeted force action
given by the CLI.

## Share a Package

Create a text-only Package from explicitly selected Artifacts. A recipient
inspects it first, then imports it into an isolated Namespace. Import never
activates a Harness. Promote an exact received Revision to `main` only when the
Caller wants it eligible for ordinary sync; the imported copy remains intact
with its origin.

## Recover a change

Inspect the relevant Recovery operation before restoring. Restore is itself a
new recoverable operation. If current paths no longer match the expected
state, stop and show the conflict rather than forcing a restoration.
