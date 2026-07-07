# Shared Terraform/provider version constraints for the CasperVPN fleet.
# Root envs and modules inherit these floors. Provider *sources* are declared
# per compute submodule so an env only pulls the clouds it actually uses.
terraform {
  required_version = ">= 1.5.0"
}
