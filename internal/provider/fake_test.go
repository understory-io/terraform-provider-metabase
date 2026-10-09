package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// The resource tests run against one of two Metabases:
//
//   - by default, fakeMetabase below: an in-memory model of the endpoints the
//     provider uses, including the revision checks and the merge semantics of
//     both permission graphs, so `go test ./...` needs no credentials;
//   - with TF_ACC=1, METABASE_URL and METABASE_API_KEY set (see
//     scripts/acc-metabase.sh), a real Metabase. The tests are written against
//     behaviour, never against ids the fake happens to assign, so the same
//     steps pass on both.

type testEnv struct {
	url, key string
	client   *metabase.Client
	fake     *fakeMetabase
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	if os.Getenv("TF_ACC") != "" {
		url, key := os.Getenv("METABASE_URL"), os.Getenv("METABASE_API_KEY")
		if url == "" || key == "" {
			t.Fatal("TF_ACC is set but METABASE_URL / METABASE_API_KEY are not; run scripts/acc-metabase.sh")
		}
		return &testEnv{url: url, key: key, client: metabase.New(url, key)}
	}
	f := newFakeMetabase()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &testEnv{url: srv.URL, key: "test-key", client: metabase.New(srv.URL, "test-key"), fake: f}
}

func (e *testEnv) providerConfig() string {
	return fmt.Sprintf(`
provider "metabase" {
  url     = %q
  api_key = %q
}
`, e.url, e.key)
}

func protoFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"metabase": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// fakeMetabase models the Metabase REST API surface the provider uses.
type fakeMetabase struct {
	mu          sync.Mutex
	nextID      int
	collections map[int]*fakeCollection
	groups      map[int]*metabase.Group
	members     map[int]*metabase.Membership
	users       map[int]*metabase.User
	collRev     int
	coll        map[string]map[string]string // group -> collection -> level
	dataRev     int
	data        map[string]map[string]map[string]any

	// collWrites counts PUTs to the collection graph, so a test can tell a
	// no-op apply from one that wrote.
	collWrites int
}

type fakeCollection struct {
	metabase.Collection
}

func newFakeMetabase() *fakeMetabase {
	admin, all := metabase.MagicAdmin, metabase.MagicAllUsers
	f := &fakeMetabase{
		nextID:      100,
		collections: map[int]*fakeCollection{},
		groups: map[int]*metabase.Group{
			1: {ID: 1, Name: "All Users", MagicGroupType: &all},
			2: {ID: 2, Name: "Administrators", MagicGroupType: &admin},
		},
		members: map[int]*metabase.Membership{},
		users:   map[int]*metabase.User{1: {ID: 1, Email: "admin@example.com", IsActive: true, IsSuperuser: true}},
		coll:    map[string]map[string]string{"1": {"root": "write"}, "2": {"root": "write"}},
		data: map[string]map[string]map[string]any{
			"1": {"1": {"view-data": "unrestricted", "create-queries": "query-builder-and-native", "download": map[string]any{"schemas": "full"}}},
			"2": {"1": {"view-data": "unrestricted", "create-queries": "query-builder-and-native", "download": map[string]any{"schemas": "full"}}},
		},
	}
	f.members[1] = &metabase.Membership{MembershipID: 1, GroupID: 1, UserID: 1}
	f.members[2] = &metabase.Membership{MembershipID: 2, GroupID: 2, UserID: 1}
	return f
}

func (f *fakeMetabase) id() int { f.nextID++; return f.nextID }

