# web-mcp Makefile
# test    — run the full Go test suite
# install — stop any running serve daemon, then build and install the browser
#           binary onto PATH (go install ./cmd/browser)
# launch  — stop any running serve daemon, then start the MCP/SSE server
#           daemonized on port 4089 (browser serve mcp --port 4089)

.PHONY: test install launch stop

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

launch: stop
	browser serve mcp --port 4089
	@echo "MCP SSE endpoint is at http://127.0.0.1:4089/sse"

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
