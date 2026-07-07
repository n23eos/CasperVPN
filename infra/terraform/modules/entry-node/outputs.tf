output "user_data" {
  description = "cloud-init user-data to feed into a compute/<cloud> submodule."
  value       = local.user_data
}

output "labels" {
  description = "Node labels (role=entry, ephemeral, ...). No IP/mimicry data."
  value       = local.labels
}

output "role" {
  description = "Fixed node role for this module."
  value       = "entry"
}

output "ansible_group" {
  description = "Inventory group the provisioned host should join."
  value       = "entry"
}
