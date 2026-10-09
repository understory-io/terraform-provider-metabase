data "metabase_user" "alex" {
  email = "alex@example.com"
}

resource "metabase_permissions_group_membership" "alex_finance" {
  group_id = metabase_permissions_group.finance.id
  user_id  = data.metabase_user.alex.id
}
