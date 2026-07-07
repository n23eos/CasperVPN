# CANONICAL compute outputs — identical across every compute/<cloud> submodule.
output "id" {
  description = "Provider instance id."
  value       = vultr_instance.this.id
}

output "ipv4" {
  description = "Public IPv4."
  value       = vultr_instance.this.main_ip
}

output "ipv6" {
  description = "Public IPv6 (empty unless enabled on the plan/region)."
  value       = vultr_instance.this.v6_main_ip
}
