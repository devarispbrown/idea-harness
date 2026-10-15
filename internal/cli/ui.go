package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// `idea ui pair` and `idea ui sessions`: remote sign-in to the web UI
// (docs/security-model.md "Remote access").
//
// A browser on another device is never handed the API token. Pairing is the
// one way in: this command, run by someone who holds the token on the
// daemon's machine, mints a one-time code the browser's sign-in page
// exchanges for a session cookie. Sessions are listed and revoked here too,
// so a lost phone is one command away from being signed out.

type uiSession struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	UserAgent  string     `json:"user_agent"`
	RemoteAddr string     `json:"remote_addr"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func cmdUI(a cmdArgs, stdout, _ io.Writer) error {
	if err := ensureDaemon(); err != nil {
		return withKind(kindDaemonUnavailable, err)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	switch a.sub {
	case "pair":
		if a.arg(0) != "" {
			return errorf(kindUsage, "ui pair takes no arguments (use --label <name> to name the device)")
		}
		if a.flags.has("--qr") && a.flags.has("--json") {
			return errorf(kindUsage, "--qr draws for a person; --json is for a program. Pick one")
		}
		var out struct {
			Code      string    `json:"code"`
			ExpiresAt time.Time `json:"expires_at"`
			Remote    bool      `json:"remote"`
			URLs      []string  `json:"urls"`
			burnedCodes
		}
		body := map[string]string{}
		if l := a.value("--label"); l != "" {
			body["label"] = l
		}
		if err := c.do("POST", "/api/v1/ui/pairing-codes", body, &out); err != nil {
			return err
		}
		if a.flags.has("--json") {
			return dumpJSON(stdout, out)
		}
		renderBurnedCodes(stdout, out.burnedCodes)
		if a.flags.has("--qr") {
			return renderPairingQR(stdout, out.Code, out.ExpiresAt, out.Remote, out.URLs, colorEnabled)
		}
		renderPairingCode(stdout, out.Code, out.ExpiresAt, out.Remote, out.URLs)
		return nil
	case "sessions":
		switch a.arg(0) {
		case "":
			if a.flags.has("--all") {
				return errorf(kindUsage, "--all goes with revoke: idea ui sessions revoke --all")
			}
			var out struct {
				Sessions []uiSession `json:"sessions"`
				burnedCodes
			}
			if err := c.do("GET", "/api/v1/ui/sessions", nil, &out); err != nil {
				return err
			}
			if a.flags.has("--json") {
				return dumpJSON(stdout, out)
			}
			renderUISessions(stdout, out.Sessions, time.Now())
			renderBurnedCodes(stdout, out.burnedCodes)
			return nil
		case "revoke":
			id := a.arg(1)
			if a.flags.has("--all") {
				if id != "" {
					return errorf(kindUsage, "give a session id or --all, not both")
				}
				var out struct {
					Revoked int `json:"revoked"`
				}
				if err := c.do("DELETE", "/api/v1/ui/sessions", nil, &out); err != nil {
					return err
				}
				fmt.Fprintln(stdout, ok("✓ revoked")+fmt.Sprintf(" %d session(s); every remote browser must pair again", out.Revoked))
				return nil
			}
			if id == "" {
				return errorf(kindUsage, "usage: idea ui sessions revoke <id> | --all")
			}
			if err := c.do("DELETE", "/api/v1/ui/sessions/"+url.PathEscape(id), nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(stdout, ok("✓ revoked")+" session "+id)
			return nil
		default:
			return errorf(kindUsage, "unknown ui sessions action %q (use revoke)", a.arg(0))
		}
	}
	return errorf(kindUsage, "usage: idea ui pair|sessions")
}

// burnedCodes is the daemon's report of pairing codes burned because too
// many wrong codes were tried while they were outstanding (security-model.md
// "Remote access"): the sign that someone who can reach the sign-in page is
// guessing.
type burnedCodes struct {
	BurnedCodes  int        `json:"burned_codes,omitempty"`
	LastBurnedAt *time.Time `json:"last_burned_at,omitempty"`
}

func renderBurnedCodes(w io.Writer, b burnedCodes) {
	if b.BurnedCodes == 0 {
		return
	}
	when := ""
	if b.LastBurnedAt != nil {
		when = ", last at " + b.LastBurnedAt.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintln(w, warn(fmt.Sprintf("! %d pairing code(s) burned in the last 7 days%s:", b.BurnedCodes, when))+
		" too many wrong codes were tried at the sign-in page while they were outstanding. Someone who can reach it may be guessing; check who can reach this daemon. A new code is unaffected.")
}

func renderPairingCode(w io.Writer, code string, expires time.Time, remote bool, urls []string) {
	fmt.Fprintln(w, bold("Pairing code: ")+bold(code))
	fmt.Fprintln(w, muted(fmt.Sprintf("Good for one sign-in, until %s (%s).", expires.Local().Format("15:04"), time.Until(expires).Round(time.Minute))))
	if !remote {
		fmt.Fprintln(w, warn("This daemon is reachable from this machine only.")+
			" Set daemon.listen or daemon.allowed_hosts (docs/configuration.md \"Remote access\"), or use `tailscale serve`, then open the UI on your other device.")
		return
	}
	fmt.Fprintln(w, "On your other device, open:")
	for _, u := range urls {
		fmt.Fprintln(w, "  "+u)
	}
	fmt.Fprintln(w, muted("and enter the code. Never share it; anyone with it before it is used can sign in."))
}

// renderPairingQR is `idea ui pair --qr`: the first remote URL as a QR code
// (qr.go), and the pairing code printed separately -- never inside the QR,
// because codes never go in URLs. Without a remote URL there is nothing a
// phone could open, so it says so instead of drawing a loopback address.
func renderPairingQR(w io.Writer, code string, expires time.Time, remote bool, urls []string, color bool) error {
	if !remote || len(urls) == 0 {
		renderPairingCode(w, code, expires, remote, urls)
		return nil
	}
	fmt.Fprintln(w, "Scan to open the sign-in page on your other device:")
	fmt.Fprintln(w)
	if err := renderQR(w, urls[0], color); err != nil {
		return fmt.Errorf("drawing the QR code: %w", err)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+urls[0])
	for _, u := range urls[1:] {
		fmt.Fprintln(w, muted("  also: "+u))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Then type this code there (it is not in the QR code):")
	fmt.Fprintln(w, "  "+bold(code))
	fmt.Fprintln(w, muted(fmt.Sprintf("Good for one sign-in, until %s (%s). Never share it; anyone with it before it is used can sign in.",
		expires.Local().Format("15:04"), time.Until(expires).Round(time.Minute))))
	return nil
}

func renderUISessions(w io.Writer, list []uiSession, now time.Time) {
	if len(list) == 0 {
		fmt.Fprintln(w, muted("No browser sessions. `idea ui pair` signs one in."))
		return
	}
	for _, s := range list {
		state := ok("active")
		switch {
		case s.RevokedAt != nil:
			state = muted("revoked")
		case now.After(s.ExpiresAt):
			state = muted("expired")
		}
		name := s.Label
		if name == "" {
			name = describeUA(s.UserAgent)
		}
		fmt.Fprintf(w, "%s  %s  %s\n", bold(s.ID), state, name)
		fmt.Fprintln(w, muted(fmt.Sprintf("    from %s · signed in %s · last seen %s · expires %s",
			orDash(s.RemoteAddr), s.CreatedAt.Local().Format("2006-01-02 15:04"),
			s.LastSeenAt.Local().Format("2006-01-02 15:04"), s.ExpiresAt.Local().Format("2006-01-02"))))
	}
}

// describeUA names a browser from its User-Agent, roughly: enough to tell
// "the phone" from "the tablet" in a list.
func describeUA(ua string) string {
	l := strings.ToLower(ua)
	device := "browser"
	switch {
	case strings.Contains(l, "iphone"):
		device = "iPhone"
	case strings.Contains(l, "ipad"):
		device = "iPad"
	case strings.Contains(l, "android"):
		device = "Android"
	case strings.Contains(l, "mac os"):
		device = "Mac"
	case strings.Contains(l, "windows"):
		device = "Windows"
	case strings.Contains(l, "linux"):
		device = "Linux"
	}
	return device
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
