# Releasing web-mcp

This is the operating manual for cutting a `browser` release. Read it top to bottom once; after
that the release loop is six commands.

**The local machinery never pushes and never publishes.** `make bump` creates a tag locally and
prints the push command. `make release` builds a **draft** GitHub release and never runs `git push`.
Publishing is a human clicking **Publish** on the draft.

---

## 1. One-time setup

```sh
gh auth login                 # GitHub CLI: pick HTTPS or SSH, follow the browser prompt
gh auth status                # confirm the active account and token scopes
git remote -v                 # confirm origin points at the repository you expect
```

Install the release tools. `make tools` prints these lines and installs nothing:

```sh
go install github.com/goreleaser/goreleaser/v2@v2.19.0
cargo install git-cliff --locked      # optional; git-cliff is Rust, there is no go install
```

`make notes` and `make changelog` use plain `git log`, so git-cliff is not required for a release.
Install it only if you want its richer changelog rendering.

GoReleaser authenticates to GitHub with `GH_TOKEN` (or `GITHUB_TOKEN`). `gh auth` keeps a token but
does not export it, so bridge the two once per shell:

```sh
export GH_TOKEN="$(gh auth token)"
```

`make release-dry` uploads nothing, but the changelog uses the GitHub API
(`changelog.use: github`), so it still reads the token. CI supplies `secrets.GITHUB_TOKEN` itself.

---

## 2. The release loop

1. **Work lands.** Merge the changes you want in this release onto the default branch.
2. **Generate notes.** `make notes` writes `.release/notes.md` (grouped commits since the last tag,
   with empty `## Summary` and `## Highlights` sections). With no tag yet it covers the whole
   history and prints `no previous tag: this covers the whole history`.
3. **Ask an LLM for prose.** Open `.release/notes.md`, paste it into your model with the prompt in
   section 3, then save the model's answer over the `## Summary` and `## Highlights` sections.
   Leave the grouped commit list intact.
4. **Dry run.** `make release-dry` runs `goreleaser release --snapshot --clean` and builds every
   artifact into `dist/` locally. It uploads nothing and needs no tag.
5. **Inspect the artifacts.** Check the six archives and `checksums.txt`, and run one binary:
   `./dist/browser_<version>_linux_amd64/browser --version`.
6. **Tag.** `make bump PART=minor` (or `PART=patch` / `PART=major`) creates the annotated tag
   locally and prints the push command. It refuses a dirty tree and an existing tag.
7. **Push the tag.** `git push origin <tag>` — the only command here that writes to the remote, and
   you run it yourself.
8. **CI runs.** The `release` workflow runs `verify` (`gofmt -l`, `go vet ./...`, `go test ./...`),
   then GoReleaser, which uploads the artifacts to a **draft** release.
9. **Review and publish.** Open the draft release on GitHub, paste the notes from
   `.release/notes.md` into the description, confirm the artifacts, then click **Publish**.

You can also run `make release VERSION=<tag>` after the tag exists. It refuses a dirty tree, a
missing tag and a missing `.release/notes.md`, builds a draft release, and never pushes or publishes.

---

## 3. LLM prompt template for release notes

Paste this with the contents of `.release/notes.md`:

```text
You are writing release notes for web-mcp, a Go browser engine with an MCP server and a `browser`
CLI. Below are the commits grouped since the last release.

Rewrite them into:
1. A "## Summary" of 2-3 sentences for a developer who has not followed the project.
2. A "## Highlights" list of at most 5 user-visible changes, each one line, strongest first.
3. Keep the grouped commit list unchanged underneath.

Rules: no marketing adjectives, no invented features, no claims that are not in the commits. Name
the affected surface (CLI, MCP tool, engine) when it matters. Skip internal chores unless they
change behaviour.

Commit list:
<PASTE .release/notes.md HERE>
```

---

## 4. What each artifact is

For every `linux`/`darwin`/`windows` × `amd64`/`arm64` pair:

| Artifact | Contents |
| :--- | :--- |
| `browser_<version>_<os>_<arch>.tar.gz` | the `browser` binary plus `README.md` and `LICENSE` (unix) |
| `browser_<version>_windows_<arch>.zip` | the same, zipped for Windows |
| `checksums.txt` | SHA-256 for every archive |

Only `cmd/browser` is released. `cmd/mcp` is a thin legacy MCP-only binary superseded by
`browser serve mcp`; it keeps compiling but is not a release artifact, so users have one binary to
install and one code path to trust.

`browser --version` prints the injected build identity:

```text
0.1.0 (abc1234, 2026-10-27T00:00:00Z)
```

The shape is `<version> (<short commit>, <RFC3339 build time>)`. GoReleaser's `.Version` strips the
leading `v`, so a `v0.1.0` tag prints `0.1.0`. A plain `go build` prints
`dev (none, unknown)`.

---

## 5. Failure modes and fixes

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| `bump: dirty working tree` or `release: dirty working tree` | uncommitted changes | commit or `git stash`, then rerun |
| `bump: tag vX.Y.Z already exists` | the next version is already tagged | delete the wrong local tag or bump a different part |
| `release: tag vX.Y.Z does not exist` | `VERSION` was mistyped or the tag was never created | `make bump PART=…` first |
| `release-dry: goreleaser not found` | tool not installed | `go install github.com/goreleaser/goreleaser/v2@v2.19.0` then ensure `$(go env GOPATH)/bin` is on `PATH` |
| `gh: not logged in` / `gh auth status` fails | no GitHub credential | `gh auth login`, then `export GH_TOKEN="$(gh auth token)"` |
| `notes` prints `no previous tag: this covers the whole history` | this is the first release | expected; the notes cover every commit |
| `goreleaser check` warns about `snapshot.name_template` | newer GoReleaser renamed it to `snapshot.version_template` | harmless for now; rename when moving past v2.19.x |

---

## 6. Boundaries

- `make notes`, `make changelog`, `make release-dry`, `make bump` and `make release` never run
  `git push`, never publish a release and never create a GitHub release that is visible to users.
- Only the human-run `git push origin <tag>` starts CI, and CI only creates a **draft**.
- `.release/` is gitignored: notes and changelog fragments stay local until you paste them into the
  GitHub release description.
