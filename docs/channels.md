# Wiring a channel

The capture endpoint (`POST /api/v1/capture`) and the capture token (`idea
capture token`) are built. This page is the explicit, copy-pasteable wiring for
the three channels: **Telegram** (capture — ready to wire today), **Slack**
(create/monitor/operate — needs an adapter), and **email** (outbound
notifications — needs an SMTP notifier).

Every channel needs the same two prerequisites first.

## Prerequisites (all channels)

1. **An always-on daemon.** A channel's bot has to reach the daemon, so it must
   be running somewhere reachable — your box or a VPS. See
   [`self-hosting.md`](self-hosting.md) and `idea remote enable`.
2. **A capture token**, minted in the project the captures should land in:

   ```bash
   idea capture token --label telegram    # prints the token ONCE; store it
   ```

   The capture endpoint is `POST /api/v1/capture`, body `{"token":"…","brief":"…"}`.
   A successful call returns `{"initiative_id":"…"}` and the brief becomes a
   **draft** initiative (pipeline not started). You open it and press **Start**,
   or `POST …/initiatives/{id}/start`.

---

## Telegram (capture) — ready today

**1. Create the bot.** In Telegram, message `@BotFather`, send `/newbot`, name
it, and copy the bot token (`1234567890:AA…`).

**2. Run this adapter** on the always-on box. It long-polls the bot and forwards
each text message to the capture endpoint. Needs `curl` and `jq`.

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${TELEGRAM_BOT_TOKEN:?set TELEGRAM_BOT_TOKEN}"
: "${CAPTURE_TOKEN:?set CAPTURE_TOKEN (from idea capture token)}"
DAEMON="${IDEA_HARNESS_ADDR:-http://127.0.0.1:8787}"
offset=0
while true; do
  updates=$(curl -sf "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates" \
    --get --data-urlencode "timeout=30" --data-urlencode "offset=${offset}") || { sleep 2; continue; }
  echo "$updates" | jq -r '.result[]?.message.text | select(. != null)' | while IFS= read -r brief; do
    jq -n --arg t "$CAPTURE_TOKEN" --arg b "$brief" '{token:$t, brief:$b}' \
      | curl -sf -X POST "$DAEMON/api/v1/capture" -H 'Content-Type: application/json' --data-binary @- >/dev/null \
      || echo "capture failed" >&2
  done
  offset=$(echo "$updates" | jq -r '.result[-1].update_id + 1 // 0')
done
```

**3. Verify.** Text your bot "A recipe sharing app for home cooks"; it appears on
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
