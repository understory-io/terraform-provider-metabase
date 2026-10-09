package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// groupIndex resolves group names to ids and back. Graph permissions are
// configured by group *name* because the name is what a reader of a plan
// recognises; "Finance: read -> write" says something, "9: read -> write"
// does not.
type groupIndex struct {
	byID   map[int]metabase.Group
	byName map[string]metabase.Group
}

// loadGroups lists the groups, plus any the graphs name that the list leaves
// out (Metabase omits an empty Data Analysts group from it).
func loadGroups(ctx context.Context, c *metabase.Client, extraIDs []string) (*groupIndex, error) {
	gs, err := c.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	idx := &groupIndex{byID: map[int]metabase.Group{}, byName: map[string]metabase.Group{}}
	add := func(g metabase.Group) {
		idx.byID[g.ID] = g
		idx.byName[g.Name] = g
	}
	for _, g := range gs {
		add(g)
	}
	for _, s := range extraIDs {
		id, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		if _, ok := idx.byID[id]; ok {
			continue
		}
		g, err := c.GetGroup(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("look up group %d named by the permissions graph: %w", id, err)
		}
		add(*g)
	}
	return idx, nil
}

// id returns the group id for name, refusing the Administrators group, whose
// access is fixed and which Metabase will not let anyone write.
func (x *groupIndex) id(name string) (int, error) {
	g, ok := x.byName[name]
	if !ok {
		return 0, fmt.Errorf("no permissions group named %q", name)
	}
	if g.IsAdmin() {
		return 0, fmt.Errorf("%q is the Administrators group, which always has full access and cannot be configured", name)
	}
	return g.ID, nil
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
