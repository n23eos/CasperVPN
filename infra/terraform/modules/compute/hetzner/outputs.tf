# CANONICAL compute outputs — identical across every compute/<cloud> submodule.
output "id" {
  description = "Provider instance id."
  value       = hcloud_server.this.id
}

output "ipv4" {
  description = "Public IPv4 (advertised ingress for entry; egress for exit)."
  value       = hcloud_server.this.ipv4_address
}

output "ipv6" {
  description = "Public IPv6 (may be empty)."
  value       = hcloud_server.this.ipv6_address
}
