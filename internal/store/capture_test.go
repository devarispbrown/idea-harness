package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCaptureToken(t *testing.T) {
	db, err := OpenGlobal(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	tokID, err := db.CreateCaptureToken(ctx, "p1", "hash1", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := db.LookupCaptureToken(ctx, "hash1")
	if err != nil || projectID != "p1" {
		t.Fatalf("lookup = %q, %v; want p1", projectID, err)
	}
	if _, err := db.LookupCaptureToken(ctx, "hash-unknown"); err != ErrCaptureTokenUnknown {
		t.Fatalf("unknown token: %v, want ErrCaptureTokenUnknown", err)
	}
	tokens, err := db.ListCaptureTokens(ctx, "p1")
	if err != nil || len(tokens) != 1 || tokens[0].ID != tokID || tokens[0].Label != "telegram" {
		t.Fatalf("list = %+v, %v", tokens, err)
	}
	// A second token for another project is not listed under p1.
	if _, err := db.CreateCaptureToken(ctx, "p2", "hash2", "slack"); err != nil {
		t.Fatal(err)
	}
	if tokens, _ = db.ListCaptureTokens(ctx, "p1"); len(tokens) != 1 {
		t.Fatalf("p1 tokens = %d, want 1", len(tokens))
	}
}
