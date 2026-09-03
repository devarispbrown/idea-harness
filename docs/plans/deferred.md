# Deferred items

One line per thing a round decided not to do, with where it came from. Appended
to, never rewritten — a plan file is superseded by the next round's plan, so
"recorded in the plan" has repeatedly meant "lost".

Started in DX audit 3, after review found that audit 2's below-the-cut list was
dropped and one of its items (README's provider list omitting DeepSeek) was
still unfixed a round later and had reappeared as new work.

| Origin | Item | Why deferred |
|---|---|---|
| audit 3 / S4 | A redacting `slog` handler; daemon logs are unredacted and readable via `GET /api/v1/logs` | Larger than a doc round; the gap is documented instead |
| audit 3 / S6 | Container `--user` passes the daemon's UID, so a root daemon gives a root container | Needs a policy decision on refusing to run as root |
| audit 3 / S6 | Hermes image pinned by tag, not digest; no signature verification | Supply-chain work, separate round |
| audit 3 / S6 | Control-plane deny does not cover `.github/workflows/` and other auto-executing files | Needs a decision on scope vs. false positives |
| audit 3 / G2 | Live-response schema check against the smoke daemon | The `api.md` ↔ `openapi.yaml` axis covers this round's findings more cheaply |
| audit 3 | No SDK | Deliberate; generating one from the spec is a milestone |
| audit 3 | `linux/arm64` untested in CI | Hosted arm64 Linux runners unavailable on this repo's plan; stated in README |
| audit 3 | Cutting `0.2.0` | `v0.1.0` is still the only tag and contains every audit-2 defect; a decision after this lands |
| audit 2 | README provider list omitted DeepSeek | **Was** deferred by omission; fixed in audit 3 as R13 |
