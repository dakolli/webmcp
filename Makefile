# web-mcp Makefile
# test        — run the full Go test suite
# install     — stop any running serve daemon, then build and install the browser
#               binary onto PATH (go install ./cmd/browser), then print the live
#               tool schema
# install-dev — DESTRUCTIVE: stop the daemon, wipe the config dir (profiles,
#               config.json, proxy.json → saved logins and proxy credentials),
#               reinstall, then relaunch the MCP/SSE server on PORT and print the
#               live tool schema
# schema      — print the tool schema of the LIVE server (starts a temporary one
#               when nothing listens on PORT)
# launch      — stop any running serve daemon, then start the MCP/SSE server
#               daemonized on PORT (default 4089)
# version     — print the latest release tag and the next patch/minor/major
# notes       — group the commits since the last tag (or the whole history when
#               there is none) into .release/notes.md
# changelog   — append the same grouped content to .release/CHANGELOG-fragment.md
# release-dry — build every release artifact locally (snapshot, no upload)
# bump        — create the next annotated tag locally and print the push command
# release     — build a DRAFT GitHub release; never pushes, never publishes
# tools       — print the install commands for goreleaser and git-cliff

.PHONY: test install install-dev schema launch stop guard-config \
	version notes changelog release-dry bump release tools

# PORT is the shared MCP/SSE port for launch and the install-dev relaunch.
PORT ?= 4099

# PART selects the version component bumped by the bump target.
PART ?= patch

# CONFIG_DIR is the browser-mcp config directory install-dev wipes. Every path
# in that recipe is quoted and the recipe refuses to run when the directory is
# unset or outside $HOME.
CONFIG_DIR ?= $(HOME)/.config/browser-mcp

# stop — stop any running browser serve daemon, tolerantly: no daemon running
# (or browser not yet on PATH) must never abort install/launch, so the
# leading '-' ignores the command's exit status.
stop:
	-browser serve stop
	-web-cli serve stop

test:
	go test ./...

# schema — print the property names and types of every tool on the LIVE server.
# A client caches tools/list for the whole session, so a rebuilt server shows
# fields that the client never learns. This target asks the server itself. The
# output is the truth: a name here and missing in your client means a stale
# client cache. With nothing listening, the target starts a temporary server on
# a private socket, reads it, and stops it.
schema:
	@out="$$(./scripts/live-schema.sh '$(PORT)')"; \
	if command -v jq >/dev/null 2>&1; then \
		printf '%s' "$$out" | jq -r '.result.tools[] | .name + ": " + ([.inputSchema.properties // {} | to_entries[] | .key + ":" + (.value.type // "?")] | join(", "))'; \
	else \
		printf '%s\n' "$$out"; \
	fi

install: stop
	go install ./cmd/browser
	@$(MAKE) --no-print-directory schema

