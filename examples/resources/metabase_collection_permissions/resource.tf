# Authoritative: any group not listed has no access to this collection.
# Administrators always have write and are never listed.
resource "metabase_collection_permissions" "commission" {
  collection_id = metabase_collection.commission.id
  access = {
    (metabase_permissions_group.finance.name) = "write"
    "C-level"                                 = "write"
  }
}

resource "metabase_collection_permissions" "root" {
  collection_id = "root"
  access = {
    "All Users" = "read"
  }
}
