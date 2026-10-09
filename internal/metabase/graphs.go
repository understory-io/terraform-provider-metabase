package metabase

import (
	"context"
	"fmt"
	"net/url"
)

// RootCollection is the key the collection graph uses for "Our analytics".
const RootCollection = "root"

// CollectionGraph is GET /api/collection/graph: group id -> collection id (or
// "root") -> "read" | "write". The server leaves "none" cells out, and drops
// archived collections from the graph altogether.
type CollectionGraph struct {
	Revision int                          `json:"revision"`
	Groups   map[string]map[string]string `json:"groups"`
}

// Access returns the level a group has on a collection, "none" when absent.
func (g *CollectionGraph) Access(groupID, collection string) string {
	if v, ok := g.Groups[groupID][collection]; ok && v != "" {
		return v
	}
	return "none"
}

// GetCollectionGraph reads the collection permissions graph.
func (c *Client) GetCollectionGraph(ctx context.Context) (*CollectionGraph, error) {
	var out CollectionGraph
	if err := c.get(ctx, "/api/collection/graph", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DataGraph is GET /api/permissions/graph: group id -> database id ->
// permission key ("view-data", "create-queries", "download", "data-model",
// "details", "transforms") -> value. Values are strings, or objects such as
// {"schemas": "full"}, or per-schema maps when a permission is granular.
type DataGraph struct {
	Revision int                                  `json:"revision"`
	Groups   map[string]map[string]map[string]any `json:"groups"`
}

// GetDataGraph reads the data permissions graph.
func (c *Client) GetDataGraph(ctx context.Context) (*DataGraph, error) {
	var out DataGraph
	if err := c.get(ctx, "/api/permissions/graph", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// maxGraphAttempts bounds the read-modify-write retry on 409. Each attempt
// re-reads the graph, so a conflict only repeats if somebody else writes the
// graph between our GET and PUT that many times in a row.
const maxGraphAttempts = 5

// ModifyCollectionGraph applies a change to the collection graph without
// clobbering anyone else's.
//
// Both graphs are revisioned: a PUT names the revision it was computed from and
// the server answers 409 if the graph has moved on since. PUT
// /api/collection/graph also merges, writing only the cells in the request.
// So the safe write is: read the current graph, let diff compute just the
// cells that must change against *that* graph, and PUT those with its
// revision. On 409, start over from a fresh read. Cells nobody asked to
// change are never sent, so a concurrent edit to another collection or group
// survives, and one to the same cell is seen by the re-read rather than
// overwritten blind. `force` is never used.
//
// diff returns group id -> collection -> level for the cells to write; an
// empty result means the graph already says what was asked and nothing is
// written.
func (c *Client) ModifyCollectionGraph(ctx context.Context, diff func(*CollectionGraph) (map[string]map[string]string, error)) error {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()

	for attempt := 1; ; attempt++ {
		c.Invalidate()
		g, err := c.GetCollectionGraph(ctx)
		if err != nil {
			return err
		}
		cells, err := diff(g)
		if err != nil {
			return err
		}
		if len(cells) == 0 {
			return nil
		}
		body := map[string]any{"revision": g.Revision, "groups": cells}
		err = c.write(ctx, "PUT", "/api/collection/graph", url.Values{"skip-graph": {"true"}}, body, nil)
		if err == nil {
			return nil
		}
		if !IsConflict(err) || attempt >= maxGraphAttempts {
			return fmt.Errorf("write collection graph (revision %d, attempt %d): %w", g.Revision, attempt, err)
		}
	}
}

// ModifyDataGraph is ModifyCollectionGraph for the data permissions graph.
// PUT /api/permissions/graph merges at the level of single permission keys:
// sending {"create-queries": "no"} for a (group, database) leaves its
// view-data and download alone. diff therefore returns, per group and
// database, only the keys that change.
func (c *Client) ModifyDataGraph(ctx context.Context, diff func(*DataGraph) (map[string]map[string]map[string]any, error)) error {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()

	for attempt := 1; ; attempt++ {
		c.Invalidate()
		g, err := c.GetDataGraph(ctx)
		if err != nil {
			return err
		}
		cells, err := diff(g)
		if err != nil {
			return err
		}
		if len(cells) == 0 {
			return nil
		}
		body := map[string]any{"revision": g.Revision, "groups": cells}
		err = c.write(ctx, "PUT", "/api/permissions/graph", url.Values{"skip-graph": {"true"}}, body, nil)
		if err == nil {
			return nil
		}
		if !IsConflict(err) || attempt >= maxGraphAttempts {
			return fmt.Errorf("write data permissions graph (revision %d, attempt %d): %w", g.Revision, attempt, err)
		}
	}
}
