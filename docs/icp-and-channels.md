# ICP and channels: capture anywhere, judge anywhere

The product's interaction model is **human-in-the-loop judgment**, not chat. This
page pins the ICP, the one reframe every channel decision hangs off, and the
decisions for adding capture/judgment channels and an always-on story.

## Who this is for (ICP)

A **solo technical builder or a small team's technical lead** who:

- wants reviewed, defensible output (idea → PRD → plan → code), not a chatbot;
- is time-poor and context-switches constantly, so ideas strike — and pings land —
  at odd moments away from a desk;
- values control: their provider keys, their code, their review, their hardware.

What they hire idea-harness for: turn ideas into **reviewed, verifiable**
product/engineering work, with themselves as the reviewer of record.

## The interaction model (everything else follows)

Two interaction classes with different channel needs:

| Class | What the human does | Bandwidth | Best surface |
|---|---|---|---|
| **Capture** | "idea: a CLI that …" | low, one-shot | phone / messaging / email |
| **Judge** | read, resolve, approve, verify | high, needs context | the web UI, deep-linked |

Capture is chat-shaped; judgment is not. A channel must never try to do the
judgment UI's job — its job is to *ping and hand off*.

**The tap-vs-open line** — what a notification may let you do, vs. what must
deep-link into the app:

- *Tap is fine:* resolve a decision to its recommendation, answer a simple
  question, record a metric, waive a criterion.
- *Must open the app:* approve the PRD (unlocks the pipeline), apply/revert a
  change set (writes code), verify-with-evidence (a person's word).

A stray thumb must never be able to approve a PRD or apply a diff.

## Channels

| Channel | Current state | Decision |
|---|---|---|
| Slack | outbound notifications | keep; add interactive judgment taps |
| Linear | two-way (inbound → initiatives, outbound updates) | keep |
| Web push | per-browser, deep-links | keep — this is the phone channel |
| Telegram | none | **add** — capture + judgment taps; best fit for the solo ICP |
| Email | none | **add** as the no-config capture fallback |
| SMS | none | **skip** — can't carry judgment, costs per message, web push already covers "ping my phone" |

## The new ingress, and how to keep the trust model

Capture from a channel is an **unauthenticated write path into a local-first
daemon** — a security-model change, not just a feature. Rules:

1. **Per-channel capability token**, minted in Settings exactly like the push
   renewal token (SHA-256 stored, never listed, constant-time compare). A
   captured message without its channel's token is refused with one identical
   401, so the route reveals nothing about which channels or projects exist.
2. **Capture = draft only.** A captured idea creates a *draft* initiative (brief
   saved, pipeline **not** started). Grounding docs and "Start" happen from the
   real UI, so a channel can never auto-spend model budget or start the executor.
3. **No secrets in, no secrets out.** Capture text goes through the existing
   redactor on ingest; notifications are already redacted.

## Always-on: pull first, push as opt-in

A capture message must reach a daemon that is often asleep. Two models:

- **Pull (default):** the channel is a tiny always-on *inbox* (the bot/email just
  queues messages). The daemon drains it on start/interval. Zero infrastructure,
  zero trust change.
- **Push (opt-in):** the daemon runs on a box the builder controls
  ($5 VPS + Tailscale), reusing the existing remote-access/pairing story.
  Documented, never hosted.

**No multi-tenant SaaS.** Hosting the executor means hosting arbitrary code
execution — a liability and abuse surface that also dissolves the "runs on your
machine, nothing leaves unless you say so" moat (see `security-model.md`).

## Build order (each a small, independently shippable slice)

1. Capture endpoint + draft semantics — reuse `POST /initiatives` and the
   one-gesture brief (`idea new --file/--doc/--from`, Home compose).
2. Telegram bot — capture → draft; judgment taps; deep-link everything else.
3. Email inbox drain (pull model) — the no-config fallback.
4. Self-host VPS + Tailscale guide (push opt-in).
5. Interactive Slack taps — only if the small-team ICP is worth serving.
