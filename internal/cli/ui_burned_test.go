package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestBurnedCodesAreReported: `idea ui pair` and `idea ui sessions` tell the
// owner that codes burned, and say nothing when none did.
func TestBurnedCodesAreReported(t *testing.T) {
	var out struct {
		Code string `json:"code"`
		burnedCodes
	}
	if err := json.Unmarshal([]byte(`{"code":"X","burned_codes":2,"last_burned_at":"2026-10-15T12:00:00Z"}`), &out); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	renderBurnedCodes(&buf, out.burnedCodes)
	if got := buf.String(); !strings.Contains(got, "2 pairing code(s) burned") || !strings.Contains(got, "guessing") {
		t.Fatalf("burn warning: %q", got)
	}
	buf.Reset()
	renderBurnedCodes(&buf, burnedCodes{})
	if buf.Len() != 0 {
		t.Fatalf("no burns, yet printed %q", buf.String())
	}
	b, _ := json.Marshal(struct {
		Code string `json:"code"`
		burnedCodes
	}{Code: "X"})
	if strings.Contains(string(b), "burned") {
		t.Fatalf("--json grew burn fields with nothing burned: %s", b)
	}
}
