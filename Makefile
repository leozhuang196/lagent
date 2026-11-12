# lagent build helpers
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test race vet lint web run docker clean

all: vet test build

build: ## build the single binary
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/lagent ./cmd/lagent

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint: ## layer rules (R-1..R-6) are enforced by depguard
	golangci-lint run ./...

web: ## build the console and chat UI into internal/transport/static/dist
	cd web && npm ci && npm run build

run: build ## local single-process run with configs/dev.yaml
	./bin/lagent serve --config configs/dev.yaml

docker:
	docker build -t lagent:$(VERSION) .

clean:
	rm -rf bin data
