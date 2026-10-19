# ICP and channels: capture anywhere, judge anywhere

The product's interaction model is **human-in-the-loop judgment**, not chat. This
page pins the ICP, the interaction split, the channel decisions, and the ingress
and always-on model that make "from anywhere" real.

## Who this is for (ICP)

A **solo technical builder or a small team's technical lead** who:

- wants reviewed, defensible output (idea → PRD → plan → code), not a chatbot;
- wants to **create from anywhere** — capture an idea the moment it strikes, from
  any device, and start and steer the work without sitting at a desk;
- is time-poor and context-switches, so pings land at odd moments;
- values control: their keys, their code, their review, their hardware.

What they hire idea-harness for: turn ideas into reviewed, verifiable work, with
themselves as the reviewer of record — **and to do that from anywhere, not just at
a desk.**

## The interaction model (everything else follows)

Two interaction classes, with different channel needs:

| Class | What the human does | Bandwidth | Best surface |
|---|---|---|---|
| **Capture** | "idea: a CLI that …" | low, one-shot | phone / messaging |
| **Judge** | read, resolve, approve, verify | high, needs context | the web UI, deep-linked |

Capture is chat-shaped; judgment is context-shaped. A channel's job is to ping
and hand off — it must never replace the judgment UI.

**Context-before-action** — the one rule every channel action obeys: *no blind
action.* Before any accept, the action shows its options and their tradeoffs
(Slack interactive message / Telegram inline keyboard with the tradeoff text). If
the context genuinely cannot fit inline — a full diff, a PRD with findings — the
action is a deep link, not a button. A stray tap must never approve a PRD or
apply a diff without the human having seen why.

## Channels

| Channel | Role | Status |
|---|---|---|
| **Slack** | **operational control plane** — create, monitor, operate via slash commands and interactive messages | outbound notifications today; build out |
| **Telegram** | capture + judgment taps (the solo-builder entry) | add |
| **Web push** | phone notifications, deep-links | keep |
| **Email** | notifications only (outbound) | add (outbound) |
| **Linear** | two-way issue sync | keep |
| **SMS** | — | skip — can't carry context, costs per message |

Slack is the flagship for the team ICP; Telegram for the solo ICP. Email is
notifications-only, never a capture channel.

## The ingress model (capability-tiered)

Capture and operate from a channel are **unauthenticated write paths into a
local-first daemon** — a security-model change, not just a feature. Rules:

1. **Capability tokens, tiered by blast radius.**
   - *Capture token* — creates a draft only. It can never start the pipeline,
     spend budget, or touch the executor.
   - *Operate token* — per channel (Slack), for the judgment actions. Each
     operation carries its own context (options + tradeoffs, the diff, the
     findings) and is refused otherwise.
   - Both follow the push renewal token precedent: SHA-256 hashed, never listed,
     constant-time compared, one identical 401 on refusal.
2. **Capture = draft only.** A captured idea creates a draft initiative (brief
   saved, pipeline **not** started). Grounding docs and Start happen from the real
   UI.
3. **Operate = context-before-action.** Approve/advance/review/apply from Slack
   render what is being judged before the button; what cannot fit inline deep-links.
4. **Who runs the relay.** Slack and Telegram host the bots (the always-on relay).
   A compromised relay can at worst inject drafts under a capture token; an
   operate token is channel-scoped and never permits a blind judgment. No secrets
   in; everything is redacted on ingest and in notifications.

## Always-on: a self-hosted box is the recommended setup

"From anywhere" means the daemon must be reachable when you are not at your
machine. The recommended setup is a box you control, reachable over Tailscale.

- **Local** — Windows, macOS, Linux: run the daemon headless, join it to your
  Tailnet, reach it from any device.
- **Cloud** — Render, Digital Ocean, Hetzner, AWS, GCP: a cheap always-on box,
  the same way, over Tailscale.
- **Pull inbox (fallback)** — the channel queues and the daemon drains on start.
  For people who do not want an always-on box. Lower fidelity: nothing happens
  until the daemon wakes.

**No multi-tenant SaaS.** The daemon stays on hardware the builder controls —
their laptop or their box. Hosting the executor would mean hosting arbitrary code
execution, and it dissolves the "runs on your machine, nothing leaves unless you
say so" moat (see `security-model.md`).

## Build order (each a small, independently shippable slice)

1. **Capture endpoint + draft semantics + capability tokens** — the foundation.
2. **Always-on setup guide** — local (Windows/macOS/Linux) + cloud
   (Render/DigitalOcean/Hetzner/AWS/GCP) over Tailscale.
3. **Slack control plane** — slash commands for create / monitor / operate, with
   context-before-action.
4. **Telegram capture + judgment taps** — the solo-builder entry.
5. **Email notifications** — outbound only.
