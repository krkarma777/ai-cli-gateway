.PHONY: fmt-check vet lint test race integration build verify
GOLANGCI_LINT ?= golangci-lint

fmt-check:
	@unformatted_files="$$(gofmt -l .)" && { \
		test -z "$$unformatted_files" || { \
			printf '%s\n' "$$unformatted_files"; exit 1; \
		}; \
	}

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	go test ./...

race:
	go test -race ./...

integration:
	go test -tags=integration ./...

build:
	CGO_ENABLED=0 go build -trimpath -o "$${TMPDIR:-/tmp}/ai-cli-gateway" \
		./cmd/ai-cli-gateway

verify: fmt-check vet lint test race integration build
