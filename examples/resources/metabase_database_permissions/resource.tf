data "metabase_database" "verified" {
  name = "canopy_verified"
}

# Unset keys fall back to the lowest level (create_queries = "no",
# download = "none", ...), so this block is the whole truth for the cell.
resource "metabase_database_permissions" "finance_verified" {
  group_id       = metabase_permissions_group.finance.id
  database_id    = data.metabase_database.verified.id
  view_data      = "unrestricted"
  create_queries = "query-builder"
  download       = "full"
}
