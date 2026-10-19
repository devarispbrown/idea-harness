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

## Slack (create / monitor / operate) — needs an adapter

The daemon side is ready (capture, and the existing read/judgment routes). What
is missing is the **adapter**: a small service that receives Slack slash commands
and calls the daemon API. This is the next build.

**1. Create the app.** <https://api.slack.com/apps> → **Create New App** → **from
scratch** → name it, pick a workspace.

**2. Enable Socket Mode** (so Slack reaches the adapter without a public URL):
**Socket Mode** → on → copy the **app-level token** (`xapp-…`).

**3. Add a slash command.** **Slash Commands** → **Create New Command** → command
`/idea` → description "Start or steer an initiative". In socket mode the request
URL is unused, but Slack still wants a placeholder.

**4. Copy the bot token** (`xoxb-…`) under **OAuth & Permissions**.

**5. The adapter** (to build) connects to Slack over socket mode and maps:

| Slash command | Daemon call |
|---|---|
| `/idea <brief>` | `POST /api/v1/capture` `{token, brief}` |
| `/idea status` | `GET /api/v1/projects/{id}/initiatives` |
| `/idea approve <id>` | `POST …/initiatives/{id}/approve` |
| `/idea next <id>` | `GET …/initiatives/{id}/guidance` |

Every **operate** action must obey context-before-action (render the diff,
findings, or options before the button) — see [`icp-and-channels.md`](icp-and-channels.md).
The capture token scopes to draft creation only; operate uses the daemon API
token held by the adapter on the box.

---

## Email (outbound notifications) — needs an SMTP notifier

Email is notifications-only (never a capture channel): the daemon emails you the
same "needs your judgment" events it already sends to Slack and Web Push. The
notifier is not built yet — it is the smallest remaining piece, mirroring the
existing Slack notifier.

**1. Get SMTP credentials** — a transactional provider (Postmark, Resend, SES) or
your own server: host, port, username/password (or API key), and a from address.

**2. The notifier** (to build) adds `notifications.email` to `harness.yaml` and a
delivery path on the integration bus, next to Slack and Web Push. The credential
would follow the existing rule: an env-var *name* in config, never the secret.
