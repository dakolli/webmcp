# web-mcp Makefile
# test    — run the full Go test suite
# install — build and install the web-cli binary onto PATH (go install ./cmd/web-cli)

.PHONY: test install

test:
	go test ./...

install:
	go install ./cmd/web-cli
