# compute/hetzner — one instance behind the CANONICAL compute interface.
# Every compute/<cloud> submodule MUST expose exactly these inputs and the
# outputs in outputs.tf, so entry-node/exit-node role modules stay
# provider-agnostic and adding a cloud is "copy the folder, swap the resource".

variable "name" {
  description = "Instance name / hostname label."
  type        = string
}

variable "region" {
  description = "Cloud location (provider-specific slug, e.g. hetzner: nbg1/fsn1/hel1)."
  type        = string
}

variable "size" {
  description = "Instance type / server plan (provider-specific, e.g. hetzner: cx22)."
  type        = string
}

variable "ssh_pubkey" {
  description = "SSH public key material authorized for the ops user."
  type        = string
}

variable "user_data" {
  description = "cloud-init user-data produced by the entry-node/exit-node role module."
  type        = string
}

variable "tags" {
  description = "Free-form labels (role, wave, ephemeral, ...). No IP/mimicry data here."
  type        = map(string)
  default     = {}
}

variable "image" {
  description = "Base OS image slug. Kept a variable so nothing is hardcoded."
  type        = string
  default     = "debian-12"
}
