package quality

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// OpsInput is what operational_readiness judges: the workspace's source
// files and build files (path -> text) and the initiative's changed files.
type OpsInput struct {
	Files   map[string]string
	Changed []ChangedFile
}

// The operational-readiness heuristics. Each is a regular expression over
// source text, chosen to recognize the idiomatic form in Go, Node and Python
// and nothing looser; docs/configuration.md lists them verbatim. A project
// that does the thing some other way is reported as a warning, never a
// failure -- an absent pattern is weak evidence -- and only migration safety,
// where the pattern is the problem itself, can fail.
var (
	serviceRes = []*regexp.Regexp{
		regexp.MustCompile(`\bhttp\.ListenAndServe(?:TLS)?\(|\.ListenAndServe(?:TLS)?\(\)|\bgrpc\.NewServer\(|\bhttp\.Serve\(`),
		regexp.MustCompile(`\.listen\(\s*(?:[\w.]*(?:port|PORT)|\d{2,5})|\bcreateServer\(`),
		regexp.MustCompile(`\buvicorn\.run\(|\bFastAPI\(|\bFlask\(__name__\)|\bapp\.run\(`),
	}
	healthRouteRe = regexp.MustCompile(`(?i)/(?:health|healthz|healthcheck|health-check|livez|livenessz?|readyz|readiness|ready|ping)$`)
	// SIGTERM handling: Go signal.Notify/NotifyContext naming SIGTERM (on the
	// same statement, which is how it is written), Node process.on('SIGTERM'),
	// Python signal.signal(signal.SIGTERM, ...).
	sigtermRes = []*regexp.Regexp{
		regexp.MustCompile(`signal\.Notify(?:Context)?\([^\n]*SIGTERM`),
		regexp.MustCompile(`process\.(?:on|once)\(\s*['"]SIGTERM['"]`),
		regexp.MustCompile(`signal\.signal\(\s*signal\.SIGTERM`),
	}
	goShutdownRe = regexp.MustCompile(`\.Shutdown\(|\.GracefulStop\(`)
	// uvicorn/gunicorn install their own SIGTERM handler and drain requests.
	pyManagedServerRe = regexp.MustCompile(`\buvicorn\.run\(|\bgunicorn\b`)
	structuredLogRes  = []*regexp.Regexp{
		regexp.MustCompile(`"log/slog"|"go\.uber\.org/zap"|"github\.com/rs/zerolog[^"]*"|"github\.com/sirupsen/logrus"`),
		regexp.MustCompile(`require\(\s*['"](?:pino|winston|bunyan)['"]\s*\)|from\s+['"](?:pino|winston|bunyan)['"]`),
		regexp.MustCompile(`(?m)^\s*(?:import|from)\s+(?:structlog|pythonjsonlogger)\b`),
	}
	nodeStructuredDepRe = regexp.MustCompile(`"(?:pino|winston|bunyan)"\s*:`)
	healthcheckRe       = regexp.MustCompile(`(?im)^\s*HEALTHCHECK\s`)
	destructiveSQLRe    = regexp.MustCompile(`(?i)\b(DROP\s+TABLE|DROP\s+COLUMN|TRUNCATE(?:\s+TABLE)?|ALTER\s+TABLE\s+\S+\s+RENAME\b|DROP\s+SCHEMA|DELETE\s+FROM\s+\w+\s*;)`)
	numberedMigrationRe = regexp.MustCompile(`^\d+[_\-.]`)
)

// IsMigrationPath reports whether p is a database migration: any .sql file,
// or any file in a directory named migrations/migration/migrate/db/migrate.
func IsMigrationPath(p string) bool {
	if strings.HasSuffix(strings.ToLower(p), ".sql") {
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch strings.ToLower(seg) {
		case "migrations", "migration", "migrate":
			return true
		}
	}
	return false
}

