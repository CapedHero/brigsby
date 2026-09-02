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

## Private development workspace

The primary development workspace is private
[`CapedHero/brigsby-dev`](https://github.com/CapedHero/brigsby-dev). Its root
AGENTS.md, CONTEXT.md, ADR index, research, and private Wayfinder board guide
maintainers and AI contributors. The public
[`CapedHero/brigsby`](https://github.com/CapedHero/brigsby) repository is a
reviewed release and external-pull-request surface; it intentionally excludes
those private development materials.

## Contribution rule

See [CONTRIBUTING.md](CONTRIBUTING.md) for the public contribution path.
