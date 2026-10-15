// Package quality holds the deterministic analyses behind `idea verify`'s
// quality dimensions (domain.QualityDimension): an OpenAPI 3 diff, a Go
// exported-API diff, documented-command extraction, docs and operational-
// readiness heuristics, and performance-budget arithmetic. Everything here is
// a pure function of file contents -- no store, no Docker, no model -- so each
// rule is unit-testable against a fixture, and the workflow package owns the
// parts that run commands or ask the verification_engineer role.
package quality

import (
	"fmt"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Rollup derives a dimension's status from its checks, worst first:
// fail > not_measured > warn > pass > not_applicable.
//
// not_measured outranks warn and pass deliberately. A dimension that passed
// what it could see and could not look at the rest has not passed; reporting
// it as pass is exactly the "unmeasured shown as green" failure this exists
// to prevent. A dimension whose every check is not_applicable is
// not_applicable; with no checks at all it is not_measured.
func Rollup(checks []domain.QualityCheck) domain.QualityStatus {
	if len(checks) == 0 {
		return domain.QualityNotMeasured
	}
	rank := map[domain.QualityStatus]int{
		domain.QualityNotApplicable: 0,
		domain.QualityPass:          1,
		domain.QualityWarn:          2,
		domain.QualityNotMeasured:   3,
		domain.QualityFail:          4,
	}
	worst := domain.QualityNotApplicable
	for _, c := range checks {
		if rank[c.Status] > rank[worst] {
			worst = c.Status
		}
	}
	return worst
}

// Summarize renders a one-line summary of a dimension's checks: the status
// counts, then the first failing (or unmeasured, or warning) check's detail.
func Summarize(checks []domain.QualityCheck) string {
	if len(checks) == 0 {
		return "nothing was measured"
	}
	counts := map[domain.QualityStatus]int{}
	for _, c := range checks {
		counts[c.Status]++
	}
	var parts []string
	for _, s := range []domain.QualityStatus{domain.QualityFail, domain.QualityNotMeasured, domain.QualityWarn, domain.QualityPass, domain.QualityNotApplicable} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ReplaceAll(string(s), "_", " ")))
		}
	}
	out := strings.Join(parts, ", ")
	for _, s := range []domain.QualityStatus{domain.QualityFail, domain.QualityNotMeasured, domain.QualityWarn} {
		for _, c := range checks {
			if c.Status == s && c.Detail != "" {
				first, _, _ := strings.Cut(c.Detail, "\n")
				return out + " — " + c.Name + ": " + first
			}
		}
	}
	return out
}

// DefaultModes is each dimension's mode at an effective review rigor, before
// verify.dimensions overrides it.
//
//   - quick: every dimension off. A small change to a small project is
//     verified by its tests and acceptance evidence, as before this existed.
//   - standard: setup_reproducibility, docs_completeness and api_compatibility
//     advisory -- the three that are cheap, deterministic and almost never
//     wrong about what they report -- and the rest off.
//   - thorough: those three gated; api_ergonomics, security, performance and
//     operational_readiness advisory.
//
// An unrecognized rigor gets standard's modes, the same "safe middle"
// fallback findingCap uses.
func DefaultModes(rigor string) map[domain.QualityDimension]domain.QualityMode {
	out := make(map[domain.QualityDimension]domain.QualityMode, len(domain.QualityDimensions))
	for _, d := range domain.QualityDimensions {
		out[d] = domain.QualityModeOff
	}
	core := []domain.QualityDimension{domain.QualitySetupReproducibility, domain.QualityDocsCompleteness, domain.QualityAPICompatibility}
	switch rigor {
	case "quick":
	case "thorough":
		for _, d := range domain.QualityDimensions {
			out[d] = domain.QualityModeAdvisory
		}
		for _, d := range core {
			out[d] = domain.QualityModeGate
		}
	default:
		for _, d := range core {
			out[d] = domain.QualityModeAdvisory
		}
	}
	return out
}

// Policy is the resolved mode of every dimension and where it came from.
type Policy struct {
	Rigor string                                        `json:"rigor"`
	Modes map[domain.QualityDimension]domain.QualityMode `json:"modes"`
	// Overridden lists the dimensions whose mode verify.dimensions set
	// explicitly (the rest are the rigor default).
	Overridden []domain.QualityDimension `json:"overridden,omitempty"`
}

// ResolvePolicy applies verify.dimensions overrides to the rigor defaults.
// An override naming an unknown dimension or mode is ignored here -- the
// config schema refuses both, so reaching this with one means the schema and
// the domain disagree, and the default is the safer reading.
func ResolvePolicy(rigor string, overrides map[string]string) Policy {
	p := Policy{Rigor: rigor, Modes: DefaultModes(rigor)}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d, m := domain.QualityDimension(k), domain.QualityMode(overrides[k])
		if !d.Valid() || !m.Valid() {
			continue
		}
		p.Modes[d] = m
		p.Overridden = append(p.Overridden, d)
	}
	return p
}

// Enabled reports whether any dimension is measured at all.
func (p Policy) Enabled() bool {
	for _, m := range p.Modes {
		if m != domain.QualityModeOff {
			return true
		}
	}
	return false
}

// GateUnmet lists, for every gated dimension, why it does not satisfy the
// gate: its latest result failed, was not measured, or is missing or stale
// (measured against different applied work or a different policy -- which
// is not measured either). results holds the latest result per dimension;
// a result whose InputDigest differs from current is treated as stale.
func GateUnmet(p Policy, results map[domain.QualityDimension]domain.QualityResult, current string) []string {
	var out []string
	for _, d := range domain.QualityDimensions {
		if p.Modes[d] != domain.QualityModeGate {
			continue
		}
		r, ok := results[d]
		switch {
		case !ok:
			out = append(out, string(d)+" not measured")
		case r.InputDigest != current:
			out = append(out, string(d)+" not measured since the last change (stale)")
		case !r.Status.SatisfiesGate():
			out = append(out, string(d)+" "+strings.ReplaceAll(string(r.Status), "_", " "))
		}
	}
	return out
}