// OpsChecks runs operational_readiness's deterministic checks. The first
// four apply only to a service (a file that starts a network server by one of
// serviceRes); for a library or CLI they are not_applicable:
//
//  1. health_endpoint: a route whose last segment is health/healthz/
//     healthcheck/livez/readyz/ready/ping (warn when absent).
//  2. graceful_shutdown: SIGTERM is caught (and, in Go, a Shutdown/
//     GracefulStop call exists), or a Python server that drains on its own
//     (uvicorn/gunicorn) is used (warn when absent).
//  3. structured_logging: a structured logger is imported (log/slog, zap,
//     zerolog, logrus; pino, winston, bunyan; structlog, python-json-logger)
//     (warn when absent).
//  4. config_from_env: the code reads configuration from the environment
//     (warn when it reads none).
//  5. dockerfile_healthcheck: every Dockerfile has a HEALTHCHECK
//     instruction (warn); not_applicable without a Dockerfile.
//  6. migration_safety: a changed migration that was already numbered and
//     present before the change was edited (fail -- a database that applied
//     it will not re-run it), or an added/changed migration contains a
//     destructive statement (DROP TABLE/COLUMN/SCHEMA, TRUNCATE, RENAME,
//     unqualified DELETE) (warn); not_applicable when no migration changed.
func OpsChecks(in OpsInput) []domain.QualityCheck {
	det := domain.QualityMethodDeterministic
	var out []domain.QualityCheck
	paths := make([]string, 0, len(in.Files))
	for p := range in.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	find := func(res []*regexp.Regexp, filter func(string) bool) (string, int, bool) {
		for _, p := range paths {
			if filter != nil && !filter(p) {
				continue
			}
			for _, re := range res {
				if loc := re.FindStringIndex(in.Files[p]); loc != nil {
					return p, lineOf(in.Files[p], loc[0]), true
				}
			}
		}
		return "", 0, false
	}
	source := func(p string) bool { return IsSourcePath(p) }

	svcPath, svcLine, isService := find(serviceRes, source)
	if !isService {
		for _, n := range []string{"health_endpoint", "graceful_shutdown", "structured_logging", "config_from_env"} {
			out = append(out, domain.QualityCheck{Name: n, Status: domain.QualityNotApplicable, Method: det,
				Detail: "no network server start found (ListenAndServe, .listen(port), uvicorn.run, ...): not a service"})
		}
	} else {
		svc := domain.QualityCitation{Path: svcPath, Line: svcLine}
		// health endpoint
		var health *SurfaceItem
		for _, p := range paths {
			if !source(p) {
				continue
			}
			for _, it := range SurfaceItems(p, in.Files[p]) {
				if it.Kind == "route" && healthRouteRe.MatchString(strings.TrimSuffix(it.Name, "/")) {
					it := it
					health = &it
					break
				}
			}
			if health != nil {
				break
			}
		}
		if health != nil {
			out = append(out, domain.QualityCheck{Name: "health_endpoint", Status: domain.QualityPass, Method: det,
				Detail: "health route " + health.Name, Citations: []domain.QualityCitation{{Path: health.Path, Line: health.Line}}})
		} else {
			out = append(out, domain.QualityCheck{Name: "health_endpoint", Status: domain.QualityWarn, Method: det,
				Detail: "the service registers no /health, /healthz, /livez, /readyz, /ready or /ping route", Citations: []domain.QualityCitation{svc}})
		}
		// graceful shutdown
		if p, l, ok := find(sigtermRes, source); ok {
			if strings.HasSuffix(p, ".go") {
				if _, _, has := find([]*regexp.Regexp{goShutdownRe}, func(q string) bool { return strings.HasSuffix(q, ".go") && source(q) }); !has {
					out = append(out, domain.QualityCheck{Name: "graceful_shutdown", Status: domain.QualityWarn, Method: det,
						Detail: "SIGTERM is caught but no Shutdown/GracefulStop call drains in-flight requests", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
				} else {
					out = append(out, domain.QualityCheck{Name: "graceful_shutdown", Status: domain.QualityPass, Method: det,
						Detail: "SIGTERM is caught and the server is shut down", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
				}
			} else {
				out = append(out, domain.QualityCheck{Name: "graceful_shutdown", Status: domain.QualityPass, Method: det,
					Detail: "SIGTERM is handled", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
			}
		} else if p, l, ok := find([]*regexp.Regexp{pyManagedServerRe}, func(q string) bool {
			return strings.HasSuffix(q, ".py") || path.Base(q) == "Procfile" || path.Base(q) == "Dockerfile"
		}); ok {
			out = append(out, domain.QualityCheck{Name: "graceful_shutdown", Status: domain.QualityPass, Method: det,
				Detail: "served by uvicorn/gunicorn, which drain on SIGTERM", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
		} else {
			out = append(out, domain.QualityCheck{Name: "graceful_shutdown", Status: domain.QualityWarn, Method: det,
				Detail: "no SIGTERM handling found: a container stop kills in-flight requests", Citations: []domain.QualityCitation{svc}})
		}
		// structured logging
		if p, l, ok := find(structuredLogRes, source); ok {
			out = append(out, domain.QualityCheck{Name: "structured_logging", Status: domain.QualityPass, Method: det,
				Detail: "a structured logger is used", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
		} else if p, l, ok := find([]*regexp.Regexp{nodeStructuredDepRe}, func(q string) bool { return path.Base(q) == "package.json" }); ok {
			out = append(out, domain.QualityCheck{Name: "structured_logging", Status: domain.QualityPass, Method: det,
				Detail: "a structured logger is a dependency", Citations: []domain.QualityCitation{{Path: p, Line: l}}})
		} else {
			out = append(out, domain.QualityCheck{Name: "structured_logging", Status: domain.QualityWarn, Method: det,
				Detail: "no structured logger (slog, zap, zerolog, logrus, pino, winston, bunyan, structlog) found"})
		}
		// config via env
		var env *SurfaceItem
		for _, p := range paths {
			if !source(p) {
				continue
			}
			for _, it := range SurfaceItems(p, in.Files[p]) {
				if it.Kind == "env" {
					it := it
					env = &it
					break
				}
			}
			if env != nil {
				break
			}
		}
		if env != nil {
			out = append(out, domain.QualityCheck{Name: "config_from_env", Status: domain.QualityPass, Method: det,
				Detail: "configuration is read from the environment (" + env.Name + ")", Citations: []domain.QualityCitation{{Path: env.Path, Line: env.Line}}})
		} else {
			out = append(out, domain.QualityCheck{Name: "config_from_env", Status: domain.QualityWarn, Method: det,
				Detail: "the service reads no environment variables: its configuration cannot change per deployment without a rebuild", Citations: []domain.QualityCitation{svc}})
		}
	}

	// Dockerfile HEALTHCHECK
	var dockerfiles []string
	for _, p := range paths {
		b := path.Base(p)
		if b == "Dockerfile" || strings.HasPrefix(b, "Dockerfile.") || strings.HasSuffix(b, ".Dockerfile") {
			dockerfiles = append(dockerfiles, p)
		}
	}
	if len(dockerfiles) == 0 {
		out = append(out, domain.QualityCheck{Name: "dockerfile_healthcheck", Status: domain.QualityNotApplicable, Method: det, Detail: "no Dockerfile"})
	} else {
		var missing []domain.QualityCitation
		for _, p := range dockerfiles {
			if !healthcheckRe.MatchString(in.Files[p]) {
				missing = append(missing, domain.QualityCitation{Path: p})
			}
		}
		if len(missing) == 0 {
			out = append(out, domain.QualityCheck{Name: "dockerfile_healthcheck", Status: domain.QualityPass, Method: det,
				Detail: "every Dockerfile declares a HEALTHCHECK"})
		} else {
			out = append(out, domain.QualityCheck{Name: "dockerfile_healthcheck", Status: domain.QualityWarn, Method: det,
				Detail: fmt.Sprintf("%d Dockerfile(s) without a HEALTHCHECK instruction", len(missing)), Citations: missing})
		}
	}

	out = append(out, migrationSafety(in.Changed))
	return out
}

func migrationSafety(changed []ChangedFile) domain.QualityCheck {
	det := domain.QualityMethodDeterministic
	var fails, warns []domain.QualityCitation
	var failNotes, warnNotes []string
	considered := 0
	for _, cf := range changed {
		if !IsMigrationPath(cf.Path) || !cf.AfterExists {
			continue
		}
		considered++
		if cf.BeforeExists && cf.BeforeKnown && cf.Before != cf.After && numberedMigrationRe.MatchString(path.Base(cf.Path)) {
			fails = append(fails, domain.QualityCitation{Path: cf.Path})
			failNotes = append(failNotes, cf.Path+" was edited after it existed")
		}
		for _, loc := range destructiveSQLRe.FindAllStringIndex(cf.After, -1) {
			stmt := strings.ToUpper(strings.Join(strings.Fields(cf.After[loc[0]:loc[1]]), " "))
			if cf.BeforeExists && strings.Contains(strings.ToUpper(cf.Before), cf.After[loc[0]:loc[1]]) {
				continue // already there before the change
			}
			warns = append(warns, domain.QualityCitation{Path: cf.Path, Line: lineOf(cf.After, loc[0])})
			warnNotes = append(warnNotes, stmt)
		}
	}
	switch {
	case considered == 0:
		return domain.QualityCheck{Name: "migration_safety", Status: domain.QualityNotApplicable, Method: det, Detail: "the change touched no migrations"}
	case len(fails) > 0:
		return domain.QualityCheck{Name: "migration_safety", Status: domain.QualityFail, Method: det,
			Detail:    "an existing numbered migration was edited (a database that already applied it will not re-run it; add a new migration instead): " + strings.Join(failNotes, "; "),
			Citations: append(fails, warns...)}
	case len(warns) > 0:
		return domain.QualityCheck{Name: "migration_safety", Status: domain.QualityWarn, Method: det,
			Detail:    "destructive migration statements need a backup or expand/contract plan: " + strings.Join(uniqueStrings(warnNotes), ", "),
			Citations: warns}
	default:
		return domain.QualityCheck{Name: "migration_safety", Status: domain.QualityPass, Method: det,
			Detail: fmt.Sprintf("%d changed migration(s): additive only, no edited history", considered)}
	}
}

func lineOf(s string, offset int) int {
	return strings.Count(s[:offset], "\n") + 1
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
