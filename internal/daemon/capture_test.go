package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCaptureCreatesADraftAndStartRunsIt: mint a token, capture a brief (no API
// token, just the capture token in the body), confirm the draft, refuse a wrong
// token, then Start the draft.
func TestCaptureCreatesADraftAndStartRunsIt(t *testing.T) {
	_, ts, token, projectID := newIntegrationServer(t, "", "", nil)

	// Mint.
	resp := doReq(t, "POST", ts.URL+"/api/v1/projects/"+projectID+"/capture-tokens", token, "", `{"label":"telegram"}`)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("mint: %d %s", resp.StatusCode, b)
	}
	var minted struct{ Token string `json:"token"` }
	json.NewDecoder(resp.Body).Decode(&minted)
	resp.Body.Close()
	if minted.Token == "" {
		t.Fatal("mint returned no token")
	}

	// Capture — the unauthenticated route, token in the body.
	capReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/capture",
		strings.NewReader(`{"token":"`+minted.Token+`","brief":"A recipe sharing app for home cooks"}`))
	capReq.Header.Set("Content-Type", "application/json")
	cresp, err := http.DefaultClient.Do(capReq)
	if err != nil {
		t.Fatal(err)
	}
	if cresp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(cresp.Body)
		cresp.Body.Close()
		t.Fatalf("capture: %d %s", cresp.StatusCode, b)
	}
	var capOut struct{ InitiativeID string `json:"initiative_id"` }
	json.NewDecoder(cresp.Body).Decode(&capOut)
	cresp.Body.Close()
	if capOut.InitiativeID == "" {
		t.Fatal("capture returned no initiative id")
	}

	// The draft is listed with status draft.
	gresp := doReq(t, "GET", ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+capOut.InitiativeID, token, "", "")
	var got struct {
		Status string `json:"status"`
		Title  string `json:"title"`
	}
	json.NewDecoder(gresp.Body).Decode(&got)
	gresp.Body.Close()
	if got.Status != "draft" {
		t.Fatalf("draft status = %q, want draft", got.Status)
	}
	if got.Title != "A recipe sharing app for home cooks" {
		t.Fatalf("draft title = %q", got.Title)
	}

	// A wrong token is refused with one 401.
	wreq, _ := http.NewRequest("POST", ts.URL+"/api/v1/capture", strings.NewReader(`{"token":"nope","brief":"x"}`))
	wreq.Header.Set("Content-Type", "application/json")
	wresp, _ := http.DefaultClient.Do(wreq)
	if wresp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(wresp.Body)
		wresp.Body.Close()
		t.Fatalf("wrong token: %d %s", wresp.StatusCode, b)
	}
	wresp.Body.Close()

	// Start the draft.
	sresp := doReq(t, "POST", ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+capOut.InitiativeID+"/start", token, "", "")
	if sresp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(sresp.Body)
		sresp.Body.Close()
		t.Fatalf("start: %d %s", sresp.StatusCode, b)
	}
	sresp.Body.Close()
}
