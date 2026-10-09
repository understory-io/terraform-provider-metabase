data "metabase_permissions_group" "admins" {
  name = "Administrators"
}

# Catch somebody being made an admin in the UI.
check "admins_are_declared" {
  assert {
    condition     = data.metabase_permissions_group.admins.member_user_ids == toset([for m in metabase_permissions_group_membership.admins : tostring(m.user_id)])
    error_message = "Administrators has members the configuration does not declare."
  }
}
