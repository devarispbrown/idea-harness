package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/id"
	"github.com/devarispbrown/idea-harness/internal/quality"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// QualitySandbox runs one command for a quality dimension in an isolated
// container. The real implementation is executor.ContainerSandbox; tests
// script it. Available reports whether it can run anything at all -- a
// dimension that needs it and finds it unavailable reports not_measured.
type QualitySandbox interface {
	Available(ctx context.Context) bool
	Run(ctx context.Context, image string, opts executor.RunOptions) (output string, exitCode int, err error)
}

// SetQualitySandbox replaces the Docker sandbox verify's quality dimensions
// run commands in (tests).
func (e *Engine) SetQualitySandbox(s QualitySandbox) { e.qualitySandbox = s }

func (e *Engine) sandboxForQuality() QualitySandbox {
	if e.qualitySandbox != nil {
		return e.qualitySandbox
	}
	return dockerQualitySandbox{}
}

type dockerQualitySandbox struct{}

func (dockerQualitySandbox) Available(ctx context.Context) bool { return executor.DockerReachable(ctx) }

func (dockerQualitySandbox) Run(ctx context.Context, image string, opts executor.RunOptions) (string, int, error) {
	stdout, stderr, code, err := executor.NewContainerSandbox(image).Run(ctx, opts)
	return stdout + stderr, code, err
}

// QualityDimensionView is one row of the scorecard: the dimension, its mode
// under the current policy, its latest result (nil when it was never
// measured), and whether that result is stale -- measured against different
// applied work or a different policy than the initiative has now.
type QualityDimensionView struct {
	Dimension domain.QualityDimension `json:"dimension"`
	Mode      domain.QualityMode      `json:"mode"`
	Result    *domain.QualityResult   `json:"result"`
	Stale     bool                    `json:"stale"`
}

// QualityScorecard is verify's quality-dimension scorecard for an initiative.
type QualityScorecard struct {
	// Rigor is the effective review rigor the default modes came from.
	Rigor string `json:"rigor"`
	// Overridden lists the dimensions verify.dimensions set explicitly.
	Overridden []domain.QualityDimension `json:"overridden"`
	Dimensions []QualityDimensionView    `json:"dimensions"`
	// InputDigest is what a fresh measurement would be recorded against now.
	InputDigest string `json:"input_digest"`
	// NeedsMeasurement is true when any enabled dimension has no result for
	// the current input digest -- what `idea verify` checks before deciding
	// to measure.
	NeedsMeasurement bool `json:"needs_measurement"`
	// GateUnmet names every gated dimension that does not satisfy the gate
	// (failed, not measured, or stale). Empty means the quality half of the
	// verification gate is met; advisory dimensions never appear here.
	GateUnmet []string `json:"gate_unmet"`
}

// qualityPolicy resolves the effective rigor and every dimension's mode.
// Rigor is review.rigor, calibrated from project scale and the size of the
// initiative's applied change exactly as the reviews calibrate it, so "auto"
// means the same thing here as everywhere else.
func (e *Engine) qualityPolicy(ctx context.Context, changedFiles int) quality.Policy {
	snap := e.snapshotConfig()
	rigor := calibrateRigor(RigorLevel(snap.cfg.Review.Rigor), projectScaleOf(ctx, e.workspaceRoot), changeSize{Files: changedFiles})
	return quality.ResolvePolicy(string(rigor), snap.cfg.Verify.Dimensions)
}