# guard-config — refuse an install-dev wipe when the target is not safely under
# $HOME. Runs before stop so nothing is disturbed on a refusal.
guard-config:
	@if [ -n "$$BROWSER_MCP_CONFIG" ]; then \
		case "$$BROWSER_MCP_CONFIG" in \
			"$$HOME"/*) ;; \
			*) echo "install-dev: refusing to run: BROWSER_MCP_CONFIG=$$BROWSER_MCP_CONFIG is outside $$HOME"; exit 1 ;; \
		esac; \
	fi
	@if [ -z "$$HOME" ]; then echo "install-dev: refusing to run: HOME is unset"; exit 1; fi
	@case "$(CONFIG_DIR)" in \
		"$$HOME"/*) ;; \
		*) echo "install-dev: refusing to run: config dir $(CONFIG_DIR) is outside $$HOME"; exit 1 ;; \
	esac

# install-dev — destructive dev reset: guard, warn, wipe, reinstall, relaunch.
# Never touches an unset path.
install-dev: guard-config stop
	@echo "install-dev: wiping $(CONFIG_DIR) — THIS DELETES SAVED LOGINS, COOKIES AND PROXY CREDENTIALS"
	rm -rf -- "$(CONFIG_DIR)"
	go install ./cmd/browser
	browser serve mcp --port "$(PORT)"
	@echo "MCP SSE endpoint is at http://127.0.0.1:$(PORT)/sse"
	@$(MAKE) --no-print-directory schema

launch: stop
	browser serve mcp --port "$(PORT)"
	@echo "MCP SSE endpoint is at http://127.0.0.1:$(PORT)/sse"

# fetch-example — download a file from go-rod into ./examples/
# Usage: make fetch-example URL=https://github.com/go-rod/rod/blob/main/hijack_test.go
fetch-example:
	@if [ -z "$(URL)" ]; then \
		echo "Error: Please provide a URL. Usage: make fetch-example URL=https://github.com/go-rod/rod/blob/main/filename.go"; \
		exit 1; \
	fi
	@mkdir -p examples
	@RAW_URL=$$(echo "$(URL)" | sed 's|github.com/go-rod/rod/blob/main/|raw.githubusercontent.com/go-rod/rod/main/|'); \
	FILENAME=$$(basename "$$RAW_URL"); \
	echo "Downloading $$FILENAME into ./examples/..."; \
	curl -sSL "$$RAW_URL" -o "examples/$$FILENAME" && echo "Successfully saved examples/$$FILENAME"

# version — print the latest tag and the next patch, minor and major versions.
version:
	@tag="$$(git describe --tags --abbrev=0 2>/dev/null || printf 'v0.0.0')"; \
	version="$${tag#v}"; \
	major="$${version%%.*}"; \
	rest="$${version#*.}"; \
	minor="$${rest%%.*}"; \
	patch="$${rest#*.}"; \
	printf 'latest: %s\n' "$$tag"; \
	printf 'patch:  v%s.%s.%s\n' "$$major" "$$minor" "$$((patch + 1))"; \
	printf 'minor:  v%s.%s.0\n' "$$major" "$$((minor + 1))"; \
	printf 'major:  v%s.0.0\n' "$$((major + 1))"

# notes — write .release/notes.md from the commits since the last tag, grouped by
# prefix. With no tag yet the whole history is the first release.
notes:
	@set -eu; \
	tag="$$(git describe --tags --abbrev=0 2>/dev/null || true)"; \
	if [ -n "$$tag" ]; then \
		range="$$tag..HEAD"; \
	else \
		range="HEAD"; \
		printf '%s\n' "no previous tag: this covers the whole history"; \
	fi; \
	mkdir -p .release; \
	{ \
		printf '# Release notes\n\n## Summary\n\n## Highlights\n\n'; \
		git log --no-merges --pretty=format:'%s' "$$range" | awk ' \
			{ \
				subject = $$0; \
				prefix = subject; \
				sub(/[(!:].*/, "", prefix); \
				if (prefix == "docs" && subject ~ /^docs\(skill\)/) next; \
				if (prefix == "feat") group = "Features"; \
				else if (prefix == "fix") group = "Bug fixes"; \
				else if (prefix == "perf") group = "Performance"; \
				else if (prefix == "refactor") group = "Refactoring"; \
				else if (prefix == "docs") group = "Documentation"; \
				else group = "Other"; \
				if (!(group in printed)) { if (started) print ""; print "## " group; printed[group] = 1; started = 1 } \
				print "- " subject; \
			}'; \
	} > .release/notes.md; \
	printf 'wrote %s\n' .release/notes.md

# changelog — append the same grouped content to .release/CHANGELOG-fragment.md.
changelog: notes
	@cat .release/notes.md >> .release/CHANGELOG-fragment.md
	@printf 'appended to %s\n' .release/CHANGELOG-fragment.md

# release-dry — build every artifact locally with goreleaser --snapshot; uploads nothing.
release-dry:
	@if ! command -v goreleaser >/dev/null 2>&1; then \
		echo "release-dry: goreleaser not found — install it with: go install github.com/goreleaser/goreleaser/v2@v2.19.0" >&2; \
		exit 1; \
	fi
	goreleaser release --snapshot --clean

# bump — create the next annotated tag locally and print the push command.
bump:
	@sh scripts/bump.sh "$(PART)"

# release — build a DRAFT GitHub release for VERSION; it never git-pushes and never publishes.
release:
	@if [ -z "$(VERSION)" ]; then \
		echo "release: VERSION is required, e.g. make release VERSION=v0.1.0" >&2; \
		exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "release: dirty working tree — commit or stash first" >&2; \
		git status --short >&2; \
		exit 1; \
	fi
	@if ! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null; then \
		echo "release: tag $(VERSION) does not exist — run 'make bump PART=patch' and push that tag first" >&2; \
		exit 1; \
	fi
	@if [ ! -f .release/notes.md ]; then \
		echo "release: .release/notes.md is missing — run 'make notes' and fill in Summary/Highlights" >&2; \
		exit 1; \
	fi
	@if ! command -v goreleaser >/dev/null 2>&1; then \
		echo "release: goreleaser not found — install it with: go install github.com/goreleaser/goreleaser/v2@v2.19.0" >&2; \
		exit 1; \
	fi
	goreleaser release --clean --release-notes .release/notes.md

# tools — print the install commands for the release tools; installs nothing.
tools:
	@printf '%s\n' 'go install github.com/goreleaser/goreleaser/v2@v2.19.0'
	@printf '%s\n' 'cargo install git-cliff --locked   # git-cliff is Rust: no go install line exists'
