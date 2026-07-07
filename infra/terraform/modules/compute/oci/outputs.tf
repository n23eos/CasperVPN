# CANONICAL compute outputs — identical across every compute/<cloud> submodule.
output "id" {
  description = "Provider instance OCID."
  value       = oci_core_instance.this.id
}

output "ipv4" {
  description = "Public IPv4."
  value       = oci_core_instance.this.public_ip
}

output "ipv6" {
  description = "Public IPv6 (empty unless the subnet is IPv6-enabled)."
  value       = try(oci_core_instance.this.ipv6private_ip_address, "")
}
