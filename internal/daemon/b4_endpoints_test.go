package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestAcceptRecommendationsResolvesEveryOpenDecision proves review B4's
// "accept-recommendations" endpoint resolves every open decision with its own
// recommendation, actor human, and leaves a decision with no recommendation
// open and named in `skipped`.
func TestAcceptRecommendationsResolvesEveryOpenDecision(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	withRec := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which DNS provider?",
		Options: []string{"eNom", "Route53"}, Recommendation: "eNom", Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), withRec); err != nil {
		t.Fatal(err)
	}
	noRec := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which region?",
		Options: []string{"us", "eu"}, Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), noRec); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/decisions/accept-recommendations"
	resp := doReq(t, http.MethodPost, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept-recommendations: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body struct {
		Resolved []string          `json:"resolved"`
		Skipped  []skippedDecision `json:"skipped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Resolved) != 1 || body.Resolved[0] != withRec.Code {
		t.Fatalf("resolved = %v, want [%s]", body.Resolved, withRec.Code)
	}
	if len(body.Skipped) != 1 || body.Skipped[0].Code != noRec.Code {
		t.Fatalf("skipped = %v, want [%s]", body.Skipped, noRec.Code)
	}
	if body.Skipped[0].Reason == "" {
		t.Error("expected a non-empty skip reason for the no-recommendation decision")
	}

	got, err := pr.db.GetDecision(t.Context(), withRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.DecisionDecided || got.Resolution != "eNom" || got.ResolvedBy != "human" {
		t.Fatalf("decision not resolved with its recommendation: %+v", got)
	}
	stillOpen, err := pr.db.GetDecision(t.Context(), noRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOpen.Status != domain.DecisionOpen {
		t.Fatalf("no-recommendation decision should stay open, got %q", stillOpen.Status)
	}
}

// TestAcceptRecommendationsSkipsRecommendationNotInOptions is review M3: the
// single-resolve path (offers()) refuses to record an answer that is not
// one of a decision's options, and accept-recommendations must not bypass
// that guard just because it runs without a human clicking each one. A
// recommendation matching no option (even case/whitespace-insensitively) is
// skipped, not recorded verbatim.
func TestAcceptRecommendationsSkipsRecommendationNotInOptions(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	bogus := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which DNS provider?",
		Options: []string{"eNom", "Route53"}, Recommendation: "GoDaddy", Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), bogus); err != nil {
		t.Fatal(err)
	}
	// A recommendation that matches an option case/whitespace-insensitively
	// IS resolved, using the option's own exact text.
	caseDrift := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which region?",
		Options: []string{"us-east-1", "eu-west-1"}, Recommendation: "  US-EAST-1  ", Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), caseDrift); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/decisions/accept-recommendations"
	resp := doReq(t, http.MethodPost, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept-recommendations: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body struct {
		Resolved []string          `json:"resolved"`
		Skipped  []skippedDecision `json:"skipped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Skipped) != 1 || body.Skipped[0].Code != bogus.Code {
		t.Fatalf("skipped = %v, want [%s]", body.Skipped, bogus.Code)
	}
	if body.Skipped[0].Reason == "" {
		t.Error("expected a non-empty skip reason for the unmatched recommendation")
	}
	if len(body.Resolved) != 1 || body.Resolved[0] != caseDrift.Code {
		t.Fatalf("resolved = %v, want [%s]", body.Resolved, caseDrift.Code)
	}

	stillOpen, err := pr.db.GetDecision(t.Context(), bogus.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOpen.Status != domain.DecisionOpen {
		t.Fatalf("a recommendation not among the options must not be recorded: %+v", stillOpen)
	}
	matched, err := pr.db.GetDecision(t.Context(), caseDrift.ID)
	if err != nil {
		t.Fatal(err)
	}
	if matched.Status != domain.DecisionDecided || matched.Resolution != "us-east-1" {
		t.Fatalf("expected resolution to be the option's own exact text %q, got %+v", "us-east-1", matched)
	}
}

// TestAcceptOneRecommendationTreatsConcurrentlyResolvedAsSkipped is review
// M3: a decision a CONCURRENT single-resolve call answers between
// handleAcceptRecommendations's own ListDecisions snapshot and its call to
// acceptOneRecommendation must be treated as skipped, not a hard failure
// that would abort every remaining decision in the batch. Exercised
// directly against acceptOneRecommendation with a deliberately STALE
// decision struct (Status: Open), since reproducing the actual race through
// the HTTP handler's own internal timing is not deterministic.
func TestAcceptOneRecommendationTreatsConcurrentlyResolvedAsSkipped(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	d := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which DNS provider?",
		Recommendation: "eNom", Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	stale := *d // the snapshot handleAcceptRecommendations would have taken

	// A concurrent single-resolve wins the race first.
	if err := pr.db.ResolveDecision(t.Context(), d.ID, "a human's own answer", store.HumanActor); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(rt)
	didResolve, _, err := srv.acceptOneRecommendation(t.Context(), pr, stale)
	if err != nil {
		t.Fatalf("expected the race to be treated as skipped, not an error: %v", err)
	}
	if didResolve {
		t.Fatal("expected didResolve = false: the decision was already resolved by someone else")
	}

	got, err := pr.db.GetDecision(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolution != "a human's own answer" {
		t.Fatalf("the concurrent resolve's own answer must survive untouched: %+v", got)
	}
}

// TestMatchRecommendationOptionTolerantForms is the ICP re-run defect #2
// follow-up: the model's recommendation prose almost never matched an option
// verbatim, so accept-recommendations silently skipped every decision that
// had options. The matcher now tolerates the shapes a model actually
// produces -- case/whitespace/punctuation drift, one text containing the
// other, and a positional reference ("Option B", a bare letter or number) --
// while still refusing anything genuinely ambiguous or unrelated.
func TestMatchRecommendationOptionTolerantForms(t *testing.T) {
	options := []string{"Use Postgres", "Use SQLite", "Use MySQL"}
	cases := []struct {
		name    string
		options []string
		rec     string
		want    string
		wantOK  bool
	}{
		{"exact", options, "Use Postgres", "Use Postgres", true},
		{"case and whitespace drift", options, "  use postgres  ", "Use Postgres", true},
		{"punctuation drift", options, "Use Postgres.", "Use Postgres", true},
		{"option keyword and letter", options, "Option A", "Use Postgres", true},
		{"option keyword, number, and trailing prose", options, "Option 2 - it's simplest for now", "Use SQLite", true},
		{"choice keyword lowercase", options, "choice c", "Use MySQL", true},
		{"bare letter", options, "B", "Use SQLite", true},
		{"bare number with punctuation", options, "(3)", "Use MySQL", true},
		{"option text contains extra framing", options, "I'd go with Use Postgres for this one", "Use Postgres", true},
		{"option is a prefix of the recommendation", []string{"Postgres"}, "Postgres, for its JSON support", "Postgres", true},
		{"word that merely starts with the keyword is not an index reference", options, "Optional support for MySQL later", "", false},
		{"unrelated prose", options, "GoDaddy", "", false},
		{"out of range index", options, "Option 9", "", false},
		{"empty recommendation", options, "", "", false},
		{"ambiguous after normalization", []string{"Use it", "use it!"}, "USE IT", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok, reason := matchRecommendationOption(c.options, c.rec)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (reason=%q, got=%q)", ok, c.wantOK, reason, got)
			}
			if ok && got != c.want {
				t.Fatalf("matched = %q, want %q", got, c.want)
			}
			if !ok && reason == "" {
				t.Error("expected a non-empty reason when ok is false")
			}
		})
	}
}

