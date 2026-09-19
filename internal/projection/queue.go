package projection

import (
	"context"
	"fmt"
	"sort"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Queue rank tiers (D-b, wave 3 contract): "blocking first, then by age".
// Blocking items get the lowest (most urgent) tier; everything else sorts
// after them. Age (CreatedAt, ms) is added within a tier so an older item of
// the same urgency outranks a newer one, and tiers never overlap because each
// is a fixed multiple of a day in milliseconds -- far larger than any
// realistic age difference within a tier.
const (
	queueTierBlocking  int64 = 0
	queueTierReview    int64 = 1
	queueTierStale     int64 = 2
	queueTierVerify    int64 = 3
	queueTierTierWidth int64 = 1 << 62 / 8
)

func queueRank(tier int64, at int64) int64 { return tier*queueTierTierWidth + at }

// BuildQueue builds the "needs you" queue across every initiative in the
// project (D-b, wave 3 contract): open blocking decisions, change sets
// awaiting review, conflicts, denials, stale items, and verifications ready
// to confirm -- ranked blocking first, then by age.
//
// The returned Rank is a small 1..N ordinal (H6, review fix), not the
// internal tier*age sort key used to order them: that key is
// tier*2^59+milliseconds, well past JavaScript's safe-integer range, so the
// UI rendered it as a garbled, meaningless number ("#1729384046714066200")
// and had nothing it could compare across items anyway. 1 is always the
// most urgent item; the ordinal is documented in docs/openapi.yaml.
func BuildQueue(ctx context.Context, db *store.DB, blobs *blob.Store) ([]domain.QueueItem, error) {
	inis, err := db.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.QueueItem, 0)
	for _, ini := range inis {
		// Terminal initiatives (H3, review fix): complete has nothing left to
		// act on, and failed is resolved by `idea retry`, not by acting on a
		// queue item -- neither belongs in a list titled "needs you".
		if ini.Status == domain.InitiativeComplete || ini.Status == domain.InitiativeFailed {
			continue
		}
		items, err := buildQueueForInitiative(ctx, db, blobs, ini)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	for i := range out {
		out[i].Rank = int64(i + 1)
	}
	return out, nil
}

func buildQueueForInitiative(ctx context.Context, db *store.DB, blobs *blob.Store, ini domain.Initiative) ([]domain.QueueItem, error) {
	out := make([]domain.QueueItem, 0)
	shortTitle := ini.ShortTitle
	if shortTitle == "" {
		shortTitle = ini.Title
	}
	href := func(suffix string) string { return "#/i/" + ini.ID + "/" + suffix }
	item := func(kind, label string, rank int64, href string) domain.QueueItem {
		return domain.QueueItem{InitiativeID: ini.ID, ShortTitle: shortTitle, Kind: kind, Label: label, Rank: rank, Href: href}
	}

	// Open decisions: blocking when the initiative is actually parked on human
	// judgment right now, otherwise a live but not (yet) blocking open item --
	// still worth surfacing, just behind the ones stopping work outright.
	decisions, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range decisions {
		if d.Status != domain.DecisionOpen {
			continue
		}
		tier := queueTierReview
		if ini.Status == domain.InitiativeNeedsHumanJudgment {
			tier = queueTierBlocking
		}
		label := d.Code + ": " + truncateWords(d.Question, 80)
		out = append(out, item("decision", label, queueRank(tier, d.CreatedAt.UnixMilli()), href("decision/"+d.ID)))
	}

	// Change sets: awaiting review, conflicted, or denied.
	changeSets, err := db.ListChangeSets(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	csEvents, err := db.ListChangeSetEventsForInitiative(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	deniedAt := map[string]int64{} // change_set id -> most recent ->rejected event ms
	for _, ev := range csEvents {
		if ev.EventType == "change_set.status_changed" && ev.Operation == string(domain.ChangeSetChecksRunning)+"->"+string(domain.ChangeSetRejected) ||
			ev.EventType == "change_set.status_changed" && ev.Operation == string(domain.ChangeSetReviewRequired)+"->"+string(domain.ChangeSetRejected) ||
			ev.EventType == "change_set.status_changed" && ev.Operation == string(domain.ChangeSetApproved)+"->"+string(domain.ChangeSetRejected) ||
			ev.EventType == "change_set.status_changed" && ev.Operation == string(domain.ChangeSetConflicted)+"->"+string(domain.ChangeSetRejected) {
			deniedAt[ev.ChangeSetID] = ev.At.UnixMilli()
		}
	}
	// A denial's eligibility (H3, review fix): only the latest change set of
	// each task -- a rejected attempt since superseded by a later one (on the
	// same task) is history, not something still blocking work -- and only
	// when a finding on it actually carries a system DenyCode. A plain human
	// reject, a plain check failure (build/tests), or the restart-reconcile
	// sweep (an interrupted run recovering, no findings at all) are not a
	// "denial" the queue should keep surfacing forever.
	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	findingsByCS := map[string][]domain.Finding{}
	for _, f := range findings {
		if f.TargetType == "change_set" {
			findingsByCS[f.TargetID] = append(findingsByCS[f.TargetID], f)
		}
	}
	latestByTask := map[string]domain.ChangeSet{}
	taskKey := func(cs domain.ChangeSet) string {
		if cs.TaskID == "" {
			return "cs:" + cs.ID
		}
		return cs.TaskID
	}
	for _, cs := range changeSets {
		// ListChangeSets orders by created_at ASC with no secondary key, so
		// two change sets created in the same millisecond (routine in a fast
		// test, and not impossible in production) tie on CreatedAt alone;
		// `!Before` rather than strict `After` makes the later one in
		// iteration order win the tie, matching actual creation order
		// instead of silently keeping whichever happened to be seen first.
		key := taskKey(cs)
		cur, ok := latestByTask[key]
		if !ok || !cs.CreatedAt.Before(cur.CreatedAt) {
			latestByTask[key] = cs
		}
	}
	for _, cs := range changeSets {
		switch cs.Status {
		case domain.ChangeSetReviewRequired:
			out = append(out, item("review", cs.HumanID+" awaiting review", queueRank(queueTierReview, cs.CreatedAt.UnixMilli()), href("build")))
		case domain.ChangeSetConflicted:
			out = append(out, item("conflict", cs.HumanID+" conflicted on apply", queueRank(queueTierBlocking, cs.CreatedAt.UnixMilli()), href("build")))
		case domain.ChangeSetRejected:
			if latestByTask[taskKey(cs)].ID != cs.ID {
				continue
			}
			if !hasSystemDenyCode(findingsByCS[cs.ID]) {
				continue
			}
			if at, ok := deniedAt[cs.ID]; ok {
				out = append(out, item("denial", cs.HumanID+" denied", queueRank(queueTierBlocking, at), href("build")))
			}
		}
	}

	// Stale items: one queue entry per active stale mark's decision group
	// (mirrors BuildTimeline's grouping), pointing at acknowledging it.
	stale, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	staleByDecision := map[string]struct {
		count int
		at    int64
		first string
	}{}
	order := make([]string, 0)
	for _, s := range stale {
		g, ok := staleByDecision[s.DecisionID]
		if !ok {
			order = append(order, s.DecisionID)
			g.at = s.CreatedAt.UnixMilli()
			g.first = s.ID
		}
		g.count++
		staleByDecision[s.DecisionID] = g
	}
	for _, decisionID := range order {
		g := staleByDecision[decisionID]
		label := "a decision"
		if d, err := db.GetDecision(ctx, decisionID); err == nil && d.Code != "" {
			label = d.Code
		}
		out = append(out, item("stale", fmt.Sprintf("%d item%s stale since %s changed", g.count, plural(g.count), label),
			queueRank(queueTierStale, g.at), href("build")))
	}

	// Verifications ready to confirm: an approved requirement whose mapped
	// tests all passed, not yet confirmed.
	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	evidence, err := ComputeEvidenceTests(ctx, db, blobs, ini.ID)
	if err != nil {
		return nil, err
	}
	for _, req := range reqs {
		// H4, review fix: mirrors confirm_passing_tests' own eligibility
		// exactly (approved, active, not already decided) -- the queue used
		// to also offer failed/waived/draft/removed requirements, which
		// confirm_passing_tests would just silently skip.
		if req.SupersededBy != "" || req.Status != domain.RequirementApproved {
			continue
		}
		if !AllTestsPassed(evidence[req.ID]) {
			continue
		}
		out = append(out, item("verify", req.Code+" ready to confirm (tests passed)",
			queueRank(queueTierVerify, req.CreatedAt.UnixMilli()), href("verify")))
	}

	return out, nil
}