// appliedChangeSets returns the initiative's applied, non-empty change sets in
// the order they were created.
func (e *Engine) appliedChangeSets(ctx context.Context, initiativeID string) ([]domain.ChangeSet, error) {
	all, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	var out []domain.ChangeSet
	for _, cs := range all {
		if cs.ImplementsTask() {
			out = append(out, cs)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// qualityInputDigest names what a measurement measures: the applied change
// sets, the resolved policy, and the verify configuration. Anything that
// changes one of them makes a prior result stale.
//
// What it cannot see is the workspace being edited outside the harness
// between two verifies; that is recorded in docs/configuration.md rather
// than papered over with a full-tree hash on every scorecard read.
func (e *Engine) qualityInputDigest(applied []domain.ChangeSet, p quality.Policy) string {
	h := sha256.New()
	for _, cs := range applied {
		fmt.Fprintf(h, "cs:%s\n", cs.ID)
	}
	fmt.Fprintf(h, "rigor:%s\n", p.Rigor)
	for _, d := range domain.QualityDimensions {
		fmt.Fprintf(h, "mode:%s=%s\n", d, p.Modes[d])
	}
	snap := e.snapshotConfig()
	vc, _ := json.Marshal(snap.cfg.Verify)
	fmt.Fprintf(h, "verify:%s\ncheck_image:%s\n", vc, snap.cfg.Check.Image)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// changedPathCount counts the distinct files the applied change sets touched,
// for rigor calibration.
func (e *Engine) changedPathCount(applied []domain.ChangeSet) int {
	seen := map[string]bool{}
	for i := range applied {
		p, err := e.loadPatch(&applied[i])
		if err != nil {
			continue
		}
		for _, f := range p.Files {
			seen[f.Path] = true
		}
	}
	return len(seen)
}

// QualityScorecard reads the current scorecard without measuring anything.
func (e *Engine) QualityScorecard(ctx context.Context, initiativeID string) (QualityScorecard, error) {
	applied, err := e.appliedChangeSets(ctx, initiativeID)
	if err != nil {
		return QualityScorecard{}, err
	}
	p := e.qualityPolicy(ctx, e.changedPathCount(applied))
	digest := e.qualityInputDigest(applied, p)
	latest, err := e.db.LatestQualityResults(ctx, initiativeID)
	if err != nil {
		return QualityScorecard{}, err
	}
	return buildScorecard(p, latest, digest), nil
}

func buildScorecard(p quality.Policy, latest map[domain.QualityDimension]domain.QualityResult, digest string) QualityScorecard {
	sc := QualityScorecard{
		Rigor:       p.Rigor,
		Overridden:  append([]domain.QualityDimension{}, p.Overridden...),
		Dimensions:  make([]QualityDimensionView, 0, len(domain.QualityDimensions)),
		InputDigest: digest,
		GateUnmet:   quality.GateUnmet(p, latest, digest),
	}
	if sc.GateUnmet == nil {
		sc.GateUnmet = []string{}
	}
	for _, d := range domain.QualityDimensions {
		v := QualityDimensionView{Dimension: d, Mode: p.Modes[d]}
		if r, ok := latest[d]; ok {
			r := r
			v.Result = &r
			v.Stale = r.InputDigest != digest
		}
		if v.Mode != domain.QualityModeOff && (v.Result == nil || v.Stale) {
			sc.NeedsMeasurement = true
		}
		sc.Dimensions = append(sc.Dimensions, v)
	}
	return sc
}

// MeasureQuality measures every enabled quality dimension against the
// initiative's applied work and records the results as evidence, then
// returns the updated scorecard. Dimensions whose mode is off are not
// measured and not recorded.
//
// The workspace is read under mu -- the lock execute/apply/revert hold -- so
// a measurement never sees half an apply; the sandbox runs and the model
// review happen after it is released, against a private copy, so a
// minutes-long reproducibility run does not hold every other mutation up.
func (e *Engine) MeasureQuality(ctx context.Context, initiativeID string) (QualityScorecard, error) {
	snap, err := e.snapshotQualityInputs(ctx, initiativeID)
	if err != nil {
		return QualityScorecard{}, err
	}
	defer snap.cleanup()

	measurementID := id.New()
	var results []domain.QualityResult
	for _, d := range domain.QualityDimensions {
		mode := snap.policy.Modes[d]
		if mode == domain.QualityModeOff {
			continue
		}
		r := e.measureDimension(ctx, initiativeID, d, snap)
		r.MeasurementID = measurementID
		r.Dimension = d
		r.Mode = mode
		r.InputDigest = snap.digest
		if r.Checks == nil {
			r.Checks = []domain.QualityCheck{}
		}
		r.Status = quality.Rollup(r.Checks)
		r.Summary = quality.Summarize(r.Checks)
		results = append(results, r)
	}
	if len(results) > 0 {
		links, err := e.qualityLinks(ctx, initiativeID, snap, results)
		if err != nil {
			return QualityScorecard{}, err
		}
		if err := e.db.RecordQualityMeasurement(ctx, initiativeID, results, links); err != nil {
			return QualityScorecard{}, err
		}
	}
	latest, err := e.db.LatestQualityResults(ctx, initiativeID)
	if err != nil {
		return QualityScorecard{}, err
	}
	return buildScorecard(snap.policy, latest, snap.digest), nil
}

// qualitySnapshot is everything the dimensions read, captured under mu.
type qualitySnapshot struct {
	policy  quality.Policy
	digest  string
	applied []domain.ChangeSet
	// pathChangeSets maps a changed path to the change sets that touched it.
	pathChangeSets map[string][]string
	changed        []quality.ChangedFile
	paths          []string          // every workspace file (git's view)
	docs           map[string]string // docs + OpenAPI specs
	sources        map[string]string // source + build files, bounded
	rootFiles      map[string]string // README*, Makefile, package.json at the root
	hasWorkspace   bool
	// freshCopy is a private copy of the workspace's committed-or-committable
	// files (git ls-files -co --exclude-standard, minus .env files and the
	// harness's own directory) for sandboxed commands: what a fresh clone of
	// the applied workspace would contain. "" when no enabled dimension
	// needs one.
	freshCopy string
}

func (s *qualitySnapshot) cleanup() {
	if s.freshCopy != "" {
		_ = os.RemoveAll(s.freshCopy)
	}
}

// snapshotBudget bounds how much of the workspace the deterministic
// dimensions read, so a measurement stays cheap on a large repository: past
// it, further files are not read (the checks say what they saw).
const (
	snapshotMaxFile  = 1 << 20 // 1 MiB per file
	snapshotMaxTotal = 16 << 20
)

func (e *Engine) snapshotQualityInputs(ctx context.Context, initiativeID string) (*qualitySnapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	applied, err := e.appliedChangeSets(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	snap := &qualitySnapshot{
		applied:        applied,
		pathChangeSets: map[string][]string{},
		docs:           map[string]string{},
		sources:        map[string]string{},
		rootFiles:      map[string]string{},
		hasWorkspace:   e.workspaceRoot != "",
	}

	// Changed files: the pre-image is the FIRST applied change set's base
	// for that path (what the file was before this initiative touched it),
	// the post-image is the workspace now.
	firstSeen := map[string]domain.FileChange{}
	var order []string
	for i := range applied {
		p, err := e.loadPatch(&applied[i])
		if err != nil {
			return nil, err
		}
		for _, fc := range p.Files {
			if _, ok := firstSeen[fc.Path]; !ok {
				firstSeen[fc.Path] = fc
				order = append(order, fc.Path)
			}
			snap.pathChangeSets[fc.Path] = append(snap.pathChangeSets[fc.Path], applied[i].ID)
		}
	}
	sort.Strings(order)
	for _, p := range order {
		fc := firstSeen[p]
		cf := quality.ChangedFile{Path: p, BeforeKnown: true}
		if fc.ChangeType != domain.FileAdded && fc.BaseDigest != "" {
			cf.BeforeExists = true
			cf.BeforeKnown = false
			if b := e.contentBlobs(); b != nil {
				if data, err := b.Get(fc.BaseDigest); err == nil {
					cf.Before, cf.BeforeKnown = string(data), true
				}
			}
		}
		if snap.hasWorkspace {
			if data, err := readBounded(filepath.Join(e.workspaceRoot, filepath.FromSlash(p))); err == nil {
				cf.After, cf.AfterExists = data, true
			}
		}
		snap.changed = append(snap.changed, cf)
	}

	snap.policy = e.qualityPolicy(ctx, len(order))
	snap.digest = e.qualityInputDigest(applied, snap.policy)

	if !snap.hasWorkspace {
		return snap, nil
	}
	paths, err := workspaceFiles(ctx, e.workspaceRoot)
	if err != nil {
		return nil, err
	}
	var kept []string
	for _, p := range paths {
		if seedExcluded(p) || isEnvFile(p) {
			continue
		}
		kept = append(kept, p)
	}
	snap.paths = kept
	var total int
	for _, p := range kept {
		if total >= snapshotMaxTotal {
			break
		}
		base := path.Base(p)
		root := !strings.Contains(p, "/")
		wantRoot := root && (strings.HasPrefix(strings.ToLower(base), "readme") || base == "Makefile" || base == "package.json" || base == "go.mod")
		wantDoc := quality.IsDocPath(p) || isSpecName(p)
		wantSrc := quality.IsSourcePath(p) || base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") || strings.HasSuffix(base, ".Dockerfile") ||
			base == "package.json" || base == "Procfile"
		if !wantRoot && !wantDoc && !wantSrc {
			continue
		}
		data, err := readBounded(filepath.Join(e.workspaceRoot, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		total += len(data)
		if wantRoot {
			snap.rootFiles[p] = data
		}
		if wantDoc {
			if quality.IsDocPath(p) || quality.LooksLikeOpenAPI([]byte(data)) {
				snap.docs[p] = data
			}
		}
		if wantSrc {
			snap.sources[p] = data
		}
	}

	if needsFreshCopy(snap.policy) {
		dir, err := e.makeFreshCopy(kept)
		if err != nil {
			return nil, err
		}
		snap.freshCopy = dir
	}
	return snap, nil
}

// isSpecName matches the conventional OpenAPI spec file names. Content
// decides in the end (LooksLikeOpenAPI); the name only decides what is read.
func isSpecName(p string) bool {
	b := strings.ToLower(path.Base(p))
	if !(strings.HasSuffix(b, ".yaml") || strings.HasSuffix(b, ".yml") || strings.HasSuffix(b, ".json")) {
		return false
	}
	return strings.Contains(b, "openapi") || strings.Contains(b, "swagger")
}

// needsFreshCopy reports whether any enabled dimension runs sandboxed
// commands (setup, performance budgets, security scanners).
func needsFreshCopy(p quality.Policy) bool {
	if p.Modes[domain.QualitySetupReproducibility] != domain.QualityModeOff {
		return true
	}
	if p.Modes[domain.QualityPerformance] != domain.QualityModeOff {
		return true
	}
	return p.Modes[domain.QualitySecurity] != domain.QualityModeOff
}

// makeFreshCopy copies the workspace's files into a private directory under
// the engine's work root (visible to Docker the way the execute work root
// is), for the sandboxed dimensions.
func (e *Engine) makeFreshCopy(paths []string) (string, error) {
	parent := e.workRoot
	if parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", err
		}
	}
	dir, err := os.MkdirTemp(parent, "quality-*")
	if err != nil {
		return "", err
	}
	for _, rel := range paths {
		src := filepath.Join(e.workspaceRoot, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(dst, data, fi.Mode().Perm()|0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func readBounded(p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > snapshotMaxFile {
		return "", fmt.Errorf("not read: not a regular file or larger than %d bytes", snapshotMaxFile)
	}
	data, err := os.ReadFile(p)
	return string(data), err
}

// qualityLinks decides which requirements each result is evidence for.
//
// A result citing files the initiative changed is linked to the requirements
// the change sets that touched those files implement (through their task's
// `implements` edges), plus any requirement a check cites by code. A result
// that cites no changed file -- setup reproducibility, a scanner run -- is
// about the applied work as a whole and is linked to every requirement any
// applied change set implements.
func (e *Engine) qualityLinks(ctx context.Context, initiativeID string, snap *qualitySnapshot, results []domain.QualityResult) (map[domain.QualityDimension][]store.QualityLink, error) {
	reqs, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	byID := map[string]domain.Requirement{}
	byCode := map[string]domain.Requirement{}
	for _, r := range reqs {
		if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
			continue
		}
		byID[r.ID] = r
		byCode[NormalizeRequirementRef(r.Code)] = r
	}
	csReqs := map[string][]string{} // change set id -> requirement ids
	var all []string
	allSeen := map[string]bool{}
	for _, cs := range snap.applied {
		if cs.TaskID == "" {
			continue
		}
		edges, err := e.db.ListGraphEdgesFrom(ctx, initiativeID, "task", cs.TaskID)
		if err != nil {
			return nil, err
		}
		for _, ed := range edges {
			if ed.EdgeType != domain.EdgeImplements || ed.ToType != "requirement" {
				continue
			}
			r, ok := byID[ed.ToID]
			if !ok {
				if r2, err := e.db.GetRequirementByRef(ctx, initiativeID, ed.ToID); err == nil {
					r, ok = *r2, r2.Status != domain.RequirementRemoved && r2.SupersededBy == ""
				}
			}
			if !ok {
				continue
			}
			csReqs[cs.ID] = append(csReqs[cs.ID], r.ID)
			byID[r.ID] = r
			if !allSeen[r.ID] {
				allSeen[r.ID] = true
				all = append(all, r.ID)
			}
		}
	}
	out := map[domain.QualityDimension][]store.QualityLink{}
	for _, res := range results {
		set := map[string]bool{}
		for _, c := range res.Checks {
			for _, cit := range c.Citations {
				for _, csID := range snap.pathChangeSets[cit.Path] {
					for _, rid := range csReqs[csID] {
						set[rid] = true
					}
				}
				if cit.Requirement != "" {
					if r, ok := byCode[NormalizeRequirementRef(cit.Requirement)]; ok {
						set[r.ID] = true
					}
				}
			}
		}
		if len(set) == 0 {
			for _, rid := range all {
				set[rid] = true
			}
		}
		ids := make([]string, 0, len(set))
		for rid := range set {
			ids = append(ids, rid)
		}
		sort.Slice(ids, func(i, j int) bool { return byID[ids[i]].Code < byID[ids[j]].Code })
		for _, rid := range ids {
			out[res.Dimension] = append(out[res.Dimension], store.QualityLink{RequirementID: rid, RequirementCode: byID[rid].Code})
		}
	}
	return out, nil
}

// qualityTimeout bounds one sandboxed quality command.
func (e *Engine) qualityTimeout() time.Duration {
	cfg := e.snapshotConfig().cfg
	switch {
	case cfg.Verify.TimeoutSeconds > 0:
		return time.Duration(cfg.Verify.TimeoutSeconds) * time.Second
	case cfg.CheckTimeoutSeconds > 0:
		return time.Duration(cfg.CheckTimeoutSeconds) * time.Second
	default:
		return 10 * time.Minute
	}
}
