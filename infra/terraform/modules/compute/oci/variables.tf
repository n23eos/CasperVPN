# compute/oci — CANONICAL compute interface (name/region/size/ssh_pubkey/
# user_data/tags/image) PLUS Oracle networking prerequisites as variables.
# Oracle Cloud is a mainstream provider whose address space is not a bulk-listed
# VPN pool — good mimicry surface, generous free tier. Nothing hardcoded.

variable "name" {
  description = "Instance display name / hostname."
  type        = string
}

variable "region" {
  description = "OCI region (informational; auth/region come from the provider block in the env)."
  type        = string
  default     = ""
}

variable "size" {
  description = "OCI shape (e.g. VM.Standard.A1.Flex, VM.Standard.E2.1.Micro)."
  type        = string
}

variable "ssh_pubkey" {
  description = "SSH public key material authorized for the ops user."
  type        = string
}

variable "user_data" {
  description = "cloud-init user-data from the entry-node/exit-node role module."
  type        = string
}

variable "tags" {
  description = "Free-form freeform_tags. No IP/mimicry data here."
  type        = map(string)
  default     = {}
}

variable "image" {
  description = "Canonical interface parity; OCI selects by image_ocid below."
  type        = string
  default     = "debian-12"
}

# --- OCI-specific prerequisites (existing VCN/subnet expected) ---
variable "compartment_ocid" {
  description = "Target compartment OCID."
  type        = string
}

variable "subnet_ocid" {
  description = "Existing public subnet OCID for the instance VNIC."
  type        = string
}

variable "availability_domain" {
  description = "Availability domain name (e.g. 'Uocm:EU-FRANKFURT-1-AD-1')."
  type        = string
}

variable "image_ocid" {
  description = "Boot image OCID for the chosen region/shape."
  type        = string
}

variable "flex_ocpus" {
  description = "OCPUs for flex shapes (ignored by fixed shapes)."
  type        = number
  default     = 1
}

variable "flex_memory_gbs" {
  description = "Memory (GB) for flex shapes (ignored by fixed shapes)."
  type        = number
  default     = 6
}
