output "entry_ip" {
  description = "Public IPv4 of the entry (advertised ingress; rotated aggressively)."
  value       = module.entry_vm.ipv4
}

output "entry_id" {
  description = "Provider instance id of the entry."
  value       = module.entry_vm.id
}

output "exit_ip" {
  description = "Public IPv4 of the exit (hidden; reachable only from entry_ip)."
  value       = module.exit_vm.ipv4
}

output "exit_id" {
  description = "Provider instance id of the exit."
  value       = module.exit_vm.id
}

output "entry_region" {
  description = "Region of the entry (for the control-plane node record)."
  value       = var.entry_region
}

output "exit_region" {
  description = "Region of the exit (for the control-plane node record)."
  value       = var.exit_region
}

output "entry_cloud" {
  description = "Cloud of the entry in this reference env (Hetzner)."
  value       = "hetzner"
}

output "exit_cloud" {
  description = "Cloud of the exit in this reference env (Vultr)."
  value       = "vultr"
}
