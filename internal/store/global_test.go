package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenGlobalAndRegisterProject(t *testing.T) {
	db, err := OpenGlobal(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	id, err := db.RegisterProject(context.Background(), "test", "/tmp/proj", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id == "" {
		t.Fatal("expected project id")
	}
	projects, err := db.ListProjects(context.Background())
	if err != nil || len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d (err=%v)", len(projects), err)
	}
	if projects[0].RootPath != "/tmp/proj" {
		t.Fatalf("unexpected root path %q", projects[0].RootPath)
	}
}
