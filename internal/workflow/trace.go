package workflow

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

const (
	maxTraceDepth = 8
	maxTraceNodes = 500
)

// TraceItem is one graph edge with resolved endpoint labels.
type TraceItem struct {
	EdgeType  string `json:"edge_type"`
	FromType  string `json:"from_type"`
	FromID    string `json:"from_id"`
	FromLabel string `json:"from_label,omitempty"`
	ToType    string `json:"to_type"`
	ToID      string `json:"to_id"`
	ToLabel   string `json:"to_label,omitempty"`
}

// ImpactItem is a TraceItem with a staleness classification.
type ImpactItem struct {
	TraceItem
	Staleness string `json:"staleness"`
}

// Why returns the provenance chain for a node: it follows outgoing edges
// (implements / derived_from / produced_by / depends_on) from the node to its
// sources, bounded.
func (e *Engine) Why(ctx context.Context, initiativeID, nodeType, nodeID string) ([]TraceItem, error) {
	cache := e.buildLabelCache(ctx, initiativeID)
	// Non-nil so an empty chain marshals as [] rather than null: a node with no
	// provenance is a normal answer, and a client should not have to null-check it.
	out := []TraceItem{}
	visited := map[string]bool{}
	queue := [][2]string{{nodeType, nodeID}}
	for depth := 0; depth < maxTraceDepth && len(queue) > 0; depth++ {
		var next [][2]string
		for _, n := range queue {
			key := n[0] + ":" + n[1]
			if visited[key] {
				continue
			}
			visited[key] = true
			edges, err := e.db.ListGraphEdgesFrom(ctx, initiativeID, n[0], n[1])
			if err != nil {
				return nil, err
			}
			for _, ed := range edges {
				if ed.ToType == "requirement" && !cache.isActiveRequirement(ed.ToID) {
					continue
				}
				out = append(out, TraceItem{
					EdgeType: string(ed.EdgeType), FromType: n[0], FromID: n[1],
					FromLabel: cache.label(n[0], n[1]), ToType: ed.ToType, ToID: ed.ToID, ToLabel: cache.label(ed.ToType, ed.ToID),
				})
				next = append(next, [2]string{ed.ToType, ed.ToID})
			}
		}
		queue = next
		if len(out) >= maxTraceNodes {
			break
		}
	}
	return out, nil
}

// Impact returns the dependents of a node: it follows incoming edges (from
// nodes that point to this node) with a staleness classification.
func (e *Engine) Impact(ctx context.Context, initiativeID, nodeType, nodeID string) ([]ImpactItem, error) {
	cache := e.buildLabelCache(ctx, initiativeID)
	out := []ImpactItem{} // non-nil: see Why
	visited := map[string]bool{}
	queue := [][2]string{{nodeType, nodeID}}
	for depth := 0; depth < maxTraceDepth && len(queue) > 0; depth++ {
		var next [][2]string
		for _, n := range queue {
			key := n[0] + ":" + n[1]
			if visited[key] {
				continue
			}
			visited[key] = true
			edges, err := e.db.ListGraphEdgesTo(ctx, initiativeID, n[0], n[1])
			if err != nil {
				return nil, err
			}
			for _, ed := range edges {
				if ed.FromType == "requirement" && !cache.isActiveRequirement(ed.FromID) {
					continue
				}
				out = append(out, ImpactItem{
					TraceItem: TraceItem{
						EdgeType: string(ed.EdgeType), FromType: ed.FromType, FromID: ed.FromID,
						FromLabel: cache.label(ed.FromType, ed.FromID), ToType: n[0], ToID: n[1], ToLabel: cache.label(n[0], n[1]),
					},
					Staleness: stalenessFor(ed.EdgeType),
				})
				next = append(next, [2]string{ed.FromType, ed.FromID})
			}
		}
		queue = next
		if len(out) >= maxTraceNodes {
			break
		}
	}
	return out, nil
}

// NodeRef addresses a graph node by type + id.
type NodeRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ImpactCounts returns, for each node, the number of unique transitive
// dependents (nodes that directly or indirectly depend on it). Used to render
// "blocks N" without an Impact call per node.
//
// Unlike Impact — which returns one item per edge and can repeat a node that is
// reachable via multiple paths — these counts are DEDUPED by node, so a count
// is the number of distinct blocked nodes, not the number of edges. In a cycle
// the queried node itself may appear among its dependents (mirroring Impact's
// frontier semantics).
func (e *Engine) ImpactCounts(ctx context.Context, initiativeID string, nodes []NodeRef) (map[string]int, error) {
	cache := e.buildLabelCache(ctx, initiativeID)
	out := make(map[string]int, len(nodes))
	for _, n := range nodes {
		count, err := e.impactNodeCount(ctx, initiativeID, n.Type, n.ID, cache)
		if err != nil {
			return nil, err
		}
		out[n.Type+":"+n.ID] = count
	}
	return out, nil
}

