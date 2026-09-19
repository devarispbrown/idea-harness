package risk

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func signal(name string, detected bool) domain.RiskSignal {
	return domain.RiskSignal{Name: name, Detected: detected}
}

func TestFloorTier(t *testing.T) {
	tests := []struct {
		name    string
		signals []domain.RiskSignal
		want    domain.RiskTier
	}{
		{"no signals is low", nil, domain.RiskLow},
		{"undetected signals stay low", []domain.RiskSignal{
			signal(SignalCredential, false),
			signal(SignalPayment, false),
		}, domain.RiskLow},
		{"payment floors medium", []domain.RiskSignal{signal(SignalPayment, true)}, domain.RiskMedium},
		{"pii floors medium", []domain.RiskSignal{signal(SignalPII, true)}, domain.RiskMedium},
		{"auth floors high", []domain.RiskSignal{signal(SignalAuth, true)}, domain.RiskHigh},
		{"secret floors high", []domain.RiskSignal{signal(SignalSecret, true)}, domain.RiskHigh},
		{"network floors high", []domain.RiskSignal{signal(SignalNetwork, true)}, domain.RiskHigh},
		{"credential floors high", []domain.RiskSignal{signal(SignalCredential, true)}, domain.RiskHigh},
		{"high signal dominates medium", []domain.RiskSignal{
			signal(SignalPayment, true), signal(SignalCredential, true),
		}, domain.RiskHigh},
		{"unknown signal name is ignored", []domain.RiskSignal{signal("other", true)}, domain.RiskLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := floorTier(tt.signals); got != tt.want {
				t.Fatalf("floorTier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeRiskProfile(t *testing.T) {
	p := ComputeRiskProfile("ini-1", []domain.RiskSignal{
		signal(SignalCredential, true),
		signal(SignalPayment, true),
		signal(SignalNetwork, false),
	})

	if p.ID == "" {
		t.Error("expected a non-empty profile ID")
	}
	if p.InitiativeID != "ini-1" {
		t.Errorf("InitiativeID = %q, want %q", p.InitiativeID, "ini-1")
	}
	if p.Tier != domain.RiskHigh {
		t.Errorf("Tier = %q, want %q", p.Tier, domain.RiskHigh)
	}
	if len(p.Signals) != 3 {
		t.Errorf("len(Signals) = %d, want 3", len(p.Signals))
	}
	if len(p.Reason) < 2 {
		t.Errorf("expected reasons for detected signals, got %v", p.Reason)
	}
	if p.CreatedAt.IsZero() {
		t.Error("expected a non-zero CreatedAt")
	}
}

func TestComputeRiskProfileCopiesSignals(t *testing.T) {
	in := []domain.RiskSignal{signal(SignalPayment, true)}
	p := ComputeRiskProfile("ini-1", in)
	in[0].Detected = false
	if !p.Signals[0].Detected {
		t.Error("profile must own a copy of the input signals")
	}
}

func TestRaise(t *testing.T) {
	tests := []struct {
		a, b domain.RiskTier
		want domain.RiskTier
	}{
		{domain.RiskLow, domain.RiskMedium, domain.RiskMedium},
		{domain.RiskMedium, domain.RiskLow, domain.RiskMedium},
		{domain.RiskHigh, domain.RiskMedium, domain.RiskHigh},
		{domain.RiskHigh, domain.RiskCritical, domain.RiskCritical},
		{domain.RiskCritical, domain.RiskLow, domain.RiskCritical},
		{domain.RiskMedium, domain.RiskMedium, domain.RiskMedium},
	}
	for _, tt := range tests {
		if got := Raise(tt.a, tt.b); got != tt.want {
			t.Errorf("Raise(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestRiskSignalsDetection(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		wantSig []string
	}{
		{"empty diff", "", nil},
		{"credential markers dedupe", "set password and token here", []string{SignalCredential}},
		{"secret", "the secret value", []string{SignalSecret}},
		{"auth", "add auth header", []string{SignalAuth}},
		{"multiple categories in catalog order", "auth then token",
			[]string{SignalCredential, SignalAuth}},
		// review low-priority item: the marker catalog was tuned down to
		// auth/password/secret/token -- api_key, http(s)://, payment, credit
		// card and ssn used to be markers too, and each was noise for what
		// this scan gates (the non-blocking sensitive_area advisory): a bare
		// URL, or ordinary product copy about billing/PII, said nothing about
		// whether the change actually handles anything sensitive. None of
		// these produce a signal any more.
		{"http network no longer a marker", "fetch http://example.com", nil},
		{"https network no longer a marker", "fetch https://example.com", nil},
		{"payment no longer a marker", "process payment", nil},
		{"credit card no longer a marker", "store credit card", nil},
		{"ssn no longer a marker", "ssn field", nil},
		{"api_key no longer a marker on its own", "set api_key here", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RiskSignals(context.Background(), "digest", tt.diff)
			var names []string
			for _, s := range got {
				if !s.Detected {
					t.Fatalf("returned signal %q has Detected=false", s.Name)
				}
				names = append(names, s.Name)
			}
			if !reflect.DeepEqual(names, tt.wantSig) {
				t.Fatalf("RiskSignals(%q) names = %v, want %v", tt.diff, names, tt.wantSig)
			}
		})
	}
}

func TestRiskSignalsCaseInsensitive(t *testing.T) {
	got := RiskSignals(context.Background(), "digest", "PASSWORD: sk-abc123 and a TOKEN")
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if len(names) != 1 || names[0] != SignalCredential {
		t.Fatalf("names = %v, want [credential]", names)
	}
}

func TestThreatModelSummary(t *testing.T) {
	tests := []struct {
		name    string
		signals []domain.RiskSignal
		wantSub string
	}{
		{"empty", nil, "no elevated risk signals detected"},
		{"single", []domain.RiskSignal{signal(SignalCredential, true)}, "credential"},
		{"sorted regardless of input order", []domain.RiskSignal{
			signal(SignalPayment, true), signal(SignalAuth, true),
		}, "auth, payment"},
		{"ignores undetected", []domain.RiskSignal{signal(SignalSecret, false)}, "no elevated risk signals detected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ThreatModelSummary(tt.signals)
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("ThreatModelSummary() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}

func TestThreatModelSummaryDeterministic(t *testing.T) {
	signals := []domain.RiskSignal{signal(SignalAuth, true), signal(SignalPII, true)}
	first := ThreatModelSummary(signals)
	for i := 0; i < 10; i++ {
		if got := ThreatModelSummary(signals); got != first {
			t.Fatalf("summary not deterministic: %q vs %q", got, first)
		}
	}
}
