// Package risk computes a deterministic, tier-floored risk profile and a thin
// threat-model summary for an initiative's changes (§31). It contains no LLM
// logic: the tier floor, the marker scan, and the summary are all deterministic
// functions of the diff text so the same inputs always produce the same outputs.
package risk

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// Signal names produced by the deterministic marker scan. These names are the
// canonical categories consumed by ComputeRiskProfile's tier floor.
const (
	SignalAuth       = "auth"
	SignalSecret     = "secret"
	SignalNetwork    = "network"
	SignalCredential = "credential"
	SignalPayment    = "payment"
	SignalPII        = "pii"
)

// markerRule maps one diff marker to the risk signal it indicates. The catalog
// order is the deterministic emission order for the returned signals.
type markerRule struct {
	marker string
	signal string
}

// markerCatalog is the fixed, case-insensitive marker scan catalog. Each marker
// is matched as a whole word (bounded by non-word characters), so short markers
// like "auth"/"token"/"secret" do not false-positive on "author"/"tokenize"/
// "secretary". The scan is fully deterministic.
var markerCatalog = []markerRule{
	{"api_key", SignalCredential},
	{"password", SignalCredential},
	{"token", SignalCredential},
	{"secret", SignalSecret},
	{"auth", SignalAuth},
	{"https://", SignalNetwork},
	{"http://", SignalNetwork},
	{"payment", SignalPayment},
	{"credit card", SignalPayment},
	{"ssn", SignalPII},
}

// containsWord reports whether word appears in s bounded by non-word characters
// (or string boundaries), matching "token" but not "tokenize". A marker whose
// own first/last byte is a non-word character (e.g. "http://") carries its
// boundary with it, so it still matches when immediately followed by a host.
func containsWord(s, word string) bool {
	if word == "" {
		return false
	}
	// If the marker begins/ends with punctuation it is already delimited on
	// that side, independent of the surrounding text.
	looseBefore := !isWordChar(word[0])
	looseAfter := !isWordChar(word[len(word)-1])
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		before := i == 0 || !isWordChar(s[i-1]) || looseBefore
		after := i+len(word) == len(s) || !isWordChar(s[i+len(word)]) || looseAfter
		if before && after {
			return true
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

// RiskSignals deterministically scans a diff/patch string for known risk
// markers and returns one domain.RiskSignal per detected category, each with
// Detected=true. Signals are emitted in markerCatalog order and are deduplicated
// by category; Detail records every marker that contributed to a category.
//
// ctx and patchDigest are reserved for future policy hooks (e.g. per-initiative
// sensitivity or provenance linking); the scan itself keys only on diffText.
func RiskSignals(ctx context.Context, patchDigest, diffText string) []domain.RiskSignal {
	_ = ctx         // reserved
	_ = patchDigest // reserved
	lower := strings.ToLower(diffText)

	matched := map[string][]string{}
	var order []string
	for _, rule := range markerCatalog {
		if containsWord(lower, rule.marker) {
			if _, ok := matched[rule.signal]; !ok {
				order = append(order, rule.signal)
			}
			matched[rule.signal] = append(matched[rule.signal], rule.marker)
		}
	}

	signals := make([]domain.RiskSignal, 0, len(order))
	for _, name := range order {
		signals = append(signals, domain.RiskSignal{
			Name:     name,
			Detected: true,
			Detail:   "matched markers: " + strings.Join(matched[name], ", "),
		})
	}
	return signals
}

// ComputeRiskProfile builds a RiskProfile whose tier is the deterministic floor
// implied by the detected signals: any detected auth, secret, network, or
// credential signal floors the profile at high; any detected payment or pii
// signal floors it at medium; otherwise the floor is low. The returned tier is
// a minimum, never a ceiling: callers may raise it (see Raise) but the floor
// computed here can never be lowered by downstream logic.
func ComputeRiskProfile(initiativeID string, signals []domain.RiskSignal) domain.RiskProfile {
	tier := floorTier(signals)

	p := domain.RiskProfile{
		ID:           id.New(),
		InitiativeID: initiativeID,
		Tier:         tier,
		// append to an empty slice, not to nil: an initiative with no detected
		// signals is the normal case, and `"signals":null` made every client
		// null-check a field the spec types as an array.
		Signals:   append([]domain.RiskSignal{}, signals...),
		CreatedAt: time.Now().UTC(),
	}
	for _, s := range signals {
		if s.Detected {
			p.Reason = append(p.Reason, "detected risk signal: "+s.Name)
		}
	}
	p.Reason = append(p.Reason, "deterministic tier floor: "+string(tier))
	return p
}

// floorTier returns the minimum tier implied by the detected signals.
func floorTier(signals []domain.RiskSignal) domain.RiskTier {
	floor := domain.RiskLow
	for _, s := range signals {
		if !s.Detected {
			continue
		}
		switch s.Name {
		case SignalAuth, SignalSecret, SignalNetwork, SignalCredential:
			floor = Raise(floor, domain.RiskHigh)
		case SignalPayment, SignalPII:
			floor = Raise(floor, domain.RiskMedium)
		}
	}
	return floor
}

// Raise returns the higher of two tiers. It is the caller-facing escalation
// hook: a profile's deterministic floor can be raised (e.g. to
// domain.RiskCritical) but never lowered, so callers apply it as
//
//	p.Tier = risk.Raise(p.Tier, domain.RiskCritical)
func Raise(a, b domain.RiskTier) domain.RiskTier {
	if tierRank(b) > tierRank(a) {
		return b
	}
	return a
}

// tierRank orders tiers from least to most severe. Unknown tiers rank lowest.
func tierRank(t domain.RiskTier) int {
	switch t {
	case domain.RiskCritical:
		return 4
	case domain.RiskHigh:
		return 3
	case domain.RiskMedium:
		return 2
	case domain.RiskLow:
		return 1
	default:
		return 0
	}
}

// ThreatModelSummary returns a deterministic one-paragraph threat-model summary
// naming the detected risk categories and a generic mitigation. It performs no
// LLM call and no ranking beyond the signal categories themselves.
func ThreatModelSummary(signals []domain.RiskSignal) string {
	names := detectedNames(signals)
	if len(names) == 0 {
		return "Threat model: no elevated risk signals detected. Maintain baseline hygiene: least-privilege access, secret management, and human review before applying changes."
	}
	mitigation := "Mitigation: enforce least-privilege access, avoid committing secrets, encrypt sensitive data in transit and at rest, and require human review before applying changes."
	return "Threat model: detected risk categories: " + strings.Join(names, ", ") + ". " + mitigation
}

// detectedNames returns the sorted, de-duplicated names of detected signals so
// the summary is deterministic regardless of input slice order.
func detectedNames(signals []domain.RiskSignal) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(signals))
	for _, s := range signals {
		if s.Detected && s.Name != "" && !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}
