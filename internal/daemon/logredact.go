package daemon

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// logRedactor is the daemon log's redactor: the credential pattern catalog plus
// every secret value any loaded project has resolved, plus the daemon's own API
// token.
//
// It is daemon-wide rather than per-project because the log is: one daemon.log
// holds lines from every project, and a project-A secret can surface in a line
// logged while serving project B (a shared provider key is the ordinary case).
// The set only grows -- a secret removed from a project's .env stays masked
// until the daemon restarts, which over-masks a log line and never under-masks
// one.
//
// RedactString is on the hot path of every log record, so it reads an
// immutable *security.Redactor through an atomic pointer; add rebuilds it under
// a mutex, which only happens when a project is loaded or reloaded.
type logRedactor struct {
	mu    sync.Mutex
	known map[string]bool
	cur   atomic.Pointer[security.Redactor]
}

func newLogRedactor(values ...string) *logRedactor {
	l := &logRedactor{known: map[string]bool{}}
	l.cur.Store(security.NewRedactor())
	l.add(values...)
	return l
}

// add registers more known secret values and rebuilds the redactor when any of
// them is new.
func (l *logRedactor) add(values ...string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !l.known[v] {
			l.known[v] = true
			changed = true
		}
	}
	if !changed {
		return
	}
	all := make([]string, 0, len(l.known))
	for v := range l.known {
		all = append(all, v)
	}
	sort.Strings(all)
	l.cur.Store(security.NewRedactor(all...))
}

// RedactString masks every known secret value and every credential-pattern
// match in s. A nil logRedactor (a Runtime built as a bare struct literal, as
// some tests do) still applies the pattern catalog: it never fails open.
func (l *logRedactor) RedactString(s string) string {
	if l == nil {
		return security.Redact(s)
	}
	return l.cur.Load().RedactString(s)
}

// redactingLogger wraps log so every record passes through lr before reaching
// its handler -- the stderr/daemon.log text handler cli.newDaemonLogger builds.
func redactingLogger(log *slog.Logger, lr *logRedactor) *slog.Logger {
	return slog.New(security.NewRedactingHandler(log.Handler(), lr.RedactString))
}

// processSecrets is what the daemon can know before any project is loaded: the
// built-in provider presets' keys from its own environment.
func processSecrets() []string {
	return collectSecrets(config.Config{}, nil)
}
