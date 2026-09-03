# Brigsby

Brigsby is a local-first, safety-first CLI for inspecting, synchronizing,
packaging, and restoring text-based Skills and Instructions across AI coding
Harnesses.

The project is in its initial development phase. The current slice supports
canonical, digest-addressed Skill revisions; linked Codex, Claude Code, and
OpenCode Harness projections; structured global Instructions; and text-only,
integrity-checked Skill Packages. It deliberately does not execute package
content, host a registry, or synchronize through a cloud service.

## Install

### macOS (Homebrew)

```sh
brew install CapedHero/brigsby/brigsby
```

Homebrew 6.0+ requires third-party taps to be trusted before their code runs;
the fully-qualified name trusts just this formula and needs no separate step.
To install and upgrade by the short `brigsby` name, trust the tap once:

```sh
brew tap CapedHero/brigsby
brew trust --tap CapedHero/brigsby
brew install brigsby
```

Either way the build compiles `brigsby` from source and pulls a Go toolchain as
a build dependency. Confirm with `brigsby --version`.

### Linux and CI (`curl | sh`)

On Linux, in containers, or in CI, install a prebuilt binary. Inspect the script
first:

```sh
curl -fsSL https://raw.githubusercontent.com/CapedHero/brigsby/main/install.sh -o install.sh
less install.sh
sh install.sh
```

Or, once you trust it, in one line:

```sh
curl -fsSL https://raw.githubusercontent.com/CapedHero/brigsby/main/install.sh | sh
```

The script downloads the release archive for your platform, verifies its
published SHA-256 checksum, installs `brigsby` to `~/.local/bin` without `sudo`,
and checks the build-provenance attestation when the GitHub CLI is present. It
honours `BRIGSBY_VERSION` (a tag such as `0.0.4`, default `latest`) and
`BRIGSBY_INSTALL_DIR`. On macOS, prefer Homebrew above.

Prebuilt `brigsby_<os>_<arch>.tar.gz` archives and `checksums.txt` are attached
to every [GitHub Release](https://github.com/CapedHero/brigsby/releases) for
manual download too.

### Go toolchain

```sh
go install github.com/CapedHero/brigsby/cmd/brigsby@latest
```

### From a checkout

```sh
go build ./cmd/brigsby
```

A checkout build reports version `dev`; released installs report their tag.

## Development

From a checkout, use these commands:

```sh
go test ./...
go test -run='^$' -bench=. ./...
go build ./...
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
preserving local content and restoring canonical content.

## Contribution rule

See [CONTRIBUTING.md](CONTRIBUTING.md) for the public contribution path.
