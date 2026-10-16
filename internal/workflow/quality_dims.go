package workflow

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/quality"
)

const harnessYAMLPath = ".idea-harness/harness.yaml"

var (
	detMethod = domain.QualityMethodDeterministic
)

func notMeasured(name, why string) domain.QualityCheck {
	return domain.QualityCheck{Name: name, Status: domain.QualityNotMeasured, Method: detMethod, Detail: why}
}

// measureDimension runs one dimension's checks. It never returns an error: a
// dimension that cannot look reports not_measured with the reason, which is
// the honest answer and keeps one broken dimension from losing the others.
func (e *Engine) measureDimension(ctx context.Context, initiativeID string, d domain.QualityDimension, snap *qualitySnapshot) domain.QualityResult {
	var r domain.QualityResult
	if !snap.hasWorkspace && d != domain.QualitySecurity {
		r.Checks = []domain.QualityCheck{notMeasured(string(d), "the project has no workspace directory to measure")}
		return r
	}
	switch d {
	case domain.QualitySetupReproducibility:
		r.Checks = e.measureSetup(ctx, snap)
	case domain.QualityDocsCompleteness:
		r.Checks = e.measureDocs(ctx, initiativeID, snap)
	case domain.QualityAPICompatibility:
		r.Checks = e.measureAPICompat(ctx, initiativeID, snap)
	case domain.QualityAPIErgonomics:
		r.Checks = e.measureErgonomics(ctx, initiativeID, snap)
	case domain.QualitySecurity:
		r.Checks = e.measureSecurity(ctx, initiativeID, snap)
	case domain.QualityPerformance:
		r.Checks, r.Metrics = e.measurePerformance(ctx, initiativeID, snap)
	case domain.QualityOperationalReadiness:
		r.Checks = e.measureOps(ctx, initiativeID, snap)
	}
	return r
}

// ---------- sandbox plumbing ----------

// qualityImage is the toolchain image sandboxed quality commands run in:
// check.image, or the one detected from the project's toolchain markers --
// the same resolution the build/test checks use.
func (e *Engine) qualityImage() (image, note string) {
	if img := e.snapshotConfig().cfg.Check.Image; img != "" {
		return img, ""
	}
	return detectToolchainImage(e.workspaceRoot)
}

// verifyCacheEnv moves the package caches into the fresh copy, so what a
// network-enabled prepare phase downloads is still there for the offline run
// in the next container (each container's /tmp is its own tmpfs).
var verifyCacheEnv = []string{
	"GOMODCACHE=/work/.idea-verify/gomod",
	"GOFLAGS=-modcacherw",
	"NPM_CONFIG_CACHE=/work/.idea-verify/npm",
	"PIP_CACHE_DIR=/work/.idea-verify/pip",
	"CARGO_HOME=/work/.idea-verify/cargo",
	"YARN_CACHE_FOLDER=/work/.idea-verify/yarn",
	"PNPM_HOME=/work/.idea-verify/pnpm",
}

type sandboxOutcome struct {
	out      string
	code     int
	err      error
	timedOut bool
}

func (e *Engine) sandboxRun(ctx context.Context, sb QualitySandbox, image, workDir, cmd string, network bool) sandboxOutcome {
	timeout := e.qualityTimeout()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, code, err := sb.Run(cctx, image, executor.RunOptions{
		WorkRoot:    workDir,
		Cmd:         []string{"sh", "-c", cmd},
		Network:     network,
		Env:         verifyCacheEnv,
		ProjectRoot: e.workspaceRoot, // whose uid the check runs as under a root daemon
	})
	o := sandboxOutcome{out: e.redact(out), code: code, err: err}
	if cctx.Err() == context.DeadlineExceeded {
		o.timedOut = true
	}
	return o
}

// tail returns the last n lines of s, for check details.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return truncateOutput(strings.Join(lines, "\n"))
}

