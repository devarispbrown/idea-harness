# Email providers

The daemon's email notifier sends over SMTP (`integrations.email` in
`harness.yaml`). Every provider below uses **port 587 with STARTTLS**, which the
daemon handles automatically; the password goes in an environment variable named
by `smtp_password_env`, never inline.

Test with **Mailpit** (a local catch-all — no account, no delivery), then bring
your own provider. Two rules apply everywhere:

- **`from` must be a verified address/domain** for that provider (or, for Gmail,
  your own address).
- **`smtp_user` + `smtp_password_env`** are the login; leave both unset only for
  Mailpit.

## Mailpit (testing)

```bash
docker run -d -p 1025:1025 -p 8025:8025 axllent/mailpit
```

```yaml
integrations:
  email:
    to: test@example.com        # any address — Mailpit catches everything
    from: idea@example.com
    smtp_host: 127.0.0.1
    smtp_port: 1025             # no auth; leave smtp_user/password unset
```

Every email lands at `http://localhost:8025` and is never actually delivered.

## Postmark

1. Create a server, then copy its **Server API token** (Server → API Tokens).
2. Verify a sender signature or domain.
3. The token is used as **both** the username and the password.

```yaml
integrations:
  email:
    to: you@example.com
    from: you@yourdomain.com                  # a verified sender
    smtp_host: smtp.postmarkapp.com
    smtp_port: 587
    smtp_user: <postmark-server-api-token>
    smtp_password_env: SMTP_PASSWORD          # the SAME token
```

## Resend

1. Copy your **API key** (resend.com → API Keys).
2. Verify a domain.

```yaml
integrations:
  email:
    to: you@example.com
    from: you@yourdomain.com                  # a verified domain
    smtp_host: smtp.resend.com
    smtp_port: 587
    smtp_user: resend                         # literally "resend"
    smtp_password_env: SMTP_PASSWORD          # the API key
```

## Amazon SES

1. In the SES console, open **SMTP settings** → **Create SMTP credentials**.
   These are **not** your IAM keys — SES mints a separate username/password.
2. Verify a sending address or domain (sandbox: verify every recipient too).

```yaml
integrations:
  email:
    to: you@example.com
    from: you@yourdomain.com                  # a verified sender
    smtp_host: email-smtp.us-east-1.amazonaws.com   # match your region
    smtp_port: 587
    smtp_user: <ses-smtp-username>
    smtp_password_env: SMTP_PASSWORD          # the SES SMTP password
```

The host is `email-smtp.<region>.amazonaws.com` — use your region (e.g.
`us-east-1`, `eu-west-1`, `ap-southeast-1`).

## SendGrid

1. Create an **API key** (Settings → API Keys → "Mail Send" or full access).
2. Verify a single sender or domain.

```yaml
integrations:
  email:
    to: you@example.com
    from: you@yourdomain.com                  # a verified sender
    smtp_host: smtp.sendgrid.net
    smtp_port: 587
    smtp_user: apikey                         # literally "apikey"
    smtp_password_env: SMTP_PASSWORD          # the API key
```

## Gmail

1. Enable **2-Step Verification**, then create an **App Password**
   (myaccount.google.com → Security → App passwords). It is 16 characters, not
   your account password.

```yaml
integrations:
  email:
    to: you@gmail.com
    from: you@gmail.com
    smtp_host: smtp.gmail.com
    smtp_port: 587
    smtp_user: you@gmail.com
    smtp_password_env: SMTP_PASSWORD          # the 16-char app password
```

Gmail's SMTP needs the app password, not the account password ("less secure
apps" is retired). It also has low sending limits, so it is for personal use,
not production volume.
