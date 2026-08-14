# web-mcp Makefile
# test    — run the full Go test suite
# install — stop any running serve daemon, then build and install the web-cli
#           binary onto PATH (go install ./cmd/web-cli)
# launch  — stop any running serve daemon, then start the MCP/SSE server
#           daemonized on port 4089 (web-cli serve mcp --port 4089)

.PHONY: test install launch stop

# stop — stop any running web-cli serve daemon, tolerantly: no daemon running
# (or web-cli not yet on PATH) must never abort install/launch, so the
# leading '-' ignores the command's exit status.
stop:
	-web-cli serve stop

test:
	go test ./...

install: stop
	go install ./cmd/web-cli

launch: stop
	web-cli serve mcp --port 4089
	@echo "MCP SSE endpoint is at http://127.0.0.1:4089/sse"
