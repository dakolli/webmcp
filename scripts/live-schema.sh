#!/usr/bin/env bash
# live-schema.sh — print the tool schema of the LIVE MCP server.
#
# A client fetches tools/list once per connection and keeps it. A rebuilt
# server therefore shows new fields that a long-lived client never sees. This
# script asks the server itself, so the answer is the current truth.
#
# Usage: scripts/live-schema.sh [PORT]
#   PORT defaults to 4099.
#
# Output: one line per tool, then each property with its type. A property that
# appears here but not in your client means the client cache is stale.

set -euo pipefail

PORT="${1:-4099}"
URL="http://127.0.0.1:${PORT}/message"

probe() {
	curl --silent --show-error --max-time 5 \
		-X POST "$URL" \
		-H 'content-type: application/json' \
		--data '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
}

# A server that is already up answers at once.
if body="$(probe 2>/dev/null)" && printf '%s' "$body" | grep -q '"result"'; then
	printf '%s' "$body"
	exit 0
fi

# No server on the port: bring one up on a private socket, read it, stop it.
TMP_PORT="$PORT"
SOCK_DIR="${XDG_RUNTIME_DIR:-/tmp}"
SOCK="${SOCK_DIR}/web-mcp-schema-$$.sock"
LOG="${TMPDIR:-/tmp}/web-mcp-schema-$$.log"

if ! command -v browser >/dev/null 2>&1; then
	echo "live-schema: the browser binary is not on PATH" >&2
	exit 1
fi

browser serve mcp --port "$TMP_PORT" --socket "$SOCK" --foreground >"$LOG" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -f "$SOCK" "$LOG"' EXIT

for _ in $(seq 1 40); do
	if body="$(probe 2>/dev/null)" && printf '%s' "$body" | grep -q '"result"'; then
		printf '%s' "$body"
		exit 0
	fi
	sleep 0.25
done

echo "live-schema: no server answered on port $PORT" >&2
sed -n '1,20p' "$LOG" >&2 || true
exit 1
