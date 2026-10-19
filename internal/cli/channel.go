package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// cmdChannel configures a channel by writing harness.yaml's `integrations:`
// section — no hand-editing. Each subcommand sets the fields for one channel and
// leaves the rest untouched; credentials are env-var names, never values.
func cmdChannel(a cmdArgs, stdout, _ io.Writer) error {
	cwd, _ := os.Getwd()
	dir := filepath.Join(cwd, config.Dir)
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	h := cfg.Harness

	switch a.sub {
	case "telegram":
		env := a.value("--token-env")
		if env == "" {
			env = "TELEGRAM_BOT_TOKEN"
		}
		h.Integrations.Telegram = &config.TelegramChannelConfig{BotTokenEnv: env}
		if err := config.WriteHarness(dir, h); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ telegram configured"))
		fmt.Fprintf(stdout, "  put the bot token in %s, then restart: %s\n", bold(env), muted("idea daemon stop"))
		return nil

	case "slack":
		channel := a.value("--channel")
		if channel == "" {
			return usageErrorf("--channel <id> is required (a channel ID, not a #name)")
		}
		sc := &config.SlackConfig{Channel: channel}
		if v := a.value("--allowed-users"); v != "" {
			sc.AllowedUsers = splitCSV(v)
		}
		if v := a.value("--bot-token-env"); v != "" {
			sc.BotTokenEnv = v
		}
		if v := a.value("--app-token-env"); v != "" {
			sc.AppTokenEnv = v
		}
		h.Integrations.Slack = sc
		if err := config.WriteHarness(dir, h); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ slack configured"))
		fmt.Fprintf(stdout, "  set %s (xoxb-…) and %s (xapp-…), then restart: %s\n",
			bold(sc.BotTokenEnvName()), bold(sc.AppTokenEnvName()), muted("idea daemon stop"))
		fmt.Fprintln(stdout, muted("  invite the bot to the channel (/invite @<bot>) and test with `idea integrations test slack`"))
		return nil

	case "email":
		to, from, host := a.value("--to"), a.value("--from"), a.value("--smtp-host")
		if to == "" || from == "" || host == "" {
			return usageErrorf("--to, --from and --smtp-host are all required")
		}
		ec := &config.EmailConfig{To: to, From: from, SMTPHost: host}
		if v := a.value("--smtp-port"); v != "" {
			if n, err := parsePositiveInt(v); err != nil {
				return err
			} else {
				ec.SMTPPort = n
			}
		}
		if v := a.value("--smtp-user"); v != "" {
			ec.SMTPUser = v
		}
		if v := a.value("--smtp-password-env"); v != "" {
			ec.SMTPPasswordEnv = v
		}
		h.Integrations.Email = ec
		if err := config.WriteHarness(dir, h); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ email configured"))
		fmt.Fprintf(stdout, "  set the password in %s, then restart: %s\n", bold(ec.SMTPPasswordEnvName()), muted("idea daemon stop"))
		return nil

	case "show":
		renderChannelConfig(h.Integrations, stdout)
		return nil

	default:
		return usageErrorf("usage: idea channel telegram|slack|email|show")
	}
}

func renderChannelConfig(ic config.IntegrationsConfig, stdout io.Writer) {
	switch {
	case ic.Telegram == nil && ic.Slack == nil && ic.Email == nil:
		fmt.Fprintln(stdout, muted("no channels configured — `idea channel telegram|slack|email …` sets one up"))
	default:
		if ic.Telegram != nil {
			fmt.Fprintf(stdout, "telegram: bot token from %s\n", bold(ic.Telegram.BotTokenEnvName()))
		}
		if ic.Slack != nil {
			fmt.Fprintf(stdout, "slack: channel %s, %d allowed user(s)\n", bold(ic.Slack.Channel), len(ic.Slack.AllowedUsers))
		}
		if ic.Email != nil {
			fmt.Fprintf(stdout, "email: %s → %s via %s:%d\n", ic.Email.From, ic.Email.To, ic.Email.SMTPHost, ic.Email.EffectivePort())
		}
	}
}

func parsePositiveInt(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 {
		return 0, usageErrorf("%q is not a positive integer", s)
	}
	return n, nil
}