// impactNodeCount counts unique transitive dependents of one node (the incoming
// edge frontier), bounded by the same limits as Impact. Superseded/removed
// requirements are not counted as live dependents.
func (e *Engine) impactNodeCount(ctx context.Context, initiativeID, nodeType, nodeID string, cache labelCache) (int, error) {
	visited := map[string]bool{}
	dependents := map[string]bool{}
	queue := [][2]string{{nodeType, nodeID}}
	for depth := 0; depth < maxTraceDepth && len(queue) > 0; depth++ {
		var next [][2]string
		for _, n := range queue {
			key := n[0] + ":" + n[1]
			if visited[key] {
				continue
			}
			visited[key] = true
			edges, err := e.db.ListGraphEdgesTo(ctx, initiativeID, n[0], n[1])
			if err != nil {
				return 0, err
			}
			for _, ed := range edges {
				if ed.FromType == "requirement" && !cache.isActiveRequirement(ed.FromID) {
					continue
				}
				dk := ed.FromType + ":" + ed.FromID
				if !dependents[dk] {
					dependents[dk] = true
				}
				next = append(next, [2]string{ed.FromType, ed.FromID})
			}
		}
		queue = next
		if len(dependents) >= maxTraceNodes {
			break
		}
	}
	return len(dependents), nil
}

// stalenessFor classifies a dependent based on the edge type (§26). Deterministic.
func stalenessFor(edgeType domain.EdgeType) string {
	switch edgeType {
	case domain.EdgeSupersedes, domain.EdgeInvalidates:
		return "obsolete"
	default:
		return "revalidate"
	}
}

// labelCache resolves short human labels for graph nodes. requirement holds only
// ACTIVE requirements (not superseded/removed), so it doubles as the active-set
// check used to keep trace traversal from counting superseded rows.
type labelCache struct {
	task        map[string]string
	requirement map[string]string
	decision    map[string]string
	question    map[string]string
	artifact    map[string]string
	contextDoc  map[string]string
}

func (c labelCache) isActiveRequirement(id string) bool {
	_, ok := c.requirement[id]
	return ok
}

func (e *Engine) buildLabelCache(ctx context.Context, initiativeID string) labelCache {
	c := labelCache{
		task:        map[string]string{},
		requirement: map[string]string{},
		decision:    map[string]string{},
		question:    map[string]string{},
		artifact:    map[string]string{},
		contextDoc:  map[string]string{},
	}
	if tasks, err := e.db.ListTasks(ctx, initiativeID); err == nil {
		for _, t := range tasks {
			c.task[t.ID] = t.Code + " " + t.Title
		}
	}
	if reqs, err := e.db.ListRequirements(ctx, initiativeID); err == nil {
		for _, r := range reqs {
			if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
				continue
			}
			c.requirement[r.ID] = r.Code
		}
	}
	if decs, err := e.db.ListDecisions(ctx, initiativeID); err == nil {
		for _, d := range decs {
			c.decision[d.ID] = d.Code + " " + d.Question
		}
	}
	if qs, err := e.db.ListQuestions(ctx, initiativeID); err == nil {
		for _, q := range qs {
			c.question[q.ID] = q.Question
		}
	}
	if arts, err := e.db.ListArtifacts(ctx, initiativeID); err == nil {
		for _, a := range arts {
			c.artifact[a.ArtifactID] = string(a.Kind)
		}
	}
	if docs, err := e.db.ListContextDocs(ctx, initiativeID); err == nil {
		for _, d := range docs {
			if d.Title != "" {
				c.contextDoc[d.ID] = d.Title
			} else {
				c.contextDoc[d.ID] = d.Kind
			}
		}
	}
	return c
}

// GraphNodeTypes is the closed set of node types the product graph addresses.
// Exported so the daemon can reject an unknown one at the edge rather than
// answering an empty traversal.
var GraphNodeTypes = []string{
	"task", "requirement", "decision", "question", "artifact", "context_doc", "run",
}

// ValidNodeType reports whether typ names a node the graph can hold.
//
// Nothing validated it, so `idea why requirements REQ-1` -- a plural typo --
// answered "No provenance edges found." and exited 0. A bad *id* was caught and
// a bad *type* was not, which turns a provenance question into a confident
// false negative on the product's headline claim.
func ValidNodeType(typ string) bool {
	for _, t := range GraphNodeTypes {
		if t == typ {
			return true
		}
	}
	return false
}

func (c labelCache) label(typ, id string) string {
	switch typ {
	case "task":
		if s, ok := c.task[id]; ok {
			return s
		}
	case "requirement":
		if s, ok := c.requirement[id]; ok {
			return s
		}
	case "decision":
		if s, ok := c.decision[id]; ok {
			return s
		}
	case "question":
		if s, ok := c.question[id]; ok {
			return s
		}
	case "artifact":
		if s, ok := c.artifact[id]; ok {
			return s
		}
	case "context_doc":
		if s, ok := c.contextDoc[id]; ok {
			return s
		}
	case "run":
		if len(id) >= 8 {
			return "run " + id[:8]
		}
	}
	return typ + " " + id
}