// sandboxReady answers whether sandboxed commands can run at all, and if not,
// the not_measured reason.
func (e *Engine) sandboxReady(ctx context.Context, snap *qualitySnapshot) (QualitySandbox, string, string) {
	sb := e.sandboxForQuality()
	if snap.freshCopy == "" {
		return nil, "", "no fresh copy of the workspace was made"
	}
	if !sb.Available(ctx) {
		return nil, "", "Docker is not available, so nothing could run in the sandbox"
	}
	image, note := e.qualityImage()
	if image == "" {
		return nil, "", "no sandbox image: set check.image (" + note + ")"
	}
	return sb, image, ""
}

// ---------- setup_reproducibility ----------

// measureSetup runs the documented setup/build/test commands against a fresh
// copy of the applied workspace in the no-network sandbox.
//
// Commands come from verify.setup.commands when set; otherwise from the
// README's setup/build/test sections; otherwise from the Makefile's build/test
// targets; otherwise from the same toolchain detection the build/test checks
// use -- in which case the dimension warns that nothing documents them.
// Commands that fetch dependencies or start a server are skipped and listed.
// verify.setup.prepare commands (and only those) run first, with network.
func (e *Engine) measureSetup(ctx context.Context, snap *qualitySnapshot) []domain.QualityCheck {
	cfg := e.snapshotConfig().cfg
	var checks []domain.QualityCheck
	var cmds []quality.SetupCommand
	documented := true
	readme := quality.FindReadme(snap.paths)
	dirExists := func(d string) bool {
		for _, p := range snap.paths {
			if strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
	switch {
	case len(cfg.Verify.Setup.Commands) > 0:
		for _, c := range cfg.Verify.Setup.Commands {
			cmds = append(cmds, quality.SetupCommand{Cmd: c, Path: harnessYAMLPath})
		}
	default:
		if readme != "" {
			cmds = quality.ReadmeCommands(readme, snap.rootFiles[readme], dirExists)
		}
		if runnable(cmds) == 0 {
			if mk, ok := snap.rootFiles["Makefile"]; ok {
				cmds = append(cmds, quality.MakefileCommands(mk)...)
			}
		}
		if runnable(cmds) == 0 {
			documented = false
			b, t := detectCheckCommands(e.workspaceRoot)
			for _, dc := range []detectedCommand{b, t} {
				if dc.Cmd != "" {
					cmds = append(cmds, quality.SetupCommand{Cmd: dc.Cmd, Path: dc.Source})
				}
			}
		}
	}
	if runnable(cmds) == 0 {
		return []domain.QualityCheck{{Name: "documented_commands", Status: domain.QualityFail, Method: detMethod,
			Detail: "no setup, build or test command is documented in the README or Makefile, and none can be detected from the project's toolchain: a fresh clone has no known way to build"}}
	}
	if !documented {
		checks = append(checks, domain.QualityCheck{Name: "documented_commands", Status: domain.QualityWarn, Method: detMethod,
			Detail:    "the README and Makefile document no setup/build/test command; ran the commands detected from the toolchain instead",
			Citations: citeReadme(readme)})
	} else {
		checks = append(checks, domain.QualityCheck{Name: "documented_commands", Status: domain.QualityPass, Method: detMethod,
			Detail: fmt.Sprintf("%d documented command(s) found", runnable(cmds)), Citations: citeReadme(readme)})
	}

	sb, image, why := e.sandboxReady(ctx, snap)
	if sb == nil {
		return append(checks, notMeasured("fresh_clone_run", why))
	}

	for _, p := range cfg.Verify.Setup.Prepare {
		o := e.sandboxRun(ctx, sb, image, snap.freshCopy, p, true)
		c := domain.QualityCheck{Name: "prepare: " + p, Method: detMethod, Citations: []domain.QualityCitation{{Path: harnessYAMLPath}}}
		switch {
		case o.err != nil:
			return append(checks, notMeasured("prepare: "+p, "the sandbox could not start: "+o.err.Error()))
		case o.code != 0:
			c.Status = domain.QualityFail
			c.Detail = fmt.Sprintf("the network-enabled prepare command exited %d\n%s", o.code, tail(o.out, 20))
			return append(checks, c)
		default:
			c.Status = domain.QualityPass
			c.Detail = "prepare command succeeded (network enabled)"
			checks = append(checks, c)
		}
	}

	var skippedFetch []string
	failed := false
	for _, c := range cmds {
		cite := []domain.QualityCitation{{Path: c.Path, Line: c.Line}}
		if c.Skip != "" {
			if strings.Contains(c.Skip, "network") {
				skippedFetch = append(skippedFetch, c.Cmd)
			}
			checks = append(checks, domain.QualityCheck{Name: c.Cmd, Status: domain.QualityNotApplicable, Method: detMethod,
				Detail: "not run: " + c.Skip, Citations: cite})
			continue
		}
		if failed {
			checks = append(checks, domain.QualityCheck{Name: c.Cmd, Status: domain.QualityNotMeasured, Method: detMethod,
				Detail: "not run: an earlier documented command failed", Citations: cite})
			continue
		}
		o := e.sandboxRun(ctx, sb, image, snap.freshCopy, c.Cmd, false)
		chk := domain.QualityCheck{Name: c.Cmd, Method: detMethod, Citations: cite}
		switch {
		case o.err != nil && !o.timedOut:
			chk.Status = domain.QualityNotMeasured
			chk.Detail = "the sandbox could not start: " + o.err.Error()
			failed = true
		case o.timedOut:
			chk.Status = domain.QualityFail
			chk.Detail = fmt.Sprintf("timed out after %s in a fresh copy with no network\n%s", e.qualityTimeout(), tail(o.out, 20))
			failed = true
		case o.code != 0:
			failed = true
			chk.Status = domain.QualityFail
			chk.Detail = fmt.Sprintf("exit %d in a fresh copy with no network (image %s)\n%s", o.code, image, tail(o.out, 20))
			if len(skippedFetch) > 0 && len(cfg.Verify.Setup.Prepare) == 0 {
				// It might be broken, or it might only be missing the
				// dependencies the skipped fetch would have downloaded. The run
				// cannot tell, so it does not claim either.
				chk.Status = domain.QualityNotMeasured
				chk.Detail = fmt.Sprintf("exit %d offline after skipping the dependency fetch (%s); set verify.setup.prepare to fetch dependencies first so a failure here means the documented steps are broken\n%s",
					o.code, strings.Join(skippedFetch, "; "), tail(o.out, 20))
			}
		default:
			chk.Status = domain.QualityPass
			chk.Detail = "succeeded in a fresh copy with no network (image " + image + ")"
		}
		checks = append(checks, chk)
	}
	return checks
}

func runnable(cmds []quality.SetupCommand) int {
	n := 0
	for _, c := range cmds {
		if c.Skip == "" {
			n++
		}
	}
	return n
}

func citeReadme(readme string) []domain.QualityCitation {
	if readme == "" {
		return nil
	}
	return []domain.QualityCitation{{Path: readme}}
}

// ---------- docs_completeness ----------

func (e *Engine) measureDocs(ctx context.Context, initiativeID string, snap *qualitySnapshot) []domain.QualityCheck {
	in := quality.DocsInput{Paths: snap.paths, Docs: snap.docs, Changed: snap.changed}
	goChanges, _, _ := e.goAPIChanges(snap)
	in.GoAdded = goChanges.added
	for _, cf := range snap.changed {
		if !cf.AfterExists || !quality.LooksLikeOpenAPI([]byte(cf.After)) {
			continue
		}
		had := map[string]bool{}
		if cf.BeforeExists && cf.BeforeKnown {
			for _, op := range quality.OpenAPIOperations(cf.Path, []byte(cf.Before)) {
				had[op.Method+" "+op.Route] = true
			}
		}
		for _, op := range quality.OpenAPIOperations(cf.Path, []byte(cf.After)) {
			if !had[op.Method+" "+op.Route] {
				in.OpenAPIAdded = append(in.OpenAPIAdded, op)
			}
		}
	}
	checks := quality.DocsChecks(in)
	if e.snapshotConfig().cfg.Verify.WantsModelReview(string(domain.QualityDocsCompleteness)) {
		files := []quality.ChangedFile{}
		if readme := quality.FindReadme(snap.paths); readme != "" {
			files = append(files, quality.ChangedFile{Path: readme, After: snap.docs[readme], AfterExists: true, BeforeKnown: true})
		}
		for _, cf := range snap.changed {
			if quality.IsDocPath(cf.Path) || quality.IsSourcePath(cf.Path) {
				files = append(files, cf)
			}
		}
		checks = append(checks, e.qualityModelReview(ctx, initiativeID, domain.QualityDocsCompleteness, docsRubric, files)...)
	}
	return checks
}

// ---------- api_compatibility ----------

type goAPIResult struct {
	breaking, other []quality.APIChange
	added           []quality.GoSymbol
}

// goAPIChanges diffs the exported API of every importable package directory
// the initiative changed. The "before" package is the current one with each
// changed file swapped back to its pre-image (added files dropped, deleted
// ones restored), so files the change never touched count on both sides.
// considered is false when no public Go file changed; an error means some
// pre-image was unrecoverable or a file did not parse.
func (e *Engine) goAPIChanges(snap *qualitySnapshot) (res goAPIResult, considered bool, err error) {
	dirs := map[string]bool{}
	changedByPath := map[string]quality.ChangedFile{}
	for _, cf := range snap.changed {
		if quality.IsPublicGoPath(cf.Path) {
			dirs[path.Dir(cf.Path)] = true
			changedByPath[cf.Path] = cf
		}
	}
	if len(dirs) == 0 {
		return res, false, nil
	}
	before := map[string][]byte{}
	after := map[string][]byte{}
	for _, p := range snap.paths {
		if !quality.IsPublicGoPath(p) || !dirs[path.Dir(p)] {
			continue
		}
		if _, changed := changedByPath[p]; changed {
			continue
		}
		data, ok := snap.sources[p]
		if !ok {
			return res, true, fmt.Errorf("%s was not read (too large, or past the snapshot budget)", p)
		}
		before[p], after[p] = []byte(data), []byte(data)
	}
	for p, cf := range changedByPath {
		if cf.BeforeExists {
			if !cf.BeforeKnown {
				return res, true, fmt.Errorf("the pre-change content of %s is not recoverable", p)
			}
			before[p] = []byte(cf.Before)
		}
		if cf.AfterExists {
			after[p] = []byte(cf.After)
		}
	}
	bAPI, err := quality.ExportedGoAPI(before)
	if err != nil {
		return res, true, err
	}
	aAPI, err := quality.ExportedGoAPI(after)
	if err != nil {
		return res, true, err
	}
	for _, c := range quality.DiffGoAPI(bAPI, aAPI) {
		if c.Breaking {
			res.breaking = append(res.breaking, c)
		} else {
			res.other = append(res.other, c)
		}
	}
	for k, s := range aAPI {
		if _, ok := bAPI[k]; !ok {
			res.added = append(res.added, s)
		}
	}
	sort.Slice(res.added, func(i, j int) bool { return res.added[i].Key < res.added[j].Key })
	return res, true, nil
}

// measureAPICompat diffs every changed OpenAPI spec and every changed
// importable Go package, and fails on a breaking change no requirement or
// decision acknowledges. A breaking change that is acknowledged -- an active
// requirement's statement or criteria, or a decision's question or
// resolution, names the HTTP path or the Go symbol -- is a warning citing
// what acknowledged it: the change is intended, and the scorecard still
// says it is breaking.
func (e *Engine) measureAPICompat(ctx context.Context, initiativeID string, snap *qualitySnapshot) []domain.QualityCheck {
	ack := e.acknowledgements(ctx, initiativeID)
	var checks []domain.QualityCheck

	// OpenAPI
	var specChecks []domain.QualityCheck
	specChanged := false
	for _, cf := range snap.changed {
		isSpec := (cf.AfterExists && quality.LooksLikeOpenAPI([]byte(cf.After))) || (cf.BeforeKnown && cf.BeforeExists && quality.LooksLikeOpenAPI([]byte(cf.Before)))
		if !isSpec {
			continue
		}
		specChanged = true
		name := "openapi: " + cf.Path
		switch {
		case !cf.BeforeExists:
			specChecks = append(specChecks, domain.QualityCheck{Name: name, Status: domain.QualityPass, Method: detMethod,
				Detail: "the spec is new: nothing existed for a client to depend on", Citations: []domain.QualityCitation{{Path: cf.Path}}})
		case !cf.BeforeKnown:
			specChecks = append(specChecks, notMeasured(name, "the pre-change spec is not recoverable, so there is nothing to diff against"))
		case !cf.AfterExists:
			specChecks = append(specChecks, domain.QualityCheck{Name: name, Status: domain.QualityFail, Method: detMethod,
				Detail: "the spec was deleted: every operation it described is gone for a client", Citations: []domain.QualityCitation{{Path: cf.Path}}})
		default:
			changes, err := quality.DiffOpenAPI(cf.Path, []byte(cf.Before), cf.Path, []byte(cf.After))
			if err != nil {
				specChecks = append(specChecks, notMeasured(name, err.Error()))
				continue
			}
			specChecks = append(specChecks, apiCompatCheck(name, changes, ack, func(c quality.APIChange) []string {
				route := c.Subject
				if _, r, ok := strings.Cut(c.Subject, " "); ok {
					route = r
				}
				return []string{route}
			}))
		}
	}
	if !specChanged {
		hasSpec := false
		for p := range snap.docs {
			if quality.LooksLikeOpenAPI([]byte(snap.docs[p])) {
				hasSpec = true
				checks = append(checks, domain.QualityCheck{Name: "openapi", Status: domain.QualityPass, Method: detMethod,
					Detail: p + " was not changed", Citations: []domain.QualityCitation{{Path: p}}})
				break
			}
		}
		if !hasSpec {
			checks = append(checks, domain.QualityCheck{Name: "openapi", Status: domain.QualityNotApplicable, Method: detMethod, Detail: "no OpenAPI 3 spec in the workspace"})
		}
	}
	checks = append(checks, specChecks...)

	// Go exported API
	res, considered, err := e.goAPIChanges(snap)
	switch {
	case !considered:
		checks = append(checks, domain.QualityCheck{Name: "go_api", Status: domain.QualityNotApplicable, Method: detMethod,
			Detail: "no importable Go package (outside internal/, not main, not a test) was changed"})
	case err != nil:
		checks = append(checks, notMeasured("go_api", err.Error()))
	default:
		checks = append(checks, apiCompatCheck("go_api", append(res.breaking, res.other...), ack, func(c quality.APIChange) []string {
			names := []string{c.Subject}
			if _, last, ok := strings.Cut(c.Subject, "."); ok {
				names = append(names, last)
			}
			return names
		}))
	}
	return checks
}

// ackSource is one piece of approved intent a breaking change can be
// acknowledged by.
type ackSource struct {
	code, text string
}

func (e *Engine) acknowledgements(ctx context.Context, initiativeID string) []ackSource {
	var out []ackSource
	if reqs, err := e.db.ListRequirements(ctx, initiativeID); err == nil {
		for _, r := range reqs {
			if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
				continue
			}
			var b strings.Builder
			b.WriteString(r.Statement)
			for _, c := range r.AcceptanceCriteria {
				b.WriteString(" " + c.Metric + " " + c.Target + " " + c.Condition)
			}
			out = append(out, ackSource{code: r.Code, text: b.String()})
		}
	}
	if decs, err := e.db.ListDecisions(ctx, initiativeID); err == nil {
		for _, d := range decs {
			if d.SupersededBy != "" || d.Resolution == "" {
				continue // only a decided decision is intent
			}
			out = append(out, ackSource{code: d.Code, text: d.Question + " " + d.Resolution})
		}
	}
	return out
}

func acknowledgedBy(ack []ackSource, names []string) string {
	for _, a := range ack {
		for _, n := range names {
			if n == "" || len(n) < 3 {
				continue
			}
			if containsToken(a.text, n) {
				return a.code
			}
		}
	}
	return ""
}

// containsToken is a whole-token substring match: "Parse" does not match
// "ParseAll", "/items" does not match "/items/{id}".
func containsToken(text, tok string) bool {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_/{}.-])` + regexp.QuoteMeta(tok) + `($|[^A-Za-z0-9_/{}-])`)
	return re.MatchString(text)
}

func apiCompatCheck(name string, changes []quality.APIChange, ack []ackSource, names func(quality.APIChange) []string) domain.QualityCheck {
	var unacked, acked, additive []string
	var cites []domain.QualityCitation
	for _, c := range changes {
		if !c.Breaking {
			additive = append(additive, c.Detail)
			continue
		}
		if code := acknowledgedBy(ack, names(c)); code != "" {
			acked = append(acked, c.Detail+" (acknowledged by "+code+")")
			cites = append(cites, domain.QualityCitation{Path: c.Path, Line: c.Line, Requirement: code})
			continue
		}
		unacked = append(unacked, c.Detail)
		cites = append(cites, domain.QualityCitation{Path: c.Path, Line: c.Line})
	}
	chk := domain.QualityCheck{Name: name, Method: detMethod, Citations: cites}
	switch {
	case len(unacked) > 0:
		chk.Status = domain.QualityFail
		chk.Detail = fmt.Sprintf("%d breaking change(s) no requirement or decision acknowledges:\n- %s", len(unacked), strings.Join(limitLines(unacked, 20), "\n- "))
		if len(acked) > 0 {
			chk.Detail += fmt.Sprintf("\n%d more acknowledged:\n- %s", len(acked), strings.Join(limitLines(acked, 10), "\n- "))
		}
	case len(acked) > 0:
		chk.Status = domain.QualityWarn
		chk.Detail = fmt.Sprintf("%d breaking change(s), each acknowledged:\n- %s", len(acked), strings.Join(limitLines(acked, 20), "\n- "))
	default:
		chk.Status = domain.QualityPass
		if len(additive) == 0 {
			chk.Detail = "no client-visible change"
		} else {
			chk.Detail = fmt.Sprintf("%d additive change(s), none breaking", len(additive))
		}
	}
	return chk
}

func limitLines(ss []string, n int) []string {
	if len(ss) <= n {
		return ss
	}
	return append(append([]string{}, ss[:n]...), fmt.Sprintf("... and %d more", len(ss)-n))
}

// ---------- security ----------

// measureSecurity re-scans the applied files' current content with the same
// credential detector and allowlist the execute-time sweep uses, summarizes
// what the deny checks recorded for the initiative's change sets, and runs
// the configured scanners. With no scanner configured, the vulnerability
// check is not_measured: the useful scanners need a vulnerability database,
// and the sandbox has no network unless a scanner asks for it.
func (e *Engine) measureSecurity(ctx context.Context, initiativeID string, snap *qualitySnapshot) []domain.QualityCheck {
	var checks []domain.QualityCheck

	// 1. Secret re-scan of the applied content.
	var files []domain.FileChange
	content := map[string]string{}
	for _, cf := range snap.changed {
		if cf.AfterExists {
			files = append(files, domain.FileChange{Path: cf.Path, Digest: "present"})
			content[cf.Path] = cf.After
		}
	}
	switch {
	case !snap.hasWorkspace:
		checks = append(checks, notMeasured("secret_scan", "no workspace to re-scan"))
	case len(files) == 0:
		checks = append(checks, domain.QualityCheck{Name: "secret_scan", Status: domain.QualityNotApplicable, Method: detMethod, Detail: "no applied file to scan"})
	default:
		allow, err := e.secretAllowlist(ctx)
		if err != nil {
			checks = append(checks, notMeasured("secret_scan", "could not load the secret allowlist: "+err.Error()))
			break
		}
		matches := filterAllowlisted(e.scanForCredentials(files, func(fc domain.FileChange) (string, bool) {
			s, ok := content[fc.Path]
			return s, ok
		}), allow)
		var blocking, advisory []domain.QualityCitation
		for _, m := range matches {
			c := domain.QualityCitation{Path: m.Path, Line: m.Line}
			if m.Advisory {
				advisory = append(advisory, c)
			} else {
				blocking = append(blocking, c)
			}
		}
		switch {
		case len(blocking) > 0:
			checks = append(checks, domain.QualityCheck{Name: "secret_scan", Status: domain.QualityFail, Method: detMethod,
				Detail: fmt.Sprintf("%d credential-shaped value(s) in the applied files as they are now", len(blocking)), Citations: blocking})
		case len(advisory) > 0:
			checks = append(checks, domain.QualityCheck{Name: "secret_scan", Status: domain.QualityWarn, Method: detMethod,
				Detail: fmt.Sprintf("%d generic credential assignment(s) in test/fixture paths", len(advisory)), Citations: advisory})
		default:
			checks = append(checks, domain.QualityCheck{Name: "secret_scan", Status: domain.QualityPass, Method: detMethod,
				Detail: fmt.Sprintf("re-scanned %d applied file(s): no credential-shaped value", len(files))})
		}
	}

	// 2. What the deny checks recorded.
	checks = append(checks, e.denySummary(ctx, initiativeID, snap))

	// 3. Configured scanners.
	scanners := e.snapshotConfig().cfg.Verify.Security.Scanners
	if len(scanners) == 0 {
		checks = append(checks, notMeasured("vulnerability_scan",
			"no scanner configured: govulncheck, npm audit and pip-audit need a vulnerability database from the network, and the sandbox has none; "+
				"add one under verify.security.scanners (with network: true) to measure this"))
		return checks
	}
	sb, image, why := e.sandboxReady(ctx, snap)
	for _, s := range scanners {
		name := "scanner: " + s.Name
		if sb == nil {
			checks = append(checks, notMeasured(name, why))
			continue
		}
		o := e.sandboxRun(ctx, sb, image, snap.freshCopy, s.Command, s.Network)
		cite := []domain.QualityCitation{{Path: harnessYAMLPath}}
		switch {
		case o.err != nil && !o.timedOut:
			checks = append(checks, notMeasured(name, "the sandbox could not start: "+o.err.Error()))
		case o.timedOut:
			checks = append(checks, notMeasured(name, "timed out after "+e.qualityTimeout().String()))
		case o.code == 127:
			checks = append(checks, notMeasured(name, "the scanner is not installed in "+image+" (exit 127)"))
		case o.code != 0:
			checks = append(checks, domain.QualityCheck{Name: name, Status: domain.QualityFail, Method: detMethod,
				Detail: fmt.Sprintf("exit %d\n%s", o.code, tail(o.out, 30)), Citations: cite})
		default:
			checks = append(checks, domain.QualityCheck{Name: name, Status: domain.QualityPass, Method: detMethod,
				Detail: "exit 0" + map[bool]string{true: " (network enabled)", false: " (no network)"}[s.Network], Citations: cite})
		}
	}
	return checks
}

// denySummary reports the system-deny findings recorded against the
// initiative's change sets. A denied change set never applied, so a deny is
// the harness working, not a defect in the product -- it is reported, not
// failed. What does count against the product is an undisposed deny-coded or
// sensitive-area finding on a change set that WAS applied.
func (e *Engine) denySummary(ctx context.Context, initiativeID string, snap *qualitySnapshot) domain.QualityCheck {
	findings, err := e.db.ListFindingsByInitiative(ctx, initiativeID)
	if err != nil {
		return notMeasured("deny_checks", "could not read findings: "+err.Error())
	}
	applied := map[string]domain.ChangeSet{}
	for _, cs := range snap.applied {
		applied[cs.ID] = cs
	}
	denied := map[string]bool{}
	var open []string
	var cites []domain.QualityCitation
	for _, f := range findings {
		if f.TargetType != "change_set" {
			continue
		}
		cs, isApplied := applied[f.TargetID]
		if f.Code != "" && !isApplied {
			denied[f.TargetID] = true
			continue
		}
		if !isApplied || f.Disposition != "" {
			continue
		}
		if f.Code != "" || f.Category == "sensitive_area" {
			label := f.Category
			if f.Code != "" {
				label = string(f.Code)
			}
			open = append(open, cs.HumanID+": "+label+" — "+f.Finding)
			if f.SecretPath != "" {
				cites = append(cites, domain.QualityCitation{Path: f.SecretPath})
			}
		}
	}
	switch {
	case len(open) > 0:
		return domain.QualityCheck{Name: "deny_checks", Status: domain.QualityWarn, Method: detMethod,
			Detail: fmt.Sprintf("%d undisposed security finding(s) on applied change sets:\n- %s", len(open), strings.Join(limitLines(open, 10), "\n- ")), Citations: cites}
	case len(denied) > 0:
		return domain.QualityCheck{Name: "deny_checks", Status: domain.QualityPass, Method: detMethod,
			Detail: fmt.Sprintf("%d change set(s) were denied by the secret/PII/path checks and never applied; nothing open on applied work", len(denied))}
	default:
		return domain.QualityCheck{Name: "deny_checks", Status: domain.QualityPass, Method: detMethod, Detail: "no security finding on the initiative's change sets"}
	}
}

// ---------- performance ----------

func (e *Engine) measurePerformance(ctx context.Context, initiativeID string, snap *qualitySnapshot) ([]domain.QualityCheck, map[string]float64) {
	budgets := e.snapshotConfig().cfg.Verify.Performance.Budgets
	if len(budgets) == 0 {
		return []domain.QualityCheck{notMeasured("budgets", "no budgets declared under verify.performance.budgets")}, nil
	}
	var baseline map[string]float64
	if latest, err := e.db.LatestQualityResults(ctx, initiativeID); err == nil {
		baseline = latest[domain.QualityPerformance].Metrics
	}
	sb, image, why := e.sandboxReady(ctx, snap)
	metrics := map[string]float64{}
	var checks []domain.QualityCheck
	for _, b := range budgets {
		name := "budget: " + b.Name
		cite := []domain.QualityCitation{{Path: harnessYAMLPath}}
		if sb == nil {
			checks = append(checks, notMeasured(name, why))
			continue
		}
		o := e.sandboxRun(ctx, sb, image, snap.freshCopy, b.Command, false)
		switch {
		case o.err != nil && !o.timedOut:
			checks = append(checks, notMeasured(name, "the sandbox could not start: "+o.err.Error()))
			continue
		case o.timedOut:
			checks = append(checks, domain.QualityCheck{Name: name, Status: domain.QualityFail, Method: detMethod, Citations: cite,
				Detail: "the budget command timed out after " + e.qualityTimeout().String()})
			continue
		case o.code != 0:
			checks = append(checks, domain.QualityCheck{Name: name, Status: domain.QualityFail, Method: detMethod, Citations: cite,
				Detail: fmt.Sprintf("the budget command exited %d\n%s", o.code, tail(o.out, 20))})
			continue
		}
		v, err := quality.ExtractMetric(b.Metric, o.out)
		if err != nil {
			checks = append(checks, notMeasured(name, err.Error()+"\n"+tail(o.out, 10)))
			continue
		}
		metrics[b.Name] = v
		var base *float64
		if bv, ok := baseline[b.Name]; ok {
			base = &bv
		}
		st, detail := quality.BudgetVerdict(b.Name, b.Unit, v, b.Max, b.MaxRegressionPct, base)
		checks = append(checks, domain.QualityCheck{Name: name, Status: st, Method: detMethod, Detail: detail, Citations: cite})
	}
	return checks, metrics
}

// ---------- operational_readiness ----------

func (e *Engine) measureOps(ctx context.Context, initiativeID string, snap *qualitySnapshot) []domain.QualityCheck {
	checks := quality.OpsChecks(quality.OpsInput{Files: snap.sources, Changed: snap.changed})
	if e.snapshotConfig().cfg.Verify.WantsModelReview(string(domain.QualityOperationalReadiness)) {
		var files []quality.ChangedFile
		for _, cf := range snap.changed {
			if quality.IsSourcePath(cf.Path) || quality.IsMigrationPath(cf.Path) || strings.HasPrefix(path.Base(cf.Path), "Dockerfile") {
				files = append(files, cf)
			}
		}
		checks = append(checks, e.qualityModelReview(ctx, initiativeID, domain.QualityOperationalReadiness, opsRubric, files)...)
	}
	return checks
}
