.PHONY: build test lint vet web web-build embed-web restore-embed check-embed clean

BINARY := bin/idea

# The binary embeds the real UI at compile time, then the placeholder goes back
# on disk: the build leaves no modified tracked file, so `git commit -a` cannot
# ship an index.html that points at assets git does not track.
build: embed-web
	go build -o $(BINARY) ./cmd/idea
	$(MAKE) restore-embed

# Build the web UI and copy the dist into the Go embed directory.
embed-web:
	cd web && pnpm install && pnpm build
	rm -rf internal/web/dist && cp -r web/dist internal/web/dist

# Put the committed placeholder back after a real build, so `git commit -a`
# cannot ship an index.html that references assets nobody committed.
restore-embed:
	cp internal/web/placeholder.html internal/web/dist/index.html

# Fails when the committed index.html is a real build rather than the
# placeholder (CI runs this; see check-embed in .github/workflows/ci.yml).
check-embed:
	@cmp -s internal/web/placeholder.html internal/web/dist/index.html || { \
		echo "internal/web/dist/index.html is not the committed placeholder."; \
		echo "Run 'make restore-embed' before committing (a real build belongs in a release, not in git)."; \
		exit 1; }

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
