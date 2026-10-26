# web-mcp Makefile
# test        — run the full Go test suite
# install     — stop any running serve daemon, then build and install the browser
#               binary onto PATH (go install ./cmd/browser)
# install-dev — DESTRUCTIVE: stop the daemon, wipe the config dir (profiles,
#               config.json, proxy.json → saved logins and proxy credentials),
#               reinstall, then relaunch the MCP/SSE server on PORT
# launch      — stop any running serve daemon, then start the MCP/SSE server
#               daemonized on PORT (default 4089)

.PHONY: test install install-dev launch stop guard-config

# PORT is the shared MCP/SSE port for launch and the install-dev relaunch.
PORT ?= 4099

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

install: stop
	go install ./cmd/browser

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
