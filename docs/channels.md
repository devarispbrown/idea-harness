# Wiring a channel

Channels are configured in `harness.yaml` under `integrations:` — **one file** —
and the `idea channel` command writes it for you, so there is no hand-editing.
Credentials are **env-var names, never values**.

## Prerequisite (all channels)

**An always-on daemon.** A channel's bot has to reach the daemon, so run it on a
box or VPS — see [`self-hosting.md`](self-hosting.md) and `idea remote enable`.

---

## Telegram (capture) — native

The daemon long-polls the bot and turns each text message into a **draft**
initiative. No separate script, no capture token.

**1. Create the bot.** In Telegram (desktop or mobile — same flow), search for
`@BotFather` — use the **verified** one (blue checkmark; there are impersonators)
— start a chat, and send `/newbot`. It asks for two things, in order:

- a **display name** (anything, e.g. "My Idea Bot"), then
- a **username** (must end in `bot`, lowercase, e.g. `my_idea_bot`).

It then replies with the token (`1234567890:AA…`). Copy that.

**2. Configure it — one command:**

```bash
export TELEGRAM_BOT_TOKEN="1234567890:AA…"    # the @BotFather token
idea channel telegram                          # writes integrations.telegram to harness.yaml
idea daemon stop                               # restart; the next command reloads it
```

`idea channel telegram` writes `integrations.telegram.bot_token_env` (default
`TELEGRAM_BOT_TOKEN`) into `harness.yaml`. The project is implicit — it is the
one whose `harness.yaml` you are in. (To make the token stick across reboots,
put the `export` in your shell profile.)

**3. Verify.** In Telegram, open a chat with your bot and send "A recipe sharing
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

**4. Configure `integrations.slack`** — one command (or hand-edit `harness.yaml`):
`idea channel slack --channel <id> --allowed-users U0123`. Then **invite the bot
to the channel** (`/invite @<bot>`).

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

**2. Configure it — one command:**

```bash
export SMTP_PASSWORD="…"     # the provider password/API key
idea channel email --to you@example.com --from idea@example.com \
  --smtp-host smtp.postmarkapp.com --smtp-user <api-key>
idea daemon stop              # restart; the next command reloads it
```

`idea channel email` writes `integrations.email` into `harness.yaml`. It is the
same shape as the YAML below, so either works. Per-provider snippets (the exact
host/port/username for Postmark, Resend, SES, SendGrid, Gmail, and Mailpit for
testing): [`email-providers.md`](email-providers.md).

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

Each wanted event lands in your inbox as a one-line subject with the reason and a
deep link. `events` defaults to the same set Slack uses; narrow it to just the
four "needs you" kinds if you only want pings.
