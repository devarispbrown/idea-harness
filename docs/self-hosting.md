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
try it, keep it running, then add a real agent.

State lives under `~/.local/share/idea-harness/` on every platform, or
`$IDEA_HARNESS_STATE` if you point it elsewhere.

## 1. Try it in five minutes

Prove the whole remote path on your own machine before you make anything
permanent or spend a cent on a box.

**Install two things:** the release binary (the web UI is built in — do **not**
use `go install ./cmd/idea`, which omits it), and Tailscale on this machine *and*
on your phone. Sign both in to the same tailnet.

**Turn on remote access:**

```bash
tailscale serve --bg 8787            # prints https://<machine>.ts.net — copy the hostname
idea daemon stop
idea daemon --allowed-hosts <machine>.ts.net   # foreground; leave this terminal open
```

In a second terminal:

```bash
idea ui pair --qr                    # scan from your phone
```

**Did it work?** Open the `tailscale serve` address on your phone, enter the code,
and you land on the Home page. A **403 "invalid host"** means the name passed to
`--allowed-hosts` is not the one the browser sent — re-check `tailscale serve`'s
output and restart. A **401** means the pairing code was wrong or expired — run
`idea ui pair` again.

The policy behind `tailscale serve` and the remote listener:
[`configuration.md`](configuration.md#remote-access),
[`security-model.md`](security-model.md#remote-access).

Once the phone is signed in, everything below just **keeps it running** and,
optionally, gives it a real coding agent.

## 2. Keep it running

Wrap `idea daemon` in the OS's service manager so it starts at boot and restarts
after a crash. If you just did the try-it path, `idea daemon stop` first, so the
service takes over. The service runs `idea daemon` with no flags, so move the
hostname into `config.yaml`'s `daemon:` section (`daemon.allowed_hosts`) first —
the key and its policy are in [`configuration.md`](configuration.md#remote-access)
and [`security-model.md`](security-model.md#remote-access).

### Local — Linux (systemd, user unit)

`~/.config/systemd/user/idea-harness.service`:

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
loginctl enable-linger            # keep it running while you're logged out
```

### Local — macOS (launchd)

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

### Local — Windows (NSSM)

Install [NSSM](https://nssm.cc/), then from an elevated PowerShell:

```powershell
nssm install idea-harness "C:\tools\idea.exe" daemon
nssm set idea-harness AppStdout "C:\tools\idea-harness.log"
nssm set idea-harness AppStderr "C:\tools\idea-harness.log"
nssm start idea-harness
```

(Task Scheduler with "run whether the user is logged on or not" also works, but
NSSM restarts a crashed daemon, which is what you want.)

### Cloud — a box that's always on

A plain VPS is a better fit than a PaaS like Render for a stateful daemon —
Render's filesystem is ephemeral unless you attach a persistent disk. Ubuntu
22.04/24.04 LTS is the safe default. You have already done the remote-access step
above; the only new part is the box and the service.

1. **Provision** the box (table below).
2. **Install Tailscale:** `curl -fsSL https://tailscale.com/install.sh | sh && tailscale up`.
3. **Install the `idea` release binary** at `/usr/local/bin/idea`.
4. **Write a system service** at `/etc/systemd/system/idea-harness.service`
   (run as a dedicated user, not root):

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

5. **Expose it on the tailnet only:** repeat the "turn on remote access" step —
   `tailscale serve --bg 8787`, pass the box's MagicDNS name to `--allowed-hosts`
   (or set it in `config.yaml`), then `idea ui pair --qr`.

| Provider | Sizing | Notes |
|---|---|---|
| **DigitalOcean** | smallest Droplet ($4–6/mo) | Simplest all-round choice; persistent disk, trivial firewall. |
| **Hetzner** | CX22 (x86) or CAX11 (ARM) | Cheapest for the specs; great value, EU datacenters. |
| **AWS** | Lightsail ($5/mo) or a `t4g.nano` EC2 | Use Lightsail for the simple firewall; with EC2 leave the security group's inbound empty. |
| **GCP** | `e2-micro` (free-tier eligible) or `e2-small` | Leave the default deny-inbound firewall; nothing needs to reach the box from outside the tailnet. |
| **Render** | paid service | Possible but awkward: attach a persistent disk for state. Prefer a VPS for a stateful daemon. |

## 3. Give it a real coding agent (optional)

Everything above works with the deterministic `internal` executor, which writes a
placeholder and needs no Docker and no key. To write real code:

- **Install Docker** on the box.
- **Configure a model key** — in the daemon's environment or the project's
  `.env` (see [`agents.md`](agents.md)).
- **Choose an executor:** `idea executors setup --kind hermes --model <provider/model> --smoke`.

## 4. Keep it safe

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
