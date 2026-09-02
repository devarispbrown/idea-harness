package store

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

func TestContextDocRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	doc := &domain.ContextDoc{
		InitiativeID: ini.ID,
		Kind:         "prd",
		Title:        "PRD draft",
		Digest:       "sha256:abc123",
		Preview:      "Usage-based billing for the platform",
	}
	if err := db.CreateContextDoc(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID == "" {
		t.Fatal("expected context doc id to be assigned")
	}
	if doc.CreatedAt.IsZero() {
		t.Fatal("expected created_at to be assigned")
	}

	docs, err := db.ListContextDocs(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 context doc, got %d", len(docs))
	}
	got := docs[0]
	if got.ID != doc.ID || got.InitiativeID != doc.InitiativeID || got.Kind != doc.Kind ||
		got.Title != doc.Title || got.Digest != doc.Digest || got.Preview != doc.Preview {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, *doc)
	}
	if got.CreatedAt.UnixMilli() != doc.CreatedAt.UnixMilli() {
		t.Fatalf("created_at mismatch: got %v want %v", got.CreatedAt, doc.CreatedAt)
	}

	fetched, err := db.GetContextDoc(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.ID != doc.ID || fetched.InitiativeID != doc.InitiativeID || fetched.Kind != doc.Kind ||
		fetched.Title != doc.Title || fetched.Digest != doc.Digest || fetched.Preview != doc.Preview {
		t.Fatalf("get mismatch: got %+v want %+v", *fetched, *doc)
	}
}
