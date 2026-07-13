.PHONY: build test clean install run

BINARY=gh-actions-mcp
VERSION?=dev
LDFLAGS=-X github.com/denysvitali/gh-actions-mcp/cmd.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./... -v

conformance:
	go test ./mcp ./cmd -run 'Test(StdioProcessConformance|StreamableHTTPClientRoundTrip)' -v

clean:
	rm -f $(BINARY)

install:
	go install .

run:
	go run . --token=$$GITHUB_TOKEN

# For Claude Desktop MCP integration
install-mcp:
	cp $(BINARY) ~/.config/Claude\ Desktop/mcp-servers/

.PHONY: all
