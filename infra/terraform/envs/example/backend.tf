# State backend is intentionally left to the operator (local by default). For a
# real fleet, configure a remote backend with locking, e.g.:
#
#   terraform {
#     backend "s3" {
#       bucket         = "..."      # from CI/secret manager, never committed
#       key            = "caspervpn/entry-exit/terraform.tfstate"
#       region         = "..."
#       dynamodb_table = "..."      # state locking
#     }
#   }
#
# Left empty here so `terraform validate` works offline in CI.
