BINARY := claude-auto-resume
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race vet fmt install snapshot clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/claude-auto-resume

test:
	go test ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/claude-auto-resume

# Build a local release into ./dist without publishing (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf dist $(BINARY) car
