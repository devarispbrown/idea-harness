package quality

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

const readyServer = `package main

import (
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	addr := os.Getenv("ADDR")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	srv := &http.Server{Addr: addr, Handler: mux}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go srv.ListenAndServe()
	<-ctx.Done()
	slog.Info("shutting down")
	srv.Shutdown(context.Background())
}
`

const bareServer = `package main

import ("log"; "net/http")

func main() {
	log.Fatal(http.ListenAndServe(":8080", nil))
}
`

func TestOpsChecksReadyService(t *testing.T) {
	checks := OpsChecks(OpsInput{Files: map[string]string{
		"main.go":    readyServer,
		"Dockerfile": "FROM golang\nHEALTHCHECK CMD wget -q localhost:8080/healthz\n",
	}})
	if s := Rollup(checks); s != domain.QualityPass {
		t.Fatalf("want pass, got %s: %+v", s, checks)
	}
	if c := checkByName(t, checks, "health_endpoint"); c.Citations[0].Line != 14 {
		t.Errorf("health route must cite its line: %+v", c)
	}
	if c := checkByName(t, checks, "migration_safety"); c.Status != domain.QualityNotApplicable {
		t.Errorf("no migrations changed: %+v", c)
	}
}

func TestOpsChecksBareServiceWarns(t *testing.T) {
	checks := OpsChecks(OpsInput{Files: map[string]string{"main.go": bareServer, "Dockerfile": "FROM golang\n"}})
	for _, n := range []string{"health_endpoint", "graceful_shutdown", "structured_logging", "config_from_env", "dockerfile_healthcheck"} {
		if c := checkByName(t, checks, n); c.Status != domain.QualityWarn {
			t.Errorf("%s: want warn, got %+v", n, c)
		}
	}
	if s := Rollup(checks); s != domain.QualityWarn {
		t.Fatalf("absence heuristics only ever warn; got %s", s)
	}
}

func TestOpsChecksLibraryIsNotApplicable(t *testing.T) {
	checks := OpsChecks(OpsInput{Files: map[string]string{"lib.go": "package lib\nfunc Add(a, b int) int { return a + b }\n"}})
	if s := Rollup(checks); s != domain.QualityNotApplicable {
		t.Fatalf("a library with no Dockerfile or migrations: want not_applicable, got %s %+v", s, checks)
	}
}

func TestOpsChecksGoSigtermWithoutShutdown(t *testing.T) {
	src := strings.Replace(readyServer, "srv.Shutdown(context.Background())", "os.Exit(0)", 1)
	if c := checkByName(t, OpsChecks(OpsInput{Files: map[string]string{"main.go": src}}), "graceful_shutdown"); c.Status != domain.QualityWarn {
		t.Fatalf("catching SIGTERM without draining is a warning: %+v", c)
	}
}

func TestMigrationSafety(t *testing.T) {
	cases := []struct {
		name string
		cf   ChangedFile
		want domain.QualityStatus
	}{
		{"additive migration", ChangedFile{Path: "db/migrations/0003_add_col.sql", After: "ALTER TABLE t ADD COLUMN c INT;", AfterExists: true, BeforeKnown: true}, domain.QualityPass},
		{"destructive migration", ChangedFile{Path: "db/migrations/0004_drop.sql", After: "-- clean up\nALTER TABLE t DROP COLUMN c;\n", AfterExists: true, BeforeKnown: true}, domain.QualityWarn},
		{"edited applied migration", ChangedFile{Path: "migrations/0001_init.sql", Before: "CREATE TABLE t (a INT);", BeforeExists: true, BeforeKnown: true, After: "CREATE TABLE t (a INT, b INT);", AfterExists: true}, domain.QualityFail},
		{"unnumbered file in a migrations dir edited", ChangedFile{Path: "migrations/README.md", Before: "a", BeforeExists: true, BeforeKnown: true, After: "b", AfterExists: true}, domain.QualityPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := checkByName(t, OpsChecks(OpsInput{Files: map[string]string{}, Changed: []ChangedFile{tc.cf}}), "migration_safety")
			if c.Status != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, c)
			}
			if tc.want == domain.QualityWarn && c.Citations[0].Line != 2 {
				t.Errorf("destructive statement must cite its line: %+v", c.Citations)
			}
		})
	}
}
