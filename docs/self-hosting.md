# Self-hosting: run the daemon always-on

"Create from anywhere" only works while the daemon is reachable. This guide runs
it always-on on hardware you control — your own machine, or a cheap box in the
cloud — reachable from your devices over Tailscale and from nobody else. There is
no multi-tenant SaaS; the daemon stays on your hardware, with your keys and your
review (see [`icp-and-channels.md`](icp-and-channels.md) and
[`security-model.md`](security-model.md)).

The remote-access *mechanism* is already documented in
[`configuration.md`](configuration.md#remote-access) — `tailscale serve`, the
`daemon:` settings, `idea ui pair`. This page is the *operational* walkthrough:
how to keep the daemon running, on each platform.

## What you need

- **The binary with the web UI embedded.** Use a release binary, or build it with
  `make build`. Do **not** use `go install ./cmd/idea` for self-hosting: that
  builds the CLI without the web UI, so a remote browser gets a "web UI not
  built" page and there is nothing to pair into.
- **Tailscale** (free) on the box and on every device you create or judge from.
- **Docker**, only if you want a real coding agent (`hermes`/`omp`/`acp`/
  `openhands`). The deterministic `internal` placeholder needs no Docker and no
  model key, so you can stand the whole thing up first and wire an agent later.
- **A model provider key**, when you move off the placeholder — held in the
  daemon's environment or the project's `.env` (see
  [`agents.md`](agents.md) and [`configuration.md`](configuration.md)).

State lives under `~/.local/share/idea-harness/` on every platform, or
`$IDEA_HARNESS_STATE` if you point it elsewhere.

## Local (headless)

Run `idea daemon` in the foreground under the OS's service manager so it starts
at boot and restarts after a crash. Then add remote access once, with
`tailscale serve --bg 8787` and the machine's MagicDNS name in `allowed_hosts`
([`configuration.md`](configuration.md#remote-access); the security policy is
[`security-model.md`](security-model.md#remote-access)), and `idea ui pair` to
sign your phone in.

### Linux — systemd

A **user** unit (no root) at `~/.config/systemd/user/idea-harness.service`:

```ini
[Unit]
Description=idea-harness daemon
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/idea daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now idea-harness
loginctl enable-linger          # keep it running while you're logged out
```

### macOS — launchd

`~/Library/LaunchAgents/com.idea-harness.daemon.plist`:

```xml
<?xml version="1.0"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.idea-harness.daemon</string>
  <key>ProgramArguments</key>
  <array><string>/usr/local/bin/idea</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/idea-harness.log</string>
  <key>StandardErrorPath</key><string>/tmp/idea-harness.log</string>
</dict></plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.idea-harness.daemon.plist
```

### Windows — NSSM

Install [NSSM](https://nssm.cc/), then from an elevated PowerShell:

```powershell
nssm install idea-harness "C:\tools\idea.exe" daemon
nssm set idea-harness AppStdout "C:\tools\idea-harness.log"
nssm set idea-harness AppStderr "C:\tools\idea-harness.log"
nssm start idea-harness
```

(Task Scheduler with "run whether the user is logged on or not" also works, but
NSSM restarts a crashed daemon, which is what you want.)

## Cloud VPS

Any small Linux box works; the recipe is the same everywhere. A plain VPS is a
better fit than a PaaS like Render for a stateful daemon — Render's filesystem is
ephemeral unless you attach a persistent disk, so a VPS with a real disk is
simpler. Ubuntu 22.04/24.04 LTS is the safe default.

1. **Provision** the box (table below) and note its Tailnet IP.
2. **Install Tailscale:** `curl -fsSL https://tailscale.com/install.sh | sh && tailscale up`.
3. **Install Docker** (for a real agent) and the `idea` release binary at
   `/usr/local/bin/idea`.
4. **Write a system service** at `/etc/systemd/system/idea-harness.service`
   (add `User=idea` and run as a dedicated user, not root):

   ```ini
   [Unit]
   Description=idea-harness daemon
   After=network-online.target docker.service

   [Service]
   User=idea
   ExecStart=/usr/local/bin/idea daemon
   Restart=on-failure
   RestartSec=5
   Environment=IDEA_HARNESS_STATE=/var/lib/idea-harness

   [Install]
   WantedBy=multi-user.target
   ```

   ```bash
   systemctl daemon-reload && systemctl enable --now idea-harness
   ```

5. **Expose it on the tailnet only:** run `tailscale serve --bg 8787`, then add
   the box's MagicDNS name to `daemon.allowed_hosts` in `config.yaml` — settings
   in [`configuration.md`](configuration.md#remote-access), security policy in
   [`security-model.md`](security-model.md#remote-access).
6. **Pair your phone:** `idea ui pair --qr` on the box, scan from the phone, done.

### Provider notes

| Provider | Sizing | Notes |
|---|---|---|
| **DigitalOcean** | smallest Droplet ($4–6/mo) | Simplest all-round choice; persistent disk, trivial firewall. |
| **Hetzner** | CX22 (x86) or CAX11 (ARM) | Cheapest for the specs; great value, EU datacenters. |
| **AWS** | Lightsail ($5/mo) or a `t4g.nano` EC2 | Use Lightsail for the simple firewall; with EC2 leave the security group's inbound empty. |
| **GCP** | `e2-micro` (free-tier eligible) or `e2-small` | Leave the default deny-inbound firewall; nothing needs to reach the box from outside the tailnet. |
| **Render** | paid service | Possible but awkward: attach a persistent disk for state. Prefer a VPS for a stateful daemon. |

## Security and operations

- **No public ports.** Tailscale needs no inbound firewall holes (it does NAT
  traversal), so leave the cloud firewall default-deny inbound. Only outbound is
  needed: the model API, Docker pulls, and Tailscale.
- **The box is now the trust boundary.** Your provider keys and project state
  live on it. Use a scoped, rotatable key, keep the box on your tailnet only, and
  update it.
- **Back up the state.** `~/.local/share/idea-harness/` (or your
  `IDEA_HARNESS_STATE`) plus your project directories are the whole product.
  Snapshot them on a schedule; the store is SQLite + blobs, so a file-level
  backup while the daemon is stopped is complete.
- **HTTPS matters.** Browsers only install the app to the home screen and allow
  push on a secure context, so `tailscale serve` (which terminates HTTPS) is the
  recommended path over a plain-HTTP Tailscale IP.
