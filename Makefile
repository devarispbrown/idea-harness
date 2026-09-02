.PHONY: build test lint vet web web-build embed-web restore-embed check-embed docs docs-check clean

BINARY := bin/idea
MODULE := github.com/devarispbrown/idea-harness

# Version and commit are stamped into the binary so `idea version` and the
# daemon's /api/v1/version can name the build. VERSION comes from the current
# tag when there is one; a `go install` from a checkout has no stamp and falls
# back to the toolchain's own vcs.revision (see internal/buildinfo).
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -X '$(MODULE)/internal/buildinfo.version=$(VERSION)' -X '$(MODULE)/internal/buildinfo.commit=$(COMMIT)'

# The binary embeds the real UI at compile time, then the placeholder goes back
# on disk: the build leaves no modified tracked file, so `git commit -a` cannot
# ship an index.html that points at assets git does not track.
build: embed-web
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/idea
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

# Regenerate the command table in docs/cli.md from the CLI's own table
# (internal/cli/cli.go, via `idea help --format=md`). Prose outside the
# generated-block markers is left alone.
docs:
	go run ./internal/docsgen

# Fails when docs/cli.md no longer matches the command table — a new command
# cannot ship undocumented (CI runs this; see .github/workflows/ci.yml).
docs-check:
	go run ./internal/docsgen -check

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
