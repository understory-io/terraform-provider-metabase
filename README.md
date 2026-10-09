# terraform-provider-metabase

A small, private Terraform provider for **who can see what** in Metabase:
collections, permission groups, group memberships, and the collection and data
permission graphs. Used by
[`infrastructure-metabase`](https://github.com/understory-io/infrastructure-metabase);
published to the **TFC private registry** as `understory/metabase`, the same
way as
[`terraform-provider-hubspot`](https://github.com/understory-io/terraform-provider-hubspot).

Dashboards and cards are not here. They are content, and live in
[`canopy-metabase`](https://github.com/understory-io/canopy-metabase).

## Consuming the provider

```hcl
terraform {
  required_providers {
    metabase = {
      source  = "app.terraform.io/understory/metabase"
      version = "~> 0.1"
    }
  }
}

provider "metabase" {
  url     = "https://metabase.understory.sh"
  api_key = var.metabase_api_key
}
```

`url` and `api_key` fall back to `METABASE_URL` and `METABASE_API_KEY`. A
Metabase API key acts as the group it was created in, so the key must be an
**Administrators** key to write permissions. Give Terraform a key of its own.

## Resources and data sources

| | Manages | Import id | On destroy |
|---|---|---|---|
| `metabase_collection` | name, description, parent, archived | numeric id or entity id | forgets it; `prune = true` archives it |
| `metabase_permissions_group` | name | group id | forgets it; `prune = true` deletes it |
| `metabase_permissions_group_membership` | one user in one group (+ `is_group_manager`) | `<group_id>:<user_id>` | removes the user from the group |
| `metabase_collection_permissions` | one collection's column of the collection graph | collection id or `root` | forgets it (set `access = {}` to revoke) |
| `metabase_database_permissions` | one (group, database) cell of the data graph | `<group_id>:<database_id>` | forgets it |

| Data source | Looks up |
|---|---|
| `metabase_user` | a user by email → id (users are never created or deactivated here) |
| `metabase_database` | a database connection by name → id |
| `metabase_collection` | a collection by id or entity id |
| `metabase_collections` | every shared collection, for an "is everything declared?" `check` |
| `metabase_permissions_group` | a group by name (built-in ones too) and its active human members |
| `metabase_permissions_groups` | every group name, for an "is every group declared?" `check` |

Examples for each are under [`examples/`](examples).

## How the graphs are modelled

Metabase stores permissions as two revisioned graphs, `/api/collection/graph`
(group × collection → `read` | `write` | `none`) and `/api/permissions/graph`
(group × database → view data, create queries, download, …). The provider
cuts them so a plan reads as an answer to "who can see this?":

- **`metabase_collection_permissions` is one resource per collection**, with
  `access` a map of **group name** to `read` or `write`. It is authoritative
  for that collection: a group that is not listed has no access, so a grant
  added in the UI is drift, and a plan shows it as
  `~ access = { - "All Users" = "write" }`. Administrators always have `write`
  and are refused if listed.
- **`metabase_database_permissions` is one resource per (group, database)**,
  authoritative for that cell: a key left unset means the lowest level
  (`create_queries = "no"`, `download = "none"`, …), which is also what
  Metabase stores when a key is absent. Per-schema and per-table permissions
  are not modelled; a granular cell is reported as an error rather than
  flattened silently.

Why not one resource per (group, collection) cell? Because then a grant made in
the UI on a pair nobody declared is invisible to Terraform. Why not one
resource for the whole graph? Because every change to it would be one
2,000-line diff, and one conflict would block every unrelated change.

### Revisions

Every graph write names the revision it was computed from, and Metabase
answers **409** if the graph moved on in between. Both `PUT` endpoints merge,
writing only the cells in the request. So each write is:

1. read the graph (fresh, bypassing the client's per-run memo);
2. compute only the cells that differ from what the resource wants;
3. `PUT` those, with that graph's revision;
4. on 409, start again from 1 (up to 5 times).

A concurrent change to another collection or group is never overwritten, and
one to the same cell is re-read rather than clobbered. `force=true` is never
sent. Within one Terraform run the graph writes are also serialised with a
mutex, since Terraform applies resources in parallel and they would otherwise
make each other retry.

**Collection writes are serialised with the graph writes too.** Creating or
moving a collection writes a collection-graph revision (the new collection
copies its parent's grants), and Metabase 0.64 does not serialise those
inserts: two concurrent `POST /api/collection` calls fail with a 500 on the
revision table's primary key. The acceptance tests found this; the in-memory
fake cannot show it.

## Lifecycle: conservative by default

Following `hubspot_property`'s `prune` flag: removing a collection or group
from the configuration does not delete anything in Metabase unless
`prune = true` is set. Permission resources never revoke on destroy;
revoking is `access = {}` or a lower level, written on purpose. Memberships
are the exception: removing one removes the user from the group, because that
is the only thing removing it could mean.

Users are **not** managed. With Google sign-in, Metabase creates an account the
first time someone signs in, and deactivating one is destructive. Memberships
resolve people with `data "metabase_user"` by email.

## Enterprise features

Developed and tested against the open-source edition (0.64). On it:
`is_group_manager` is always `false`, `view_data` can only be `unrestricted`
(Metabase answers 402 to `blocked`), and sandboxing does not exist. The
schema already accepts the Enterprise `view_data` values; sandboxes are not
modelled.

## Development

```sh
mise install
mise run ci                       # tests (in-memory fake), vet, lint, vuln, coverage, build
eval "$(mise run -q acc:up)"      # a real Metabase 0.64 in docker, plus an admin API key
mise run test:acc                 # the same tests against it
```

The resource tests in `internal/provider` run the full plan/apply/import/drift
cycle. By default they hit `fakeMetabase`, an in-memory model of the endpoints
the provider uses, including the revision checks and the merge semantics of
both graphs. With `TF_ACC=1` they hit the Metabase in `METABASE_URL` instead;
they assert on behaviour, never on ids, so the same steps pass on both. CI runs
both: `ci` and `acceptance` in `.github/workflows/ci.yml`.

`scripts/acc-metabase.sh` starts `metabase/metabase:v0.64.0.4-beta` as
`mb-tfacc` on port 3999, completes setup, and mints a key. `docker rm -f
mb-tfacc` throws it away.

## Releasing

Exactly as in `terraform-provider-hubspot`: a conventional commit on `main` →
release-please PR → merge → tag `vX.Y.Z` → goreleaser builds and signs with the
org's provider-signing GPG key → `scripts/tfc-upload-version.sh` uploads the
version to the `understory` private registry. Tags pushed by release-please's
`GITHUB_TOKEN` do not trigger the goreleaser job, so push the tag by hand:

```sh
git push origin refs/tags/vX.Y.Z
```

The repository needs the secrets `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE` and
`TFC_TOKEN`, the same as the hubspot provider's. The key is the existing org
key; do not mint a second one.
