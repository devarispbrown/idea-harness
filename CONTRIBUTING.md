# Contributing

idea-harness is built as a series of vertically-integrated milestones (§65). Development follows
the product constitution: plans are adversarially reviewed before implementation, and every
source ChangeSet is independently reviewed (§14, §75).

## Process

For each milestone (or material change):

1. Read the authoritative artifacts (`docs/plans/`, `docs/architecture.md`).
2. Produce or update the technical execution plan.
3. Adversarially review the plan from multiple perspectives; resolve blocking findings.
4. Decompose into dependency-ordered tasks.
5. Execute in small, explainable units.
6. Run deterministic checks; adversarially review every ChangeSet.
7. Update documentation; verify acceptance criteria.

## Layout

See `docs/architecture.md` for the package map.

## Build & test

```bash
make build     # build web UI + binary (bin/idea)
make test      # go test ./...
make vet       # go vet ./...
make embed-web # rebuild web/dist and copy into internal/web/dist
```

Go toolchain requires a workspace-local module cache in the sandboxed environment; the Makefile
and scripts honor `GOPATH`/`GOCACHE`/`GOMODCACHE` relative to the repo.

## Conventions

- Enums and invariants live in `internal/domain` (single source of truth).
- State mutations append an event atomically via `store.DB.Apply` (§32).
- Secrets never persist inline; the redaction choke point is `security.Redactor` wired into the
  blob store (§47–§48).
- Structured output goes through `model.DecodeStructured` (§39).
- Role Contracts in `internal/roles/contracts/*.yaml` define staff behavior, not ad-hoc prompts.

## Security

See `docs/security.md`. Never bypass the token auth, redaction, path guard, or trust assignment.
