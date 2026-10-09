# Fail the plan's checks when a collection exists that the configuration does
# not declare (somebody made one in the UI).
data "metabase_collections" "live" {}

check "every_collection_is_declared" {
  assert {
    condition     = length(setsubtract(data.metabase_collections.live.ids, [for c in metabase_collection.all : c.id])) == 0
    error_message = "Collections exist that no metabase_collection declares: ${join(", ", setsubtract(data.metabase_collections.live.ids, [for c in metabase_collection.all : c.id]))}"
  }
}
