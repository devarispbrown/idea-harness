package daemon

import (
	"net/http"
	"testing"
)

// TestRiskIsAReadAndStaysOne pins the P0 from the second DX audit: handleRisk
// called SaveRiskProfile, which is append-only, so every GET grew the database.
// Measured against the shipped v0.1.0 binary: three GETs added three
// risk_profiles rows and three events.
//
// The event half matters as much as the row half. SaveRiskProfile goes through
// db.Apply, so each read also appended a risk_profile.saved event -- which
// handleActivity returns and handleEvents streams over SSE. A read polluted the
// live event feed, not just a table nobody queried.
func TestRiskIsAReadAndStaysOne(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())
	riskURL := base + "/initiatives/" + iniID + "/risk"

	countActivity := func() int {
		t.Helper()
		resp := doReq(t, http.MethodGet, base+"/activity", token, "", "")
		var events []struct {
			EventType string `json:"event_type"`
		}
		decode(t, resp, &events)
		return len(events)
	}

	// One read first, so the comparison is between reads rather than between
	// "no risk profile computed yet" and "one computed".
	mustGetRisk(t, riskURL, token)
	before := countActivity()

	for i := 0; i < 3; i++ {
		mustGetRisk(t, riskURL, token)
	}

	if after := countActivity(); after != before {
		t.Errorf("three GETs on /risk appended %d event(s) to the activity feed; a read must not write", after-before)
	}
}

func mustGetRisk(t *testing.T, url, token string) {
	t.Helper()
	resp := doReq(t, http.MethodGet, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /risk = %d, want 200", resp.StatusCode)
	}
	var profile struct {
		Tier string `json:"tier"`
	}
	decode(t, resp, &profile)
	if profile.Tier == "" {
		t.Fatal("GET /risk returned no tier; the handler must still compute the profile")
	}
}
