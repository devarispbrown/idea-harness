package model

import (
	"context"
	"errors"
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

// TestModelNotFoundErrorNamesTheModelAndHowToFixIt pins the ICP walkthrough
// finding: a retired/unknown model id 404s at call time (a stale catalog
// entry, or a config left pointing at a model the provider withdrew), and the
// message must name the model and how to change it -- not just "status 404".
func TestModelNotFoundErrorNamesTheModelAndHowToFixIt(t *testing.T) {
	const upstream = `{"error":{"message":"The model ` + "`gemini-2.5-flash`" + ` is not available to your project"}}`

	for _, c := range []struct {
		name string
		call func(baseURL string) error
	}{
		{"openai", func(u string) error {
			_, err := NewOpenAI("k", u).Complete(context.Background(), Request{Model: "gemini-2.5-flash", Messages: []Message{{Role: "user", Content: "hi"}}, MaxOutput: 100})
			return err
		}},
		{"anthropic", func(u string) error {
			_, err := NewAnthropic("k", u).Complete(context.Background(), Request{Model: "gemini-2.5-flash", Messages: []Message{{Role: "user", Content: "hi"}}, MaxOutput: 100})
			return err
		}},
		{"gemini", func(u string) error {
			_, err := NewGemini("k", u).Complete(context.Background(), Request{Model: "gemini-2.5-flash", Messages: []Message{{Role: "user", Content: "hi"}}, MaxOutput: 100})
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(upstream))
			}))
			defer ts.Close()

			err := c.call(ts.URL)
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "gemini-2.5-flash") {
				t.Errorf("error does not name the model that 404'd: %v", err)
			}
			if !strings.Contains(msg, "404") {
				t.Errorf("error does not carry the status: %v", err)
			}
			if !strings.Contains(msg, "idea model set") {
				t.Errorf("error does not say how to change the model: %v", err)
			}
			if !strings.Contains(msg, "not available to your project") {
				t.Errorf("error discards the upstream body: %v", err)
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

// TestTruncationIsDetected pins the safety net under the stage-budget cap.
//
// Capping a request to the provider's ceiling turns a guaranteed 400 into a
// request that succeeds — but a structured reply cut mid-object is not caught
// downstream: repairJSON closes unterminated containers and the review schema
// has no minItems, so a findings list truncated from 15 to 1 validates clean.
// A review flagged that the cap, shipped alone, traded a loud failure for a
// quietly diminished artifact. Each provider now reports it from its own finish
// reason.
func TestTruncationIsDetected(t *testing.T) {
	for _, c := range []struct {
		name, body string
		call       func(u string) error
	}{
		{
			name: "openai finish_reason=length",
			body: `{"choices":[{"message":{"content":"{\"findings\":[]"},"finish_reason":"length"}],"usage":{"completion_tokens":8192}}`,
			call: func(u string) error { _, err := NewOpenAI("k", u).Complete(context.Background(), req()); return err },
		},
		{
			name: "anthropic stop_reason=max_tokens",
			body: `{"content":[{"type":"text","text":"{\"findings\":["}],"stop_reason":"max_tokens","usage":{"output_tokens":8192}}`,
			call: func(u string) error { _, err := NewAnthropic("k", u).Complete(context.Background(), req()); return err },
		},
		{
			name: "gemini finishReason=MAX_TOKENS",
			body: `{"candidates":[{"content":{"parts":[{"text":"{\"findings\":["}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"candidatesTokenCount":8192}}`,
			call: func(u string) error { _, err := NewGemini("k", u).Complete(context.Background(), req()); return err },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()

			err := c.call(ts.URL)
			if err == nil {
				t.Fatal("a truncated reply was accepted as a successful one")
			}
			if !errors.Is(err, ErrTruncated) {
				t.Fatalf("error is not ErrTruncated: %v", err)
			}
			if !strings.Contains(err.Error(), "100") {
				t.Errorf("the message should name the budget that was hit: %v", err)
			}
		})
	}
}

// TestAnUntruncatedReplyIsNotFlagged is the other direction: the ordinary stop
// reason must pass through.
func TestAnUntruncatedReplyIsNotFlagged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],"usage":{"completion_tokens":2}}`))
	}))
	defer ts.Close()
	if _, err := NewOpenAI("k", ts.URL).Complete(context.Background(), req()); err != nil {
		t.Fatalf("a complete reply was rejected: %v", err)
	}
}

// TestTruncatedErrorNamesOnlyReachableActions is the "the error must never
// recommend an action that cannot be taken" requirement (round-6 B1).
// model.truncatedError used to say "raise the stage budget or use a model
// with a larger output ceiling" -- and the first half was a dead end: no
// config field in this codebase raised the number this package actually
// sent (MaxOutput was a hardcoded provider constant, and harness.yaml's
// review.max_tokens, the one field a human would try, was read nowhere near
// here). This package has no idea whether a caller is even running inside
// the workflow engine, so it must not name a workflow-level remediation
// (review.max_tokens, harness.yaml) it cannot verify applies or exists --
// that is workflow.wrapTruncationGuidance's job, one layer up, where it IS
// reachable. This test pins truncatedError to naming only what is true and
// actionable at ITS layer: which model, which provider, and the exact
// budget that was hit.
func TestTruncatedErrorNamesOnlyReachableActions(t *testing.T) {
	err := truncatedError("anthropic", "claude-sonnet-5", 16384)
	msg := err.Error()

	for _, unreachable := range []string{"review.max_tokens", "harness.yaml", "raise the stage budget"} {
		if strings.Contains(msg, unreachable) {
			t.Errorf("truncatedError names %q, which this package cannot verify is reachable "+
				"(that is workflow.wrapTruncationGuidance's job, one layer up): %q", unreachable, msg)
		}
	}
	for _, fact := range []string{"anthropic", "claude-sonnet-5", "16384"} {
		if !strings.Contains(msg, fact) {
			t.Errorf("truncatedError does not name %q, a fact this layer does know: %q", fact, msg)
		}
	}
}
