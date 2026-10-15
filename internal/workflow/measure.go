package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/tools"
)

// Measuring outcome metrics (`idea metric measure`).
//
// A command source runs in the same isolated container the build/test checks
// use -- no network, read-only root, dropped capabilities -- with the
// workspace mounted read-only at /repo. It never runs on the host: no Docker
// means no measurement, the same fail-closed rule as the checks. There is no
// networked variant: a number that lives behind an API is a url source, which
// is fetched by the daemon's SSRF-guarded fetcher (public addresses only) and
// only once outcomes.allow_url_sources opts in. Neither runs on a timer
// unless a person enables automations.metric_measurement, and then each
// metric only on its own cadence (daemon/automations.go).

// sandboxRunner is the slice of executor.ContainerSandbox measurement needs.
type sandboxRunner interface {
	Run(ctx context.Context, opts executor.RunOptions) (stdout, stderr string, exitCode int, err error)
}

// jsonFetcher is the slice of tools.Fetcher measurement needs.
type jsonFetcher interface {
	FetchJSON(ctx context.Context, url string) ([]byte, error)
}

// MetricSandboxFunc runs a command source in place of the container
// sandbox. It exists for tests outside this package -- the daemon's
// scheduled-measurement tests -- which have no Docker.
type MetricSandboxFunc func(ctx context.Context, opts executor.RunOptions) (stdout, stderr string, exitCode int, err error)

// Run implements the measurement sandbox.
func (f MetricSandboxFunc) Run(ctx context.Context, opts executor.RunOptions) (string, string, int, error) {
	return f(ctx, opts)
}

// SetMetricSandbox replaces the container sandbox command sources run in.
func (e *Engine) SetMetricSandbox(fn MetricSandboxFunc) {
	e.metricSandbox = func(string) sandboxRunner { return fn }
}

// MeasureResult is one metric's measurement outcome. Exactly one of
// Observation, Skipped and Error is set.
type MeasureResult struct {
	MetricID    string                    `json:"metric_id"`
	MetricCode  string                    `json:"metric_code"`
	Observation *domain.MetricObservation `json:"observation,omitempty"`
	Status      *domain.MetricStatus      `json:"status,omitempty"`
	Skipped     string                    `json:"skipped,omitempty"`
	Error       string                    `json:"error,omitempty"`
}

const defaultMeasureTimeout = 120 * time.Second

