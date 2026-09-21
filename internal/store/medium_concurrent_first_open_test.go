package store

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentFirstOpenOfAnUnmigratedDB is the MEDIUM regression test for
// review's "concurrent first-open" finding.
//
// Open runs every migration against a fresh database. The per-migration
// "has this version already been applied?" check used to be a standalone
// SELECT outside any transaction, so two connections opening the SAME
// un-migrated file for the first time concurrently -- a real scenario Open
// on its own does nothing to prevent -- could both read "not yet applied"
// for version 1 before either had committed, both proceed to run its DDL in
// their own transaction, and whichever committed second then re-ran DDL
// SQLite had already applied and failed outright (reproduced failing
// roughly 3 of 4 times under real concurrent opens). EnsureProject's
// single-flight (internal/daemon) is what actually protects the daemon's
// own usage in production; this test is about store.Open's own contract,
// which any other or future caller relies on directly.
//
// This is a real concurrency test, not a simulation: N goroutines really do
// call Open on the same fresh path at the same time. It is deterministic in
// the sense that matters -- the fix (applyMigration's INSERT-before-DDL
// claim, review MEDIUM) makes every interleaving safe, so this passes on
// every run, not just probabilistically more often than before.
func TestConcurrentFirstOpenOfAnUnmigratedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	dbs := make([]*DB, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			dbs[i], errs[i] = Open(path)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open #%d failed: %v", i, err)
		}
	}
	for _, db := range dbs {
		if db != nil {
			db.Close()
		}
	}
}
