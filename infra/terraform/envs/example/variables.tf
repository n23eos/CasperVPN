variable "node_name" {
  description = "Base name for this entry/exit pair."
  type        = string
  default     = "casper-demo"
}

variable "ssh_pubkey" {
  description = "SSH public key authorized on both nodes (ops user)."
  type        = string
}

variable "entry_region" {
  description = "Hetzner location for the entry (e.g. nbg1, fsn1, hel1)."
  type        = string
  default     = "hel1"
}

variable "entry_size" {
  description = "Hetzner server type for the entry."
  type        = string
  default     = "cx22"
}

variable "exit_region" {
  description = "Vultr region for the exit (e.g. fra, ams, waw)."
  type        = string
  default     = "waw"
}

variable "exit_size" {
  description = "Vultr plan for the exit."
  type        = string
  default     = "vc2-1c-1gb"
}
