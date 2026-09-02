# Brigsby release records

Each public Brigsby release has a GitHub Release record. It identifies the
public tag and commit, the exact private development source commit, the digest
of the reviewed release manifest, the staging-exporter version, the successful
private and public CI runs, and the `CapedHero/homebrew-brigsby` commit that
publishes the matching formula. Release records deliberately contain no private
paths, instructions, experiments, or unreleased material.

Use this public GitHub Release body template:

```text
Public ref: <tag>
Public commit: <sha>
Private source commit: <sha>
Release-manifest digest: sha256-<hex>
Exporter: brigsby-release-export/v1
Private CI: <run URL or ID>
Public CI: <run URL or ID>
Homebrew tap commit: <sha or n/a>
```

`Homebrew tap commit` is `n/a` for a tag that predates the Homebrew tap.
