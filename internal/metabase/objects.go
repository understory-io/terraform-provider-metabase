package metabase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Collection is the subset of a Metabase collection the provider manages.
type Collection struct {
	ID              int     `json:"id"`
	EntityID        string  `json:"entity_id,omitempty"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	Location        string  `json:"location,omitempty"`
	Archived        bool    `json:"archived"`
	PersonalOwnerID *int    `json:"personal_owner_id,omitempty"`
	Namespace       *string `json:"namespace,omitempty"`
	Type            *string `json:"type,omitempty"`
}

// ParentID derives the parent collection id from Location ("/12/34/" -> 34).
// A collection at the root has no parent and returns nil.
func (c Collection) ParentID() *int {
	parts := strings.Split(strings.Trim(c.Location, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return nil
	}
	return &id
}

// Ancestors returns the ids in Location, root first.
func (c Collection) Ancestors() []int {
	var out []int
	for _, p := range strings.Split(strings.Trim(c.Location, "/"), "/") {
		if id, err := strconv.Atoi(p); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// GetCollection reads one collection by numeric id or 21-character entity id.
func (c *Client) GetCollection(ctx context.Context, ref string) (*Collection, error) {
	var out Collection
	if err := c.get(ctx, "/api/collection/"+url.PathEscape(ref), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListCollections returns every collection, archived ones included, personal
// collections and those nested in them excluded. Only ordinary collections in
// the default namespace are listed: snippet and transform folders, and the
// special collections Metabase makes for itself (the Trash, Usage analytics,
// the Library), carry a namespace or a type and are not collections in the
// sense this provider means.
func (c *Client) ListCollections(ctx context.Context) ([]Collection, error) {
	var all []Collection
	for _, archived := range []string{"false", "true"} {
		// The unarchived list starts with the root collection, whose id is the
		// string "root", so the page is decoded one item at a time.
		var page []json.RawMessage
		q := url.Values{"archived": {archived}, "exclude-other-user-collections": {"true"}}
		if err := c.get(ctx, "/api/collection", q, &page); err != nil {
			return nil, err
		}
		for _, raw := range page {
			var col Collection
			if json.Unmarshal(raw, &col) == nil {
				all = append(all, col)
			}
		}
	}

	byID := map[int]Collection{}
	for _, col := range all {
		if col.ID != 0 {
			byID[col.ID] = col
		}
	}
	var out []Collection
	for _, col := range all {
		if col.ID == 0 || col.PersonalOwnerID != nil || (col.Namespace != nil && *col.Namespace != "") || (col.Type != nil && *col.Type != "") {
			continue
		}
		personal := false
		for _, a := range col.Ancestors() {
			if p, ok := byID[a]; !ok || p.PersonalOwnerID != nil {
				personal = true
			}
		}
		if !personal {
			out = append(out, col)
		}
	}
	return out, nil
}

// CreateCollection creates a collection and returns it.
// Body keys are the API's: name, description, parent_id, archived.
//
// Creating or moving a collection writes a collection-graph revision (the new
// collection copies its parent's grants), and Metabase 0.64 does not serialise
// those inserts: two concurrent creates fail with a 500 on the revision
// table's primary key. So collection writes take the same lock as graph
// writes.
func (c *Client) CreateCollection(ctx context.Context, in map[string]any) (*Collection, error) {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()
	var out Collection
	if err := c.write(ctx, "POST", "/api/collection", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateCollection patches a collection; keys left out of in are unchanged.
// It takes the graph lock for the reason CreateCollection does.
func (c *Client) UpdateCollection(ctx context.Context, id int, in map[string]any) (*Collection, error) {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()
	var out Collection
	if err := c.write(ctx, "PUT", fmt.Sprintf("/api/collection/%d", id), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Group is a permissions group.
type Group struct {
	ID             int     `json:"id"`
	EntityID       string  `json:"entity_id,omitempty"`
	Name           string  `json:"name"`
	MagicGroupType *string `json:"magic_group_type"`
	MemberCount    int     `json:"member_count,omitempty"`
}

// Magic group types Metabase creates and will not let anyone rename or delete.
const (
	MagicAdmin       = "admin"
	MagicAllUsers    = "all-internal-users"
	MagicDataAnalyst = "data-analyst"
)

// IsAdmin reports whether g is the Administrators group, whose permissions are
// fixed at full access and cannot be written.
func (g Group) IsAdmin() bool { return g.MagicGroupType != nil && *g.MagicGroupType == MagicAdmin }

// ListGroups returns every permission group. Metabase leaves the Data Analysts
// group out of this list while it has no members, so the provider also looks
// groups up one at a time (GetGroup) when a graph names an id this omits.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	var out []Group
	if err := c.get(ctx, "/api/permissions/group", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetGroup reads one permission group.
func (c *Client) GetGroup(ctx context.Context, id int) (*Group, error) {
	var out Group
	if err := c.get(ctx, fmt.Sprintf("/api/permissions/group/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateGroup creates a permission group.
// A new group gets a row in both graphs, so it is serialised with them.
func (c *Client) CreateGroup(ctx context.Context, name string) (*Group, error) {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()
	var out Group
	if err := c.write(ctx, "POST", "/api/permissions/group", nil, map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameGroup renames a permission group.
func (c *Client) RenameGroup(ctx context.Context, id int, name string) error {
	return c.write(ctx, "PUT", fmt.Sprintf("/api/permissions/group/%d", id), nil, map[string]any{"name": name}, nil)
}

// DeleteGroup deletes a permission group, its memberships and its grants.
func (c *Client) DeleteGroup(ctx context.Context, id int) error {
	c.graphMu.Lock()
	defer c.graphMu.Unlock()
	return c.write(ctx, "DELETE", fmt.Sprintf("/api/permissions/group/%d", id), nil, nil, nil)
}

// Membership is one user's membership of one group.
type Membership struct {
	MembershipID   int  `json:"membership_id"`
	GroupID        int  `json:"group_id"`
	UserID         int  `json:"user_id"`
	IsGroupManager bool `json:"is_group_manager"`
}

// ListMemberships returns every membership, for every user including API-key
// users and deactivated ones.
func (c *Client) ListMemberships(ctx context.Context) ([]Membership, error) {
	var byUser map[string][]Membership
	if err := c.get(ctx, "/api/permissions/membership", nil, &byUser); err != nil {
		return nil, err
	}
	var out []Membership
	for _, ms := range byUser {
		out = append(out, ms...)
	}
	return out, nil
}

// FindMembership returns the membership of user in group, or nil.
func (c *Client) FindMembership(ctx context.Context, groupID, userID int) (*Membership, error) {
	all, err := c.ListMemberships(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.GroupID == groupID && m.UserID == userID {
			return &m, nil
		}
	}
	return nil, nil
}

// AddMembership adds user to group.
func (c *Client) AddMembership(ctx context.Context, groupID, userID int, manager bool) error {
	body := map[string]any{"group_id": groupID, "user_id": userID}
	if manager {
		body["is_group_manager"] = true
	}
	return c.write(ctx, "POST", "/api/permissions/membership", nil, body, nil)
}

// SetGroupManager flips is_group_manager on a membership (Enterprise only).
func (c *Client) SetGroupManager(ctx context.Context, membershipID int, manager bool) error {
	return c.write(ctx, "PUT", fmt.Sprintf("/api/permissions/membership/%d", membershipID), nil,
		map[string]any{"is_group_manager": manager}, nil)
}

// RemoveMembership deletes a membership.
func (c *Client) RemoveMembership(ctx context.Context, membershipID int) error {
	return c.write(ctx, "DELETE", fmt.Sprintf("/api/permissions/membership/%d", membershipID), nil, nil, nil)
}

// User is the subset of a Metabase user the provider exposes. Names are left
// out on purpose: the provider resolves people by email and nothing more.
type User struct {
	ID          int     `json:"id"`
	Email       string  `json:"email"`
	IsActive    bool    `json:"is_active"`
	IsSuperuser bool    `json:"is_superuser"`
	SSOSource   *string `json:"sso_source"`
	GroupIDs    []int   `json:"group_ids"`
}

// ListUsers returns every human user, active and deactivated. API-key users
// and Metabase's internal user are not in this list.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var out struct {
		Data []User `json:"data"`
	}
	if err := c.get(ctx, "/api/user", url.Values{"status": {"all"}, "limit": {"100000"}}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// FindUserByEmail looks a user up by email, case-insensitively.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	users, err := c.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, nil
}

// Database is the subset of a Metabase database connection the provider exposes.
type Database struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	IsSample bool   `json:"is_sample"`
}

// ListDatabases returns every database connection.
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	var out struct {
		Data []Database `json:"data"`
	}
	if err := c.get(ctx, "/api/database", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
