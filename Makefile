APP=vps-commander
GO=go
LDFLAGS=-s -w

.PHONY: all clean test vet race build linux-amd64 linux-arm64

all: test build

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build: linux-amd64 mcp-stdio

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/vps-commander-hub-linux-amd64 ./cmd/hub
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/vps-commander-agent-linux-amd64 ./cmd/agent

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/vps-commander-hub-linux-arm64 ./cmd/hub
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/vps-commander-agent-linux-arm64 ./cmd/agent

clean:
	rm -rf bin

mcp-stdio:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/vps-commander-mcp-stdio-linux-amd64 ./cmd/mcp-stdio
