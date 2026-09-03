.PHONY: build test lint vet web web-build embed-web web-deps-update restore-embed check-embed docs docs-check api-check api-docs-dist clean

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
#
# --frozen-lockfile so a build cannot silently resolve past pnpm-lock.yaml and
# produce different bytes than the lockfile that was reviewed. Bumping a
# dependency is `make web-deps-update`, which shows up as a lockfile diff.
#
# CI=true makes pnpm non-interactive. Without it, a node_modules directory
# installed before web/package.json pinned packageManager makes pnpm ask for
# confirmation to purge it and then abort with ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY,
# which fails the build on any developer machine that predates the pin.
embed-web:
	cd web && CI=true pnpm install --frozen-lockfile && pnpm build
	rm -rf internal/web/dist && cp -r web/dist internal/web/dist

# Resolve new dependency versions on purpose, updating pnpm-lock.yaml.
web-deps-update:
	cd web && pnpm install --no-frozen-lockfile

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

# Fails when docs/openapi.yaml and the daemon's route table disagree: a route
# with no operation in the spec, or an operation no route serves. The spec is
# hand-written, so this is the only thing standing between it and quiet drift.
#
# It reads the patterns out of internal/daemon's AST rather than grepping, and
# only compares paths under /api/ — the spec-serving endpoints (/openapi.yaml,
# /openapi.json, /api-reference) sit outside the auth boundary and outside the
# API surface the spec describes.
api-check:
	go run ./internal/docsgen -api-check

# Build the API reference as static files any host can serve, for people who
# would rather not carry its ~3.7 MiB inside every binary (APIDOCS=0 drops it).
#
# API_REFERENCE_BASE=./ is what makes a subpath deploy work: the default build
# emits absolute /assets/... URLs, correct at a site root and 404 anywhere else.
# The spec is copied in beside the page rather than fetched from a daemon --
# hostOriginGuard 403s any request carrying a non-loopback Origin, so a hosted
# page could not read a local daemon's spec even with CORS headers added, and
# co-hosting also means the page works with no daemon running at all.
API_DOCS_DIST := dist-api-docs

api-docs-dist:
	rm -rf $(API_DOCS_DIST)
	cd web && CI=true pnpm install --frozen-lockfile && API_REFERENCE_BASE=./ pnpm build
	mkdir -p $(API_DOCS_DIST)
	cp web/dist/api-reference.html $(API_DOCS_DIST)/index.html
	cp -R web/dist/assets $(API_DOCS_DIST)/assets
	cp docs/openapi.yaml $(API_DOCS_DIST)/openapi.yaml
	go run ./internal/docsgen -spec-json $(API_DOCS_DIST)/openapi.json
	cp deploy/api-docs/_headers $(API_DOCS_DIST)/_headers
	@echo
	@echo "$(API_DOCS_DIST)/ is ready to publish:"
	@du -sh $(API_DOCS_DIST) | sed 's/^/  /'
	@echo "  scripts/publish-api-docs.sh <cloudflare|aws|fastly> --dry-run"

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
