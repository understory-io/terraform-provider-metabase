resource "metabase_collection" "projects" {
  name = "Projects"
}

resource "metabase_collection" "commission" {
  name        = "Commission overviews"
  description = "Commission statements. Owners and the CS lead only."
  parent_id   = metabase_collection.projects.id
}
