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
