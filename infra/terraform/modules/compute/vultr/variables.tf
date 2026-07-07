# compute/vultr — CANONICAL compute interface (see compute/hetzner/variables.tf).
# Same inputs everywhere; only the resource inside main.tf changes per cloud.

variable "name" {
  description = "Instance label / hostname."
  type        = string
}

variable "region" {
  description = "Vultr region id (e.g. fra, ams, waw)."
  type        = string
}

variable "size" {
  description = "Vultr plan id (e.g. vc2-1c-1gb)."
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
  description = "Free-form labels. No IP/mimicry data here."
  type        = map(string)
  default     = {}
}

variable "image" {
  description = "OS slug (canonical interface parity across clouds). Kept a variable — no hardcode."
  type        = string
  default     = "debian-12"
}

variable "image_os_name" {
  description = "Vultr OS display name resolved to os_id (Vultr has no slug lookup)."
  type        = string
  default     = "Debian 12 x64 (bookworm)"
}
