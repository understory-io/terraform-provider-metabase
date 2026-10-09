package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// suffix keeps names unique when the acceptance tests run repeatedly against
// the same real instance, where group names must be unique.
func suffix() string { return strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36) }

func TestCollection_LifecycleMoveArchiveImport(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	cfg := func(childParent, archived, desc string) string {
		return env.providerConfig() + fmt.Sprintf(`
resource "metabase_collection" "a" {
  name = "tf-a-%[1]s"
}
resource "metabase_collection" "b" {
  name = "tf-b-%[1]s"
}
resource "metabase_collection" "child" {
  name        = "tf-child-%[1]s"
  description = %[4]s
  parent_id   = %[2]s
  archived    = %[3]s
}`, s, childParent, archived, desc)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("metabase_collection.a.id", "false", `"first"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("metabase_collection.child", "parent_id", "metabase_collection.a", "id"),
					resource.TestCheckResourceAttr("metabase_collection.child", "description", "first"),
					resource.TestCheckResourceAttr("metabase_collection.child", "archived", "false"),
					resource.TestCheckResourceAttrSet("metabase_collection.child", "entity_id"),
				),
			},
			// Move to another parent and drop the description.
			{
				Config: cfg("metabase_collection.b.id", "false", "null"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("metabase_collection.child", "parent_id", "metabase_collection.b", "id"),
					resource.TestCheckNoResourceAttr("metabase_collection.child", "description"),
				),
			},
			// To the root.
			{
				Config: cfg("null", "false", "null"),
				Check:  resource.TestCheckNoResourceAttr("metabase_collection.child", "parent_id"),
			},
			// Archive, and the next plan is empty (archived collections still read).
			{
				Config: cfg("null", "true", "null"),
				Check:  resource.TestCheckResourceAttr("metabase_collection.child", "archived", "true"),
			},
			{
				ResourceName:            "metabase_collection.a",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"prune"},
			},
			// Import by entity id resolves to the same numeric id.
			{
				ResourceName: "metabase_collection.a",
				ImportState:  true,
				ImportStateIdFunc: func(st *terraform.State) (string, error) {
					return st.RootModule().Resources["metabase_collection.a"].Primary.Attributes["entity_id"], nil
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if states[0].ID == "" || states[0].Attributes["name"] != "tf-a-"+s {
						return fmt.Errorf("import by entity id resolved to %v", states[0].Attributes)
					}
					return nil
				},
			},
		},
	})

	// prune defaults to false: destroy forgot the collections and left them live.
	cols, err := env.client.ListCollections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cols {
		if c.Name == "tf-a-"+s && !c.Archived {
			found = true
		}
	}
	if !found {
		t.Errorf("collection tf-a-%s should still exist unarchived after destroy with prune = false", s)
	}
}

func TestCollection_PruneArchivesOnDestroy(t *testing.T) {
	env := newTestEnv(t)
	name := "tf-prune-" + suffix()
	var id string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{{
			Config: env.providerConfig() + fmt.Sprintf(`
resource "metabase_collection" "p" {
  name  = %q
  prune = true
}`, name),
			Check: func(st *terraform.State) error {
				id = st.RootModule().Resources["metabase_collection.p"].Primary.ID
				return nil
			},
		}},
	})
	col, err := env.client.GetCollection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !col.Archived {
		t.Errorf("collection %s should be archived after destroy with prune = true", id)
	}
}

func TestCollectionPermissions_AuthoritativeAndDrift(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	cfg := func(access string) string {
		return env.providerConfig() + fmt.Sprintf(`
resource "metabase_permissions_group" "readers" {
  name = "tf-readers-%[1]s"
}
resource "metabase_permissions_group" "writers" {
  name = "tf-writers-%[1]s"
}
resource "metabase_collection" "c" {
  name = "tf-perm-%[1]s"
}
resource "metabase_collection_permissions" "c" {
  collection_id = metabase_collection.c.id
  access        = %[2]s
}`, s, access)
	}
	readers, writers := "tf-readers-"+s, "tf-writers-"+s

	var collectionID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{
			// All Users is left out, so the grant it inherited from the root at
			// creation is revoked: the resource is authoritative.
			{
				Config: cfg(`{ (metabase_permissions_group.readers.name) = "read", (metabase_permissions_group.writers.name) = "write" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access.%", "2"),
					resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access."+readers, "read"),
					resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access."+writers, "write"),
					func(st *terraform.State) error {
						collectionID = st.RootModule().Resources["metabase_collection.c"].Primary.ID
						g, err := metabase.New(env.url, env.key).GetCollectionGraph(context.Background())
						if err != nil {
							return err
						}
						if lvl := g.Access("1", collectionID); lvl != "none" {
							return fmt.Errorf("All Users should have none on %s, has %s", collectionID, lvl)
						}
						return nil
					},
				),
			},
			// Somebody grants All Users write in the UI: the plan shows it.
			{
				PreConfig: func() {
					err := env.client.ModifyCollectionGraph(context.Background(), func(*metabase.CollectionGraph) (map[string]map[string]string, error) {
						return map[string]map[string]string{"1": {collectionID: "write"}}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config:             cfg(`{ (metabase_permissions_group.readers.name) = "read", (metabase_permissions_group.writers.name) = "write" }`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Applying puts it back.
			{
				Config: cfg(`{ (metabase_permissions_group.readers.name) = "read", (metabase_permissions_group.writers.name) = "write" }`),
				Check:  resource.TestCheckNoResourceAttr("metabase_collection_permissions.c", "access.All Users"),
			},
			// Downgrade one, drop the other.
			{
				Config: cfg(`{ (metabase_permissions_group.writers.name) = "read" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access.%", "1"),
					resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access."+writers, "read"),
				),
			},
			// Revoke everything on purpose.
			{
				Config: cfg(`{}`),
				Check:  resource.TestCheckResourceAttr("metabase_collection_permissions.c", "access.%", "0"),
			},
			{
				ResourceName:      "metabase_collection_permissions.c",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestCollectionPermissions_RejectsAdminsAndUnknownGroups(t *testing.T) {
	env := newTestEnv(t)
	for _, tc := range []struct{ access, want string }{
		{`{ "Administrators" = "write" }`, "Administrators group"},
		{`{ "no-such-group-` + suffix() + `" = "read" }`, "no permissions group named"},
		{`{ "All Users" = "none" }`, "value must be one of"},
	} {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: protoFactories(),
			Steps: []resource.TestStep{{
				Config: env.providerConfig() + fmt.Sprintf(`
resource "metabase_collection_permissions" "root" {
  collection_id = "root"
  access        = %s
}`, tc.access),
				ExpectError: regexpMust(tc.want),
			}},
		})
	}
}

// Many permission resources applied in parallel all write the same revisioned
// graph. The client serialises and retries; none may fail with a 409 and
// none may undo another's cell.
func TestCollectionPermissions_ParallelWritesDoNotConflict(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{{
			Config: env.providerConfig() + fmt.Sprintf(`
resource "metabase_permissions_group" "g" {
  name = "tf-parallel-%[1]s"
}
resource "metabase_collection" "c" {
  count = 8
  name  = "tf-parallel-%[1]s-${count.index}"
}
resource "metabase_collection_permissions" "c" {
  count         = 8
  collection_id = metabase_collection.c[count.index].id
  access        = { (metabase_permissions_group.g.name) = count.index %% 2 == 0 ? "read" : "write" }
}`, s),
			Check: func(st *terraform.State) error {
				g, err := metabase.New(env.url, env.key).GetCollectionGraph(context.Background())
				if err != nil {
					return err
				}
				gid := st.RootModule().Resources["metabase_permissions_group.g"].Primary.ID
				for i := 0; i < 8; i++ {
					cid := st.RootModule().Resources[fmt.Sprintf("metabase_collection.c.%d", i)].Primary.ID
					want := map[bool]string{true: "read", false: "write"}[i%2 == 0]
					if got := g.Access(gid, cid); got != want {
						return fmt.Errorf("collection %s: want %s, got %s", cid, want, got)
					}
				}
				return nil
			},
		}},
	})
}

func TestGroupAndMembership(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	email := "tf-member-" + s + "@example.com"
	env.createUser(t, email)

	cfg := func(name string, member bool) string {
		c := env.providerConfig() + fmt.Sprintf(`
data "metabase_user" "u" {
  email = %q
}
resource "metabase_permissions_group" "g" {
  name = %q
}
data "metabase_permissions_group" "g" {
  name       = metabase_permissions_group.g.name
  depends_on = [metabase_permissions_group_membership.m]
}
`, email, name)
		if member {
			c += `
resource "metabase_permissions_group_membership" "m" {
  group_id = metabase_permissions_group.g.id
  user_id  = data.metabase_user.u.id
}`
		} else {
			c += `
resource "metabase_permissions_group_membership" "m" {
  count    = 0
  group_id = metabase_permissions_group.g.id
  user_id  = data.metabase_user.u.id
}`
		}
		return c
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("tf-group-"+s, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metabase_permissions_group_membership.m", "is_group_manager", "false"),
					resource.TestCheckResourceAttrPair("data.metabase_permissions_group.g", "id", "metabase_permissions_group.g", "id"),
					resource.TestCheckResourceAttr("data.metabase_permissions_group.g", "member_user_ids.#", "1"),
					resource.TestCheckTypeSetElemAttrPair("data.metabase_permissions_group.g", "member_user_ids.*", "data.metabase_user.u", "id"),
				),
			},
			{
				ResourceName:      "metabase_permissions_group_membership.m",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:            "metabase_permissions_group.g",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"prune"},
			},
			// Rename in place; membership survives.
			{
				Config: cfg("tf-group-renamed-"+s, true),
				Check:  resource.TestCheckResourceAttr("metabase_permissions_group.g", "name", "tf-group-renamed-"+s),
			},
			// Removing the membership resource removes the user from the group.
			{
				Config: cfg("tf-group-renamed-"+s, false),
				Check:  resource.TestCheckResourceAttr("data.metabase_permissions_group.g", "member_user_ids.#", "0"),
			},
		},
	})
}

func TestGroup_ImportRefusesBuiltIn(t *testing.T) {
	env := newTestEnv(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{{
			Config:        env.providerConfig() + `resource "metabase_permissions_group" "admins" { name = "Administrators" }`,
			ResourceName:  "metabase_permissions_group.admins",
			ImportState:   true,
			ImportStateId: "2",
			ExpectError:   regexpMust("built-in group"),
		}},
	})
}

func TestDatabasePermissions_DefaultsDriftAndImport(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	cfg := func(body string) string {
		return env.providerConfig() + fmt.Sprintf(`
data "metabase_database" "sample" {
  name = "Sample Database"
}
resource "metabase_permissions_group" "g" {
  name = "tf-data-%s"
}
resource "metabase_database_permissions" "g" {
  group_id    = metabase_permissions_group.g.id
  database_id = data.metabase_database.sample.id
  %s
}`, s, body)
	}
	var gid, dbid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{
			// Everything unset falls back to the lowest level: a new group
			// starts with native SQL and full downloads, and loses both here.
			{
				Config: cfg(`view_data = "unrestricted"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metabase_database_permissions.g", "create_queries", "no"),
					resource.TestCheckResourceAttr("metabase_database_permissions.g", "download", "none"),
					resource.TestCheckResourceAttr("metabase_database_permissions.g", "data_model", "none"),
					func(st *terraform.State) error {
						r := st.RootModule().Resources["metabase_database_permissions.g"].Primary.Attributes
						gid, dbid = r["group_id"], r["database_id"]
						g, err := metabase.New(env.url, env.key).GetDataGraph(context.Background())
						if err != nil {
							return err
						}
						if _, ok := g.Groups[gid][dbid]["create-queries"]; ok {
							return fmt.Errorf("create-queries should be absent (no) in the graph, cell is %v", g.Groups[gid][dbid])
						}
						return nil
					},
				),
			},
			{
				Config: cfg(`
  view_data      = "unrestricted"
  create_queries = "query-builder"
  download       = "full"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("metabase_database_permissions.g", "create_queries", "query-builder"),
					resource.TestCheckResourceAttr("metabase_database_permissions.g", "download", "full"),
				),
			},
			// Native SQL granted by hand is drift.
			{
				PreConfig: func() {
					err := env.client.ModifyDataGraph(context.Background(), func(*metabase.DataGraph) (map[string]map[string]map[string]any, error) {
						return map[string]map[string]map[string]any{gid: {dbid: {"create-queries": "query-builder-and-native"}}}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg(`
  view_data      = "unrestricted"
  create_queries = "query-builder"
  download       = "full"`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(`
  view_data      = "unrestricted"
  create_queries = "query-builder"
  download       = "full"`),
			},
			{
				ResourceName:      "metabase_database_permissions.g",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestDatabasePermissions_RejectsAdmins(t *testing.T) {
	env := newTestEnv(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{{
			Config: env.providerConfig() + `
data "metabase_permissions_group" "admins" {
  name = "Administrators"
}
data "metabase_database" "sample" {
  name = "Sample Database"
}
resource "metabase_database_permissions" "admins" {
  group_id    = data.metabase_permissions_group.admins.id
  database_id = data.metabase_database.sample.id
  view_data   = "unrestricted"
}`,
			ExpectError: regexpMust("Administrators group"),
		}},
	})
}

func TestCollectionsDataSource_ListsSharedCollections(t *testing.T) {
	env := newTestEnv(t)
	s := suffix()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoFactories(),
		Steps: []resource.TestStep{{
			Config: env.providerConfig() + fmt.Sprintf(`
resource "metabase_collection" "x" {
  name = "tf-ds-%s"
}
data "metabase_collections" "all" {
  depends_on = [metabase_collection.x]
}
data "metabase_collection" "x" {
  entity_id = metabase_collection.x.entity_id
}
data "metabase_permissions_groups" "all" {}`, s),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemAttrPair("data.metabase_collections.all", "ids.*", "metabase_collection.x", "id"),
				resource.TestCheckResourceAttrPair("data.metabase_collection.x", "id", "metabase_collection.x", "id"),
				resource.TestCheckResourceAttr("data.metabase_collection.x", "name", "tf-ds-"+s),
				resource.TestCheckTypeSetElemAttr("data.metabase_permissions_groups.all", "names.*", "Administrators"),
				resource.TestCheckTypeSetElemAttr("data.metabase_permissions_groups.all", "names.*", "All Users"),
			),
		}},
	})
}

// createUser makes a password user, as an admin would in the UI. The provider
// itself never creates users.
func (e *testEnv) createUser(t *testing.T, email string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"first_name": "Terraform", "last_name": "Member", "email": email})
	req, _ := http.NewRequest(http.MethodPost, e.url+"/api/user", bytes.NewReader(body))
	req.Header.Set("X-Api-Key", e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		t.Fatalf("create user: %s", resp.Status)
	}
}
