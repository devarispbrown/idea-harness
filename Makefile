.PHONY: build test lint vet web web-build embed-web clean

BINARY := bin/idea

build: embed-web
	go build -o $(BINARY) ./cmd/idea

# Build the web UI and copy the dist into the Go embed directory.
embed-web:
	cd web && pnpm install && pnpm build
	rm -rf internal/web/dist && cp -r web/dist internal/web/dist

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./... 2>/dev/null || echo "golangci-lint not installed; run 'go vet' instead"

web:
	cd web && pnpm install && pnpm dev

web-build: embed-web

clean:
	rm -rf bin/ .smoke/
