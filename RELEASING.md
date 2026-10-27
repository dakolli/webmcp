# Releasing

Everything runs on GitHub. You need `git` and `make`. You do not need the GitHub CLI, a token,
or GoReleaser on your machine.

## One-time setup

Do this once in the repository:

1. Open **Settings → Actions → General**.
2. Find **Workflow permissions**.
3. Select **Read and write permissions**.
4. Click **Save**.

The release job creates the GitHub release. It cannot do that with a read-only token.

## The loop

### 1. Collect the commits

```sh
make notes
```

Writes `.release/notes.md` with every commit since the last tag, grouped by type. With no tag it
covers the whole history.

### 2. Turn it into prose

Open `.release/notes.md`. Paste it into your LLM with this prompt:

> Rewrite these grouped commits as release notes for web-mcp vX.Y.Z. Two sections: Highlights
> (3-5 bullets, user-facing, no file paths) and Changes (keep the groups). Plain English.

Save the answer over `.release/notes.md`.

### 3. Update the changelog

```sh
make changelog VERSION=0.1.0
```

Inserts a `## 0.1.0 - YYYY-MM-DD` section into `CHANGELOG.md` at the top. A section for the same
version is replaced, never duplicated.

Commit it, because the tag must include the changelog:

```sh
git add CHANGELOG.md && git commit -m "docs: changelog for 0.1.0" && git push
```

### 4. Tag

```sh
make bump PART=minor
```

`patch`, `minor` or `major`. Creates the annotated tag locally and prints the push line. It refuses
a dirty tree and an existing tag. The tag is `v0.1.0` when the latest tag is `v0.0.0` and you ask
for `minor`.

### 5. Push the tag

```sh
git push origin v0.1.0
```

This is the only step that starts the release. The workflow runs `verify` (gofmt, vet, test), then
GoReleaser builds and publishes.

### 6. Watch it

Open **Actions → release** on GitHub. Two jobs, about two minutes.

Green means the release exists. **Releases** now lists `v0.1.0` with these files:

| File | What it is |
| :--- | :--- |
| `browser_<version>_linux_amd64.tar.gz` | Linux x86-64 |
| `browser_<version>_linux_arm64.tar.gz` | Linux ARM64 |
| `browser_<version>_darwin_amd64.tar.gz` | macOS Intel |
| `browser_<version>_darwin_arm64.tar.gz` | macOS Apple silicon |
| `browser_<version>_windows_amd64.zip` | Windows x86-64 |
| `browser_<version>_windows_arm64.zip` | Windows ARM64 |
| `checksums.txt` | SHA-256 for every archive |

### 7. Edit the release text

Open the release on the website. Click **Edit**. Paste the notes into the description. Click
**Update release**.

## How users install

Go toolchain:

```sh
go install github.com/dakolli/webmcp/cmd/browser@latest
```

No toolchain:

```sh
curl -fsSL https://raw.githubusercontent.com/dakolli/webmcp/main/install.sh | sh
```

`install.sh` detects OS and arch, downloads the matching archive, checks it against
`checksums.txt`, and installs to `~/.local/bin` or `$BIN_DIR`.

## Failure modes

Open the failed run on the website and read the first red line.

| Message | Cause | Fix |
| :--- | :--- | :--- |
| `Resource not accessible by integration` | Read-only workflow token | The one-time setting above |
| `git is in a dirty state` | A build step changed a tracked file | Never add a hook that writes files |
| `gofmt needed on: …` | Unformatted Go file | `gofmt -w` the file, commit, re-tag |
| `tag already exists` | You tagged twice | `git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z`, then bump again |
| `no releases` after a green run | The release is hidden as a draft | `.goreleaser.yaml` must say `draft: false` |

## Boundaries

- `make notes` and `make changelog` write local files only. They touch no remote.
- `make release-dry` builds archives into `dist/` locally. It is optional. `dist/` is gitignored.
- `make bump` writes a local tag. It pushes nothing.
- `git push` is yours to run.
