# Releasing ServeIt

This document is for maintainers publishing binaries to GitHub Releases. Users should download the latest build from the [README](README.md#latest-binaries).

## Prepare the release

Start from an up-to-date `main` branch with a clean worktree:

```bash
git switch main
git pull --ff-only
go test ./...
go vet ./...
```

Review the commits since the previous tag and choose the next semantic version.

## Publish a tag

```bash
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

Pushing a tag beginning with `v` starts [the release workflow](.github/workflows/release.yml). It cross-compiles five static binaries:

| Asset | Target |
|---|---|
| `ServeIt-linux-amd64` | Linux, Intel/AMD 64-bit |
| `ServeIt-linux-arm64` | Linux, ARM64 |
| `ServeIt-macos-amd64` | macOS, Intel |
| `ServeIt-macos-arm64` | macOS, Apple silicon |
| `ServeIt-windows-amd64.exe` | Windows, Intel/AMD 64-bit |

The workflow also creates `SHA256SUMS`, generates release notes, and attaches all six files to the GitHub release.

The same workflow can be started manually from the Actions tab with a release tag. Prefer a pushed annotated tag so the source commit and published version are explicit and reviewable.

## Verify the result

1. Confirm every build job and the publishing job completed successfully.
2. Open `https://github.com/Danakolana/ServeIt/releases/tag/vX.Y.Z`.
3. Confirm all five binaries and `SHA256SUMS` are present.
4. Download at least the primary Linux artifact and verify its checksum.
5. Start the binary and check the startup panel, file browser, admin login, and one selective guest link.

Release-workflow hardening and clean-environment `go install` support are tracked in [BACKLOG.md](BACKLOG.md).