func (f *fakeMetabase) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") == "" {
		http.Error(w, "Unauthenticated", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	p := strings.TrimSuffix(r.URL.Path, "/")
	seg := strings.Split(strings.TrimPrefix(p, "/api/"), "/")
	route := r.Method + " " + p

	switch {
	case route == "GET /api/collection/graph":
		writeJSON(w, map[string]any{"revision": f.collRev, "groups": f.coll})
	case route == "PUT /api/collection/graph":
		f.putCollGraph(w, body)
	case route == "GET /api/permissions/graph":
		writeJSON(w, map[string]any{"revision": f.dataRev, "groups": f.data})
	case route == "PUT /api/permissions/graph":
		f.putDataGraph(w, body)

	case route == "GET /api/collection":
		archived := r.URL.Query().Get("archived") == "true"
		out := []any{}
		if !archived {
			out = append(out, map[string]any{"id": "root", "name": "Our analytics"})
		}
		for _, id := range sortedKeys(f.collections) {
			if c := f.collections[id]; c.Archived == archived {
				out = append(out, c.Collection)
			}
		}
		writeJSON(w, out)
	case route == "POST /api/collection":
		f.createCollection(w, body)
	case len(seg) == 2 && seg[0] == "collection":
		c := f.findCollection(seg[1])
		if c == nil {
			http.Error(w, "Not found.", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut {
			f.updateCollection(c, body)
		}
		writeJSON(w, c.Collection)

	case route == "GET /api/permissions/group":
		out := []metabase.Group{}
		for _, id := range sortedKeys(f.groups) {
			out = append(out, *f.groups[id])
		}
		writeJSON(w, out)
	case route == "POST /api/permissions/group":
		g := &metabase.Group{ID: f.id(), Name: body["name"].(string)}
		f.groups[g.ID] = g
		gid := strconv.Itoa(g.ID)
		// Like Metabase: a new group starts with All Users' data access and
		// no collection access.
		f.data[gid] = map[string]map[string]any{}
		for db, cell := range f.data["1"] {
			f.data[gid][db] = copyCell(cell)
		}
		f.dataRev++
		writeJSON(w, g)
	case len(seg) == 3 && seg[0] == "permissions" && seg[1] == "group":
		id, _ := strconv.Atoi(seg[2])
		g, ok := f.groups[id]
		if !ok {
			http.Error(w, "Not found.", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			g.Name = body["name"].(string)
		case http.MethodDelete:
			delete(f.groups, id)
			delete(f.coll, seg[2])
			delete(f.data, seg[2])
			for mid, m := range f.members {
				if m.GroupID == id {
					delete(f.members, mid)
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, g)

	case route == "GET /api/permissions/membership":
		out := map[string][]metabase.Membership{}
		for _, m := range f.members {
			k := strconv.Itoa(m.UserID)
			out[k] = append(out[k], *m)
		}
		writeJSON(w, out)
	case route == "POST /api/permissions/membership":
		m := &metabase.Membership{MembershipID: f.id(), GroupID: int(body["group_id"].(float64)), UserID: int(body["user_id"].(float64))}
		if v, ok := body["is_group_manager"].(bool); ok {
			m.IsGroupManager = v
		}
		f.members[m.MembershipID] = m
		writeJSON(w, []any{})
	case len(seg) == 3 && seg[0] == "permissions" && seg[1] == "membership":
		id, _ := strconv.Atoi(seg[2])
		m, ok := f.members[id]
		if !ok {
			http.Error(w, "Not found.", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.members, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		m.IsGroupManager = body["is_group_manager"].(bool)
		writeJSON(w, m)

	case route == "GET /api/user":
		out := []metabase.User{}
		for _, id := range sortedKeys(f.users) {
			out = append(out, *f.users[id])
		}
		writeJSON(w, map[string]any{"data": out})
	case route == "POST /api/user":
		u := &metabase.User{ID: f.id(), Email: body["email"].(string), IsActive: true}
		f.users[u.ID] = u
		m := &metabase.Membership{MembershipID: f.id(), GroupID: 1, UserID: u.ID}
		f.members[m.MembershipID] = m
		writeJSON(w, u)
	case route == "GET /api/database":
		writeJSON(w, map[string]any{"data": []metabase.Database{{ID: 1, Name: "Sample Database", Engine: "h2", IsSample: true}}})

	default:
		http.Error(w, "fake: no route for "+route, http.StatusNotFound)
	}
}

func (f *fakeMetabase) findCollection(ref string) *fakeCollection {
	if id, err := strconv.Atoi(ref); err == nil {
		return f.collections[id]
	}
	for _, c := range f.collections {
		if c.EntityID == ref {
			return c
		}
	}
	return nil
}

func (f *fakeMetabase) createCollection(w http.ResponseWriter, body map[string]any) {
	c := &fakeCollection{metabase.Collection{ID: f.id(), Name: body["name"].(string), Location: "/"}}
	c.EntityID = fmt.Sprintf("fakeEntityId%09d", c.ID)
	if d, ok := body["description"].(string); ok {
		c.Description = &d
	}
	parentKey := "root"
	if p, ok := body["parent_id"].(float64); ok {
		parent := f.collections[int(p)]
		c.Location = parent.Location + strconv.Itoa(parent.ID) + "/"
		parentKey = strconv.Itoa(parent.ID)
	}
	f.collections[c.ID] = c
	// Like Metabase: a new collection copies its parent's grants, and that is
	// a graph change, so the revision moves.
	for _, cells := range f.coll {
		if lvl, ok := cells[parentKey]; ok {
			cells[strconv.Itoa(c.ID)] = lvl
		}
	}
	f.collRev++
	writeJSON(w, c.Collection)
}

func (f *fakeMetabase) updateCollection(c *fakeCollection, body map[string]any) {
	if v, ok := body["name"].(string); ok {
		c.Name = v
	}
	if v, ok := body["description"]; ok {
		if s, isStr := v.(string); isStr {
			c.Description = &s
		} else {
			c.Description = nil
		}
	}
	if v, ok := body["parent_id"]; ok {
		if p, isNum := v.(float64); isNum {
			parent := f.collections[int(p)]
			c.Location = parent.Location + strconv.Itoa(parent.ID) + "/"
		} else {
			c.Location = "/"
		}
	}
	if v, ok := body["archived"].(bool); ok {
		c.Archived = v
	}
}

func (f *fakeMetabase) putCollGraph(w http.ResponseWriter, body map[string]any) {
	if int(body["revision"].(float64)) != f.collRev {
		http.Error(w, "Looks like someone else edited the permissions and your data is out of date.", http.StatusConflict)
		return
	}
	groups := body["groups"].(map[string]any)
	if _, ok := groups["2"]; ok {
		http.Error(w, "You cannot create or revoke permissions for the 'Admin' group.", http.StatusBadRequest)
		return
	}
	for gid, cells := range groups {
		if f.coll[gid] == nil {
			f.coll[gid] = map[string]string{}
		}
		for col, lvl := range cells.(map[string]any) {
			if lvl == "none" {
				delete(f.coll[gid], col)
			} else {
				f.coll[gid][col] = lvl.(string)
			}
		}
	}
	f.collRev++
	f.collWrites++
	writeJSON(w, map[string]any{"revision": f.collRev})
}

// lowest is what Metabase stores as an absent key.
var lowest = map[string]string{"create-queries": "no", "download": "none", "data-model": "none", "details": "no", "transforms": "no"}

func (f *fakeMetabase) putDataGraph(w http.ResponseWriter, body map[string]any) {
	if int(body["revision"].(float64)) != f.dataRev {
		http.Error(w, "Looks like someone else edited the permissions and your data is out of date.", http.StatusConflict)
		return
	}
	for gid, dbs := range body["groups"].(map[string]any) {
		if gid == "2" {
			http.Error(w, "You cannot create or revoke permissions for the 'Admin' group.", http.StatusBadRequest)
			return
		}
		if f.data[gid] == nil {
			f.data[gid] = map[string]map[string]any{}
		}
		for db, keys := range dbs.(map[string]any) {
			if f.data[gid][db] == nil {
				f.data[gid][db] = map[string]any{}
			}
			for k, v := range keys.(map[string]any) {
				s := v
				if obj, ok := v.(map[string]any); ok {
					s = obj["schemas"]
				}
				if s == lowest[k] {
					delete(f.data[gid][db], k)
				} else {
					f.data[gid][db][k] = v
				}
			}
		}
	}
	f.dataRev++
	writeJSON(w, map[string]any{"revision": f.dataRev})
}

func copyCell(c map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range c {
		out[k] = v
	}
	return out
}

func sortedKeys[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
