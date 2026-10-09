terraform {
  required_providers {
    metabase = {
      source = "registry.terraform.io/understory-io/metabase"
    }
  }
}

# Reads METABASE_URL and METABASE_API_KEY from the environment. The key must
# belong to the Administrators group to write permissions.
provider "metabase" {}