// numberRe matches a decimal number, optionally signed, with an optional
// exponent. A command's observation is the LAST number it prints, so a
// script may log progress before printing its result.
var numberRe = regexp.MustCompile(`[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)

// MeasureMetrics measures one metric (ref is its code or id) or, with ref
// empty, every live metric with a command or url source. Manual metrics are
// reported as skipped when measured in bulk and refused when named. Each
// successful measurement is recorded as an observation (with attribution and
// drift, see store.RecordObservation); one source failing does not stop the
// rest.
func (e *Engine) MeasureMetrics(ctx context.Context, initiativeID, ref, actor string) ([]MeasureResult, error) {
	now := time.Now()
	var targets []domain.OutcomeMetric
	if ref != "" {
		m, err := e.db.GetMetricByRef(ctx, initiativeID, ref, now)
		if err != nil {
			return nil, err
		}
		if m.Source.Kind == domain.MetricSourceManual || m.Source.Kind == "" {
			return nil, fmt.Errorf("%s has a manual source; record a value with `idea metric record %s <value>`", m.Code, m.Code)
		}
		targets = []domain.OutcomeMetric{*m}
	} else {
		all, err := e.db.ListMetrics(ctx, initiativeID, now)
		if err != nil {
			return nil, err
		}
		targets = all
	}
	out := make([]MeasureResult, 0, len(targets))
	for _, m := range targets {
		res := MeasureResult{MetricID: m.ID, MetricCode: m.Code}
		if m.Source.Kind == domain.MetricSourceManual || m.Source.Kind == "" {
			res.Skipped = "manual source"
			out = append(out, res)
			continue
		}
		value, prov, err := e.measureOne(ctx, m)
		if err != nil {
			res.Error = e.redact(err.Error())
			out = append(out, res)
			continue
		}
		obs := &domain.MetricObservation{
			InitiativeID: initiativeID, MetricID: m.ID, Value: value, Source: m.Source.Kind,
			Provenance: []domain.ProvenanceRef{prov},
		}
		_, after, err := e.db.RecordObservation(ctx, obs, actor, time.Now())
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.Observation, res.Status = obs, &after
		out = append(out, res)
	}
	return out, nil
}

// measureOne reads one value from a command or url source, with the
// provenance of where it came from (the digest of the redacted output).
func (e *Engine) measureOne(ctx context.Context, m domain.OutcomeMetric) (float64, domain.ProvenanceRef, error) {
	snap := e.snapshotConfig().cfg
	switch m.Source.Kind {
	case domain.MetricSourceCommand:
		image := snap.Outcomes.Image
		if image == "" {
			image = snap.Check.Image
		}
		if image == "" {
			image, _ = detectToolchainImage(e.workspaceRoot)
		}
		// "" falls through to the sandbox's own minimal default image.
		timeout := defaultMeasureTimeout
		if snap.Outcomes.MeasureTimeoutSeconds > 0 {
			timeout = time.Duration(snap.Outcomes.MeasureTimeoutSeconds) * time.Second
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		// Under the engine's work root (the project's state directory), like
		// the quality copies: Docker can see it, and a root daemon killed
		// mid-command has its leftover removed at the next start
		// (executor.RecoverWorkRoots). No work root (tests): system temp.
		work, err := e.metricWorkDir()
		if err != nil {
			return 0, domain.ProvenanceRef{}, err
		}
		defer os.RemoveAll(work)
		var sb sandboxRunner
		if e.metricSandbox != nil {
			sb = e.metricSandbox(image)
		} else {
			sb = executor.NewContainerSandbox(image)
		}
		opts := executor.RunOptions{WorkRoot: work, Cmd: []string{"sh", "-c", m.Source.Command}}
		if e.workspaceRoot != "" {
			opts.RepoRoot = e.workspaceRoot
			opts.Cmd = []string{"sh", "-c", "cd /repo && " + m.Source.Command}
		}
		stdout, stderr, code, err := sb.Run(runCtx, opts)
		if err != nil {
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: command timed out after %s", m.Code, timeout)
			}
			// No host fallback, ever: the command is project code.
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: sandbox unavailable (is Docker running?): %v", m.Code, err)
		}
		output := e.redact(stdout + stderr)
		if code != 0 {
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: command exited %d: %s", m.Code, code, lastLine(output))
		}
		v, err := lastNumber(stdout)
		if err != nil {
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: %v", m.Code, err)
		}
		return v, domain.ProvenanceRef{Kind: "metric_command", ID: m.Code, Digest: e.storeBlob(output), Trust: domain.TrustExternalUntrusted}, nil

	case domain.MetricSourceURL:
		if !snap.Outcomes.AllowURLSources {
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: url sources are off; set outcomes.allow_url_sources: true in .idea-harness/harness.yaml to fetch them", m.Code)
		}
		var f jsonFetcher = e.metricFetcher
		if f == nil {
			f = tools.NewFetcher()
		}
		body, err := f.FetchJSON(ctx, m.Source.URL)
		if err != nil {
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: fetch: %v", m.Code, err)
		}
		v, err := jsonPointerNumber(body, m.Source.Pointer)
		if err != nil {
			return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: %v", m.Code, err)
		}
		return v, domain.ProvenanceRef{Kind: "metric_url", ID: m.Source.URL, Digest: e.storeBlob(e.redact(string(body))), Trust: domain.TrustExternalUntrusted}, nil
	}
	return 0, domain.ProvenanceRef{}, fmt.Errorf("%s: source %q cannot be measured", m.Code, m.Source.Kind)
}

// lastNumber returns the last number in s.
// metricWorkDir creates a metric command's scratch work root,
// <work root>/metric-*, or in the system temp directory when the engine has
// no work root.
func (e *Engine) metricWorkDir() (string, error) {
	if e.workRoot != "" {
		if err := os.MkdirAll(e.workRoot, 0o755); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(e.workRoot, "metric-*")
}

func lastNumber(s string) (float64, error) {
	all := numberRe.FindAllString(s, -1)
	if len(all) == 0 {
		return 0, fmt.Errorf("the command printed no number")
	}
	return strconv.ParseFloat(all[len(all)-1], 64)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return domain.TrimToWordBoundary(s, 200)
}

// jsonPointerNumber resolves an RFC 6901 JSON pointer in doc and returns the
// number there. A numeric string ("42") is accepted; anything else is not.
func jsonPointerNumber(doc []byte, pointer string) (float64, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, fmt.Errorf("the response is not JSON")
	}
	if pointer != "" {
		if !strings.HasPrefix(pointer, "/") {
			return 0, fmt.Errorf("pointer %q must start with /", pointer)
		}
		for _, raw := range strings.Split(pointer[1:], "/") {
			tok := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
			switch cur := v.(type) {
			case map[string]any:
				next, ok := cur[tok]
				if !ok {
					return 0, fmt.Errorf("pointer %s: no key %q", pointer, tok)
				}
				v = next
			case []any:
				i, err := strconv.Atoi(tok)
				if err != nil || i < 0 || i >= len(cur) {
					return 0, fmt.Errorf("pointer %s: no index %q", pointer, tok)
				}
				v = cur[i]
			default:
				return 0, fmt.Errorf("pointer %s: %q is not inside an object or array", pointer, tok)
			}
		}
	}
	if f, ok := num(v); ok {
		return f, nil
	}
	return 0, fmt.Errorf("the value at %q is not a number", pointer)
}

// RecordMetric records a human-supplied value for a metric (`idea metric
// record`, the MCP record_metric tool): the store computes attribution and
// drift. observedAt zero means now.
func (e *Engine) RecordMetric(ctx context.Context, initiativeID, ref string, value float64, note, actor string, observedAt time.Time) (*domain.MetricObservation, domain.MetricStatus, error) {
	m, err := e.db.GetMetricByRef(ctx, initiativeID, ref, time.Now())
	if err != nil {
		return nil, domain.MetricStatus{}, err
	}
	if actor == "" {
		actor = store.HumanActor
	}
	obs := &domain.MetricObservation{
		InitiativeID: initiativeID, MetricID: m.ID, Value: value, ObservedAt: observedAt,
		Source: domain.MetricSourceManual, Note: e.redact(strings.TrimSpace(note)),
		Provenance: []domain.ProvenanceRef{{Kind: "actor", ID: actor, Trust: domain.TrustHumanApproved}},
	}
	_, after, err := e.db.RecordObservation(ctx, obs, actor, time.Now())
	if err != nil {
		return nil, domain.MetricStatus{}, err
	}
	return obs, after, nil
}
