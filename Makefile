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
# caido       — local dev aid: start the headless Caido server from ./caido.toml
#               with separate UI and proxy listeners.
# version     — print the latest release tag and the next patch/minor/major
# notes       — group the commits since the last tag (or the whole history when
#               there is none) into .release/notes.md
# changelog   — write a dated, grouped section into CHANGELOG.md (newest first)
# release-dry — OPTIONAL local build. The release workflow builds the archives on GitHub.
# bump        — create the next annotated tag locally and print the push command
# release     — build a DRAFT GitHub release; never pushes, never publishes
# tools       — print the install commands for goreleaser and git-cliff

.PHONY: test install install-dev schema launch caido stop guard-config \
	version notes changelog release-dry bump release tools

# PORT is the shared MCP/SSE port for launch and the install-dev relaunch.
PORT ?= 4099

# PART selects the version component bumped by the bump target: patch, minor,
# major or half. half steps the minor by a half, so 0.2.0 becomes 0.2.5.
PART ?= patch

# CONFIG_DIR is the browser-mcp config directory install-dev wipes. Every path
# in that recipe is quoted and the recipe refuses to run when the directory is
# unset or outside $HOME.
CONFIG_DIR ?= $(HOME)/.config/browser-mcp

# The Caido dev config path is the single source of truth; copy
# caido.example.toml to this path, which git ignores.
CAIDO_TOML ?= caido.toml

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

# caido — local dev aid for a machine that already has caido-cli. Reads the
# single gitignored caido.toml and passes its addresses to the server. No
# install step runs.
caido:
	@set -eu; \
	cfg="$(CAIDO_TOML)"; \
	if ! command -v caido-cli >/dev/null 2>&1; then \
		echo "caido-cli: not on PATH" >&2; \
		exit 1; \
	fi; \
	if [ ! -f "$$cfg" ]; then \
		echo "caido: $$cfg missing — copy caido.example.toml" >&2; \
		exit 1; \
	fi; \
	ui=$$(sed -n 's/^ui *= *"\(.*\)"/\1/p' "$$cfg"); \
	proxy=$$(sed -n 's/^proxy *= *"\(.*\)"/\1/p' "$$cfg"); \
	data_path=$$(sed -n 's/^data_path *= *"\(.*\)"/\1/p' "$$cfg"); \
	allow_guests=$$(sed -n 's/^allow_guests *= *\(.*\)/\1/p' "$$cfg"); \
	open_browser=$$(sed -n 's/^open_browser *= *\(.*\)/\1/p' "$$cfg"); \
	case "$$data_path" in \
		"~"/*) data_path="$(HOME)$${data_path#\~}" ;; \
	esac; \
	mkdir -p "$$data_path"; \
	echo "ui   http://$$ui"; \
	echo "proxy browser_create {\"proxy\":\"$$proxy\",\"ignore_certs\":true}"; \
	set -- --ui-listen "$$ui" --proxy-listen "$$proxy" --data-path "$$data_path"; \
	if [ "$$allow_guests" = "true" ]; then set -- "$$@" --allow-guests; fi; \
	if [ "$$open_browser" = "false" ]; then set -- "$$@" --no-open; fi; \
	caido-cli "$$@"

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

# changelog — write a dated, grouped section into CHANGELOG.md at the repo root.
# The newest section sits directly under the title; an existing section for the
# same version is replaced, never duplicated. VERSION defaults to the next patch
# after the latest tag.
changelog:
	@set -eu; \
	version="$(VERSION)"; \
	if [ -z "$$version" ]; then \
		tag="$$(git describe --tags --abbrev=0 2>/dev/null || printf 'v0.0.0')"; \
		rest="$${tag#v}"; \
		major="$${rest%%.*}"; \
		rest="$${rest#*.}"; \
		minor="$${rest%%.*}"; \
		patch="$${rest#*.}"; \
		version="$$major.$$minor.$$((patch + 1))"; \
	fi; \
	version="$${version#v}"; \
	tag="$$(git describe --tags --abbrev=0 2>/dev/null || true)"; \
	if [ -n "$$tag" ]; then range="$$tag..HEAD"; else range="HEAD"; fi; \
	body="$$(git log --no-merges --pretty=format:'%s' "$$range" | awk ' \
		BEGIN { split("Features|Bug fixes|Performance|Refactoring|Documentation|Other", names, "|"); for (i = 1; i <= 6; i++) rank[names[i]] = i } \
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
			r = rank[group]; \
			items[r] = items[r] "- " subject "\n"; \
			seen[r] = 1; \
		} \
		END { \
			for (i = 1; i <= 6; i++) { \
				if (!(i in seen)) continue; \
				if (printed) print ""; \
				print "## " names[i]; \
				printf "%s", items[i]; \
				printed = 1; \
			} \
		}')"; \
	section="$$(printf '## %s - %s\n\n%s' "$$version" "$$(date +%F)" "$$body")"; \
	if [ ! -f CHANGELOG.md ]; then \
		printf '# Changelog\n\n%s\n' "$$section" > CHANGELOG.md; \
	else \
		sec="$$(mktemp)"; \
		tmp="$$(mktemp)"; \
		trap 'rm -f "$$sec" "$$tmp"' EXIT; \
		printf '%s\n' "$$section" > "$$sec"; \
		awk -v target="$$version" -v secfile="$$sec" ' \
			BEGIN { while ((getline line < secfile) > 0) section[++s] = line; close(secfile) } \
			{ lines[NR] = $$0 } \
			END { \
				n = NR; \
				title = 0; \
				for (i = 1; i <= n; i++) if (lines[i] == "# Changelog") { title = i; break } \
				for (i = 1; i <= n; i++) { \
					if (index(lines[i], "## " target " - ") != 1) continue; \
					for (j = i; j <= n; j++) { \
						if (j > i && lines[j] ~ /^## [0-9]/) break; \
						drop[j] = 1; \
					} \
				} \
				m = 0; \
				out[++m] = "# Changelog"; \
				out[++m] = ""; \
				for (i = 1; i <= s; i++) out[++m] = section[i]; \
				for (i = 1; i <= n; i++) { \
					if (i == title || (i in drop)) continue; \
					out[++m] = lines[i]; \
				} \
				while (m > 0 && out[m] == "") m--; \
				blank = 0; \
				for (i = 1; i <= m; i++) { \
					if (out[i] == "") { if (blank) continue; blank = 1 } else blank = 0; \
					print out[i]; \
				} \
			}' CHANGELOG.md > "$$tmp"; \
		mv "$$tmp" CHANGELOG.md; \
		trap - EXIT; \
	fi; \
	printf 'wrote %s\n' CHANGELOG.md

# release-dry — OPTIONAL local snapshot build into dist/. The GitHub Action is the real builder.
release-dry:
	@if ! command -v goreleaser >/dev/null 2>&1; then \
		echo "release-dry: goreleaser not found — install it with: go install github.com/goreleaser/goreleaser/v2@v2.17.0" >&2; \
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
		echo "release: goreleaser not found — install it with: go install github.com/goreleaser/goreleaser/v2@v2.17.0" >&2; \
		exit 1; \
	fi
	goreleaser release --clean --release-notes .release/notes.md

# tools — print the install commands for the release tools; installs nothing.
tools:
	@printf '%s\n' 'go install github.com/goreleaser/goreleaser/v2@v2.17.0'
	@printf '%s\n' 'cargo install git-cliff --locked   # git-cliff is Rust: no go install line exists'

# Local, machine-specific overrides. The file is untracked (see .gitignore) and
# make ignores it when it is absent. Keep it out of commits.
-include Makefile.local
