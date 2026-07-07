# exit-node — the EXIT role (egress to the open internet), fully provider-agnostic.
# The exit is deliberately DUMB and HIDDEN: it exposes no public transport
# listeners and accepts traffic ONLY from its paired entry's IP (enforced by the
# firewall role via allowed_entry_ip). Compromising or fingerprinting an entry
# therefore never reveals or reaches the exit — they can even live on different
# clouds. See [АНТИ-БЛОК] entry != exit.

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

variable "allowed_entry_ip" {
  description = "The paired entry node's public IP — the ONLY source allowed to reach this exit. Comes from the entry compute module output; never hardcoded."
  type        = string
}

variable "extra_labels" {
  description = "Additional operational labels merged into the node labels."
  type        = map(string)
  default     = {}
}