// TestTriageRanksBlockingFirstThenTopThree proves review B4's triage ranking:
// blocking beats a plain open decision/question, and only the top 3 are
// "priority" -- the rest "optional".
func TestTriageRanksBlockingFirstThenTopThree(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// Four open decisions -- more than the top-3 cutoff.
	for i := 0; i < 4; i++ {
		d := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct,
			Question: "Decision " + string(rune('A'+i)) + "?", Options: []string{"x", "y"}, Status: domain.DecisionOpen}
		if err := pr.db.CreateDecision(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	// One blocking finding, which must outrank every decision regardless of
	// creation order.
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, ReviewRunID: "r1", TargetType: "change_set", TargetID: "cs-1",
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh, Category: "secret_sweep",
		Finding: "a real credential was found", Recommendation: "rotate it",
	}); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/triage"
	resp := doReq(t, http.MethodGet, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("triage: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body struct {
		Items []struct {
			Type     string `json:"type"`
			Blocking bool   `json:"blocking"`
			Rank     string `json:"rank"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Items) != 5 {
		t.Fatalf("expected 5 items (1 finding + 4 decisions), got %d", len(body.Items))
	}
	if body.Items[0].Type != "finding" || !body.Items[0].Blocking || body.Items[0].Rank != "priority" {
		t.Fatalf("blocking finding must rank first: %+v", body.Items[0])
	}
	priority, optional := 0, 0
	for _, it := range body.Items {
		switch it.Rank {
		case "priority":
			priority++
		case "optional":
			optional++
		default:
			t.Fatalf("item has no rank: %+v", it)
		}
	}
	if priority != 3 || optional != 2 {
		t.Fatalf("priority=%d optional=%d, want 3 and 2", priority, optional)
	}
}
