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

**1. Create the bot.** In Telegram, message `@BotFather`, send `/newbot`, name
it, and copy the bot token (`1234567890:AA…`).

**2. Put the token in the daemon's environment:**

```bash
export TELEGRAM_BOT_TOKEN="1234567890:AA…"
```

**3. Configure the channel** in `~/.local/share/idea-harness/config.yaml` (or
`$IDEA_HARNESS_STATE/config.yaml`):

```yaml
channels:
  telegram:
    bot_token_env: TELEGRAM_BOT_TOKEN   # the env var name, never the token itself
    project: <project-id>               # where drafts land (idea status shows ids)
```

**4. Restart the daemon** so it picks up the config: `idea daemon stop`, then any
command starts it again.

**5. Verify.** Text your bot "A recipe sharing app for home cooks"; it appears on
the Home page as a **draft** initiative. Open it → **Start**.

---

## Slack (create / monitor / operate) — native

The daemon runs the Slack adapter itself over **Socket Mode** (no public URL —
the daemon opens a WebSocket to Slack). Configure `integrations.slack` in
`harness.yaml` (socket mode on, `allowed_users`, `channel`, and the app/bot token
env names), as in [`configuration.md`](configuration.md) and
[`integrations.md`](integrations.md). Then add the app:

**1. Create the app.** <https://api.slack.com/apps> → **Create New App** → **from
scratch** → name it, pick a workspace.

**2. Enable Socket Mode** → on → copy the **app-level token** (`xapp-…`).

**3. Add a slash command** `/idea`. In socket mode the request URL is unused.

**4. Copy the bot token** (`xoxb-…`) under **OAuth & Permissions**.

**5. Put both tokens in the env vars** your `harness.yaml` names, restart the
daemon, and use:

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

**1. Get SMTP credentials** — a transactional provider (Postmark, Resend, SES) or
your own server: host, port, username/password, and a from address.

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
