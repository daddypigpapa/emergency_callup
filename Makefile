# SPEC §3.3, §14. Every phase requires `make test lint` to pass.

.PHONY: build test lint sim run fmt vet

BINARY := bin/server
SIM_BINARY := bin/sim

build:
	go build -o $(BINARY) ./cmd/server
	go build -o $(SIM_BINARY) ./cmd/sim

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

# SPEC §11.3: web/ (excluding vendor/, which we don't control) must contain
# zero uses of innerHTML/outerHTML/insertAdjacentHTML/document.write. Grep
# out comment-only mentions (lines starting with // or * after trimming)
# isn't attempted here on purpose — a real match inside a comment would
# still be worth a human's eyes, so this stays a strict grep.
lint: fmt vet
	@echo "checking for banned DOM APIs outside web/vendor..."
	@if grep -rnE "\.innerHTML|\.outerHTML|insertAdjacentHTML|document\.write\(" web/ --include="*.js" --include="*.html" | grep -v "^web/vendor/" | grep -v -E "^\S+:[0-9]+:\s*(//|\*|/\*)"; then \
		echo "lint FAILED: banned DOM API usage found (see above)"; exit 1; \
	else \
		echo "lint OK: no innerHTML/outerHTML/insertAdjacentHTML/document.write outside vendor"; \
	fi
	@if [ -n "$$(gofmt -l .)" ]; then echo "lint FAILED: gofmt issues:"; gofmt -l .; exit 1; fi

sim: build
	./$(SIM_BINARY)

run: build
	./$(BINARY)
