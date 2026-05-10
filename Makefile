.PHONY: all lint test test-go test-python test-rust fmt vet build bench fuzz clean

all: lint test

# --- Go ---

fmt:
	gofmt -l . | tee /dev/stderr | (! read)

vet:
	go vet ./...
	cd sdk/go/reconciler && go vet ./...

build:
	go build ./cmd/receiver/
	go build ./cmd/dispatcher/
	go build ./cmd/admin/
	go build ./cmd/factoryctl/

test-go:
	go test -v -race -count=1 -timeout 120s ./...
	cd sdk/go/reconciler && go test -v -race -count=1 -timeout 30s ./...

# --- Python ---

test-python:
	cd sdk/python && pip install -e ".[dev,server]" -q && pytest -v

# --- Rust ---

test-rust:
	cd sdk/rust && rustup component add clippy 2>/dev/null && cargo test && cargo clippy -- -D warnings

# --- Bench & Fuzz ---

bench:
	@pkgs=$$(grep -rl '^func Benchmark' --include='*_test.go' . | xargs -I{} dirname {} | sort -u | tr '\n' ' '); \
	if [ -n "$$pkgs" ]; then go test $$pkgs -bench=. -benchmem -count=6 -run='^$$' -timeout 300s; fi

fuzz:
	@grep -rn '^func Fuzz' --include='*_fuzz_test.go' . | sed 's|^\./||' | \
	while IFS=: read -r file line func_line; do \
		dir=$$(dirname "$$file"); \
		target=$$(echo "$$func_line" | sed 's/func \(Fuzz[A-Za-z0-9_]*\).*/\1/'); \
		case "$$dir" in \
			sdk/go/reconciler*) cd sdk/go/reconciler && go test ./ -fuzz="^$${target}$$" -fuzztime=60s -v && cd ../../.. ;; \
			*) go test "./$$dir/" -fuzz="^$${target}$$" -fuzztime=60s -v ;; \
		esac; \
	done

# --- Aggregates ---

lint: fmt vet

test: test-go test-python test-rust

clean:
	go clean ./...
	cd sdk/rust && cargo clean
