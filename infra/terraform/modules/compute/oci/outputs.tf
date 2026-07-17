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
  # OCI exposes IPv6 on the VNIC, not the instance, and this module does not wire
  # a VNIC data source, so IPv6 is reported empty here (matches the other clouds'
  # "empty unless enabled" contract). Wire a data.oci_core_vnic lookup to populate.
  description = "Public IPv6 (empty — OCI IPv6 lives on the VNIC, not wired here)."
  value       = ""
}
