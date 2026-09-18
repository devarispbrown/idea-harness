package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestApproveCompletesDespiteACancelledRequestContext is E1.
//
// Approving a change set (verdict "approved") runs the adversarial reviewer
// inside the HTTP request. handleReviewChangeSet used to pass r.Context()
// straight through, so closing the browser tab -- which cancels it -- aborted
// the review mid-flight: "context canceled" in daemon.log, and the change set
// stuck in review_required with no record of why to anyone not tailing the
// log. context.WithoutCancel now detaches the review from the client's
// connection, so it runs to completion whether or not the client stays.
//
// The request's context is cancelled from a second goroutine shortly after
// the handler starts (the same bounded-sleep pattern this package's own
// async tests use, e.g. shutdown_test.go) -- standing in for the client
// disconnecting mid-request, the real trigger for the defect this closes.
// Cancelling before the handler starts at all would also abort the
// change-set lookup that precedes the review, which is a different read on
// r.Context() this fix does not touch and is not what was reported.
func TestApproveCompletesDespiteACancelledRequestContext(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _, csID := reviewRequiredChangeSet(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(rt)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review",
		strings.NewReader(`{"verdict":"approved"}`))
	req.Header.Set("X-Idea-Token", token)
	req.Header.Set("Content-Type", "application/json")
	// hostOriginGuard exact-matches a loopback Host; httptest.NewRequest's
	// default ("example.com") is not one.
	req.Host = "127.0.0.1"

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (a cancelled request context must not fail the review)", rec.Code, rec.Body.String())
	}
	cs, err := pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetApproved {
		t.Fatalf("change set is %q, want approved -- the review must complete even though "+
			"the request context was cancelled mid-flight", cs.Status)
	}
}
