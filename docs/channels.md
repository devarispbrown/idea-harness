# Wiring a channel

The capture endpoint (`POST /api/v1/capture`), the capture token (`idea capture
token`), and the native Telegram poller are built. This page is the wiring for
the three channels: **Telegram** (capture — the daemon runs the bot itself),
**Slack** (create/monitor/operate — needs an adapter), and **email** (outbound
notifications — needs an SMTP notifier).

Channels are configured in `<state root>/config.yaml` under a `channels:` section.
Credentials are **env-var names, never values**, so the file stays committable.

## Prerequisite (all channels)

**An always-on daemon.** A channel's bot has to reach the daemon, so run it on a
box or VPS — see [`self-hosting.md`](self-hosting.md) and `idea remote enable`.

---

## Telegram (capture) — native, ready today

The daemon long-polls the bot and turns each text message into a **draft**
initiative. No separate script, no capture token.

**1. Create the bot.** In Telegram (desktop or mobile — same flow), search for
`@BotFather` — use the **verified** one (blue checkmark; there are impersonators)
— start a chat, and send `/newbot`. It asks for two things, in order:

- a **display name** (anything, e.g. "My Idea Bot"), then
- a **username** (must end in `bot`, lowercase, e.g. `my_idea_bot`).

It then replies with the token (`1234567890:AA…`). Copy that.

**2. Put the token in the daemon's environment.** The daemon reads it from its
own process environment, so export it in the shell where you run `idea`, then
restart the daemon (step 4) from that same shell:

```bash
export TELEGRAM_BOT_TOKEN="1234567890:AA…"
```

(To make it stick across reboots, put the `export` in your shell profile —
`~/.zshrc` or `~/.bashrc` — and restart the daemon after.)

**3. Configure the channel** in `~/.local/share/idea-harness/config.yaml` (or
`$IDEA_HARNESS_STATE/config.yaml`):

```yaml
channels:
  telegram:
    bot_token_env: TELEGRAM_BOT_TOKEN   # the env var name, never the token itself
    project: <project-id>               # the 32-char id in the web UI address (#/p/<id>/)
```

`<project-id>` is the 32-character id you see in the web UI's address bar
(`http://127.0.0.1:8787/#/p/<id>/…`) — the same value `idea init` printed when
you created the project.

**4. Restart the daemon** so it picks up the config: `idea daemon stop`, then any
command starts it again.

**5. Verify.** In Telegram, open a chat with your bot and send "A recipe sharing
app for home cooks"; it appears on the Home page as a **draft** initiative. Open
it → **Start**.

---

## Slack (create / monitor / operate) — native

The daemon runs the Slack adapter itself over **Socket Mode** (no public URL —
the daemon opens a WebSocket to Slack). The canonical setup — including the app
**manifest** you paste to get the scopes, Socket Mode and Interactivity in one
step — is [`integrations.md`](integrations.md). In short:

**1. Create the app from the manifest** at <https://api.slack.com/apps>
([`integrations/slack-manifest.yaml`](integrations/slack-manifest.yaml)).

**2. Copy the Bot User OAuth Token** (`xoxb-…`) and generate an **app-level
token** (`xapp-…`, `connections:write` scope).

**3. Put them in the daemon's environment:**

```bash
export SLACK_BOT_TOKEN="xoxb-…"
export SLACK_APP_TOKEN="xapp-…"
```

**4. Configure `integrations.slack`** in `harness.yaml` — `channel` (a channel ID)
and `allowed_users` (who may act; empty means notify-only) — then **invite the
bot to the channel** (`/invite @<bot>`).

**5. Restart the daemon** and check it: `idea integrations test slack`. Then use:

| Slash command | Does |
|---|---|
| `/idea new <idea>` | start an initiative |
| `/idea status` | list this project's initiatives |
| `/idea approve <id>` | approve a PRD |
| `/idea advance <id>` | advance to design & plan |
| `/idea execute <id>` | run the coding agent |
| `/idea review <change-set> approve\|reject` | review a change set |
| `/idea apply <change-set>` | apply a change set |

Operate commands go through the same gates as the CLI — a refusal from Slack is
the same message the terminal would print. A change-set review that refuses over
blocking findings is resolved by accepting them: `idea review CHG-1 approve
--accept-findings` from the CLI. The buttons in the notification threads
(approve/reject a change set, accept recommendations, answer a question) still
work alongside the slash commands.

---

## Email (outbound notifications) — native

Email is notifications-only (never a capture channel): the daemon emails you the
same "needs your judgment" events it sends to Slack and Web Push.

**1. Get SMTP credentials** — a transactional provider (Postmark, Resend, SES,
SendGrid) or Gmail. Per-provider snippets (hosts, ports, the exact
username/password to use): [`email-providers.md`](email-providers.md).

**2. Put the password in an env var:**

```bash
export SMTP_PASSWORD="…"
```

**3. Configure `integrations.email` in `harness.yaml`:**

```yaml
integrations:
  email:
    to: you@example.com
    from: idea@example.com
    smtp_host: smtp.postmarkapp.com
    smtp_port: 587                  # default 587
    smtp_user: <your-api-key>
    smtp_password_env: SMTP_PASSWORD   # env var NAME, never the password
    # events: [initiative.needs_judgment, change_set.ready_for_review, verify.failed, outcome.drifted]
```

**4. Restart the daemon.** Each wanted event lands in your inbox as a one-line
subject with the reason and a deep link. `events` defaults to the same set Slack
uses; narrow it to just the four "needs you" kinds if you only want pings.
