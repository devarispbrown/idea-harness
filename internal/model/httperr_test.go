package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProviderErrorsCarryTheResponseBody pins audit 5's B2.
//
// `openai: status 400` was the message for every OpenAI failure, and the body
// it discarded named the cause exactly. That is why the 32000-token defect --
// present since the initial commit, shipped in v0.1.0 -- was undiagnosable from
// inside the product through four audits.
func req() Request {
	return Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, MaxOutput: 100}
}

func TestProviderErrorsCarryTheResponseBody(t *testing.T) {
	const upstream = `{"error":{"message":"max_tokens is too large: 32000. This model supports at most 16384 completion tokens."}}`

	for _, c := range []struct {
		name string
		call func(baseURL string) error
	}{
		{"openai", func(u string) error {
			_, err := NewOpenAI("k", u).Complete(context.Background(), req())
			return err
		}},
		{"anthropic", func(u string) error {
			_, err := NewAnthropic("k", u).Complete(context.Background(), req())
			return err
		}},
		{"gemini", func(u string) error {
			_, err := NewGemini("k", u).Complete(context.Background(), req())
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(upstream))
			}))
			defer ts.Close()

			err := c.call(ts.URL)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "400") {
				t.Errorf("error does not carry the status: %v", err)
			}
			if !strings.Contains(err.Error(), "max_tokens is too large") {
				t.Errorf("error discards the body that names the cause: %v", err)
			}
		})
	}
}

// TestProviderErrorBodyIsScrubbed: a provider that echoes the request can echo
// the credential with it. An error travels into logs, Findings and stored
// artifacts, so a key must not travel with it.
func TestProviderErrorBodyIsScrubbed(t *testing.T) {
	body := `{"error":"bad auth for sk-ant-api03-AbCdEfGh1234567890XyZwVuTsRqPoNmLkJiHgFeDcBa9876543210"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	_, err := NewOpenAI("k", ts.URL).Complete(context.Background(), req())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "AbCdEfGh1234567890") {
		t.Fatalf("the credential travelled with the error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected the secret to be replaced, got: %v", err)
	}
}

// TestErrorBodyIsBounded: a provider answering with an HTML page or a stream
// must not put unbounded content into a log line or an artifact.
func TestErrorBodyIsBounded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 100_000)))
	}))
	defer ts.Close()

	_, err := NewOpenAI("k", ts.URL).Complete(context.Background(), req())
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > maxErrBody+128 {
		t.Fatalf("error is %d bytes; the body is not bounded", len(err.Error()))
	}
}

// TestScrubLeavesProseAlone is the other direction: the whole value of the fix
// is the prose in the body, so the scrub must not eat it.
func TestScrubLeavesProseAlone(t *testing.T) {
	// Long, secret-shaped-but-not-secret tokens included deliberately. A first
	// version of this test used only short words, so it passed while
	// `claude-3-7-sonnet-latest` -- 24 characters of letters, digits and
	// hyphens, and the single most useful token in a provider error -- was
	// being redacted.
	for _, in := range []string{
		"max_tokens is too large: 32000. This model supports at most 16384 completion tokens.",
		"The model claude-3-7-sonnet-latest does not exist or you do not have access to it.",
		"model gpt-4o-mini-2024-07-18 is not available; try models/gemini-2.5-flash instead",
		"invalid_request_error: unsupported_parameter",
	} {
		if got := scrubForLog(in); got != in {
			t.Errorf("scrub rewrote ordinary prose:\n  in:  %s\n  out: %s", in, got)
		}
	}
}

// TestScrubStillCatchesSecrets is the other half: loosening the rule above must
// not let a credential through.
func TestScrubStillCatchesSecrets(t *testing.T) {
	for _, secret := range []string{
		"sk-ant-api03-AbCdEfGh1234567890XyZwVuTsRqPoNmLkJiHgFeDcBa98765",
		"sk-proj-AbCdEfGh1234567890XyZwVuTsRqPoNmLkJi",
		"AbCdEfGh1234567890XyZwVuTsRqPoNmLkJi",
		"ghp_AbCdEfGh1234567890XyZwVuTsRqPoNmLkJi",
	} {
		got := scrubForLog("upstream said " + secret + " was rejected")
		if strings.Contains(got, secret) {
			t.Errorf("credential survived the scrub: %s", got)
		}
	}
}
