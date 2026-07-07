# entry-node — the ENTRY role (mimicry front, aggressive rotation), fully
# provider-agnostic. It produces cloud-init + labels only; it never creates a VM
# and never references a cloud. The env wires this module's `user_data` output
# into a compute/<cloud> submodule. Keeping the role provider-free is what lets
# entry and exit live on DIFFERENT clouds and never share fate.

variable "name" {
  description = "Logical node name (also hostname)."
  type        = string
}

variable "ops_user" {
  description = "Non-root operations user created by cloud-init (Ansible target)."
  type        = string
  default     = "casper"
}

variable "ssh_pubkey" {
  description = "SSH public key authorized for the ops user."
  type        = string
}

variable "ephemeral" {
  description = "Marks this entry as disposable (its IP is rotated aggressively)."
  type        = bool
  default     = true
}

variable "extra_labels" {
  description = "Additional operational labels merged into the node labels."
  type        = map(string)
  default     = {}
}
