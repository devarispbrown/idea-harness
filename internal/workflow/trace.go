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
	var out []TraceItem
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
	var out []ImpactItem
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

// stalenessFor classifies a dependent based on the edge type (§26). Deterministic.
func stalenessFor(edgeType domain.EdgeType) string {
	switch edgeType {
	case domain.EdgeSupersedes, domain.EdgeInvalidates:
		return "obsolete"
	default:
		return "revalidate"
	}
}

// labelCache resolves short human labels for graph nodes.
type labelCache struct {
	task        map[string]string
	requirement map[string]string
	decision    map[string]string
	artifact    map[string]string
	contextDoc  map[string]string
}

func (e *Engine) buildLabelCache(ctx context.Context, initiativeID string) labelCache {
	c := labelCache{
		task:        map[string]string{},
		requirement: map[string]string{},
		decision:    map[string]string{},
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
			c.requirement[r.ID] = r.Code
		}
	}
	if decs, err := e.db.ListDecisions(ctx, initiativeID); err == nil {
		for _, d := range decs {
			c.decision[d.ID] = d.Code + " " + d.Question
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
