# Brigsby

Brigsby is a local-first, safety-first CLI for inspecting, synchronizing,
packaging, and restoring text-based Skills and Instructions across AI coding
Harnesses.

The project is in its initial development phase. The current slice supports
canonical, digest-addressed Skill revisions; linked Codex, Claude Code, and
OpenCode Harness projections; structured global Instructions; and text-only,
integrity-checked Skill Packages. It deliberately does not execute package
content, host a registry, or synchronize through a cloud service.

## Development

From a checkout, use these commands:

```sh
go test ./...
go test -run='^$' -bench=. ./...
go build ./...
go run ./cmd/brigsby --help
go run ./cmd/brigsby --help
```

These commands require the Go version declared in [`go.mod`](go.mod).

## AI Caller Skill

The released first-party teaching Skill is
[`skills/brigsby`](skills/brigsby/). Install that directory as the `brigsby`
Skill using the supported skill-installation mechanism for the chosen Harness.
An AI Caller can discover it from its model-facing description or invoke
`$brigsby` explicitly. The Skill starts from the installed `brigsby --help`,
then reads nested help before it uses a version-sensitive option.

The Skill must stop for approval when the requested scope is ambiguous, a
replacement or restore is required, or Brigsby reports a choice between
preserving local content and restoring canonical content. The private
development Skills and maintainer instructions are not part of a release.

## Contribution rule

See [CONTRIBUTING.md](CONTRIBUTING.md) for the public contribution path.
