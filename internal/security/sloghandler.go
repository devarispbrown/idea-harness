package security

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// NewRedactingHandler wraps inner so that every log record passes through
// redact before inner formats or writes it: the message, every attribute value
// (string, error, fmt.Stringer, or any other value), nested groups, and the
// attributes bound earlier with Logger.With.
//
// It exists because the daemon log was the one persisted surface the redaction
// choke point did not reach (audit 3 / S4): daemon.log is written to disk and
// served by GET /api/v1/logs, and the daemon logs wrapped errors that can carry
// a provider's response body, a DSN, or a command's output verbatim.
//
// Redaction happens per value, not on the formatted line, so it works under any
// inner handler (text or JSON) without parsing its output. That loses one shape
// a line-level pass would see: the generic credential-assignment rule keys on
// the NAME next to the value ("password=..."), and an attribute's key and value
// only meet in the formatted output. redactAttr reconstructs that pairing
// explicitly (key + "=" + value) so `log.Info("x", "password", "hunter2xyz")`
// is masked the same way the same text in a message would be.
//
// A value whose redaction changes nothing is passed through untouched, with
// its original kind, so a clean []string or struct keeps the inner handler's
// native formatting (a JSON array stays an array). Only a value that actually
// carried a credential is replaced, by its redacted string form.
//
// Group names and attribute keys are not redacted: they are constants in the
// daemon's own source, never data.
func NewRedactingHandler(inner slog.Handler, redact func(string) string) slog.Handler {
	if redact == nil {
		redact = Redact
	}
	return &redactingHandler{inner: inner, redact: redact}
}

type redactingHandler struct {
	inner  slog.Handler
	redact func(string) string
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = h.redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(red), redact: h.redact}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), redact: h.redact}
}

// redactAttr returns a with its value redacted, recursing into groups.
// LogValuer values are resolved first, so a type that lazily renders itself
// cannot smuggle a credential past the handler.
func (h *redactingHandler) redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		red := make([]slog.Attr, len(group))
		for i, g := range group {
			red[i] = h.redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(red...)}
	case slog.KindString:
		s := v.String()
		if r := h.redactKeyed(a.Key, s); r != s {
			return slog.String(a.Key, r)
		}
		return slog.Attr{Key: a.Key, Value: v}
	case slog.KindAny:
		var s string
		switch x := v.Any().(type) {
		case nil:
			return slog.Attr{Key: a.Key, Value: v}
		case error:
			s = x.Error()
		case fmt.Stringer:
			s = x.String()
		case []byte:
			s = string(x)
		default:
			s = fmt.Sprintf("%+v", x)
		}
		if r := h.redactKeyed(a.Key, s); r != s {
			return slog.String(a.Key, r)
		}
		return slog.Attr{Key: a.Key, Value: v}
	default:
		// Int, Uint, Float, Bool, Duration, Time: no credential can live in
		// a number or a timestamp, and rendering one to a string would change
		// how the inner handler formats it.
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// redactKeyed redacts s on its own, and then again as "key=s" so a rule that
// keys on the variable name (the generic credential-assignment rule:
// password=..., api_key=..., token=...) sees the same text it would in a
// formatted line.
func (h *redactingHandler) redactKeyed(key, s string) string {
	s = h.redact(s)
	if key == "" {
		return s
	}
	prefix := key + "="
	joined := h.redact(prefix + s)
	if strings.HasPrefix(joined, prefix) {
		return joined[len(prefix):]
	}
	// The match swallowed the key itself. Keep the whole redacted form rather
	// than guess where the value begins: over-masking a log line is harmless.
	return joined
}
