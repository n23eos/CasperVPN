# Reference env: one ENTRY (Hetzner) + one EXIT (Vultr) — DIFFERENT clouds on
# purpose, to make the entry != exit separation concrete. Swap either
# compute/<cloud> source to move a role to another provider; the role modules
# and the rest of this file do not change. NOT applied in CI (validate/plan only).
#
# Auth comes from provider env vars, never from code:
#   export HCLOUD_TOKEN=...      # entry (Hetzner)
#   export VULTR_API_KEY=...     # exit (Vultr)

provider "hcloud" {}

provider "vultr" {
  # api_key is a required provider argument (unlike hcloud's optional token), so
  # validate needs a definition even though the value comes from the environment
  # (TF_VAR_vultr_api_key / VULTR_API_KEY). Default is empty — never a secret in code.
  api_key     = var.vultr_api_key
  rate_limit  = 700
  retry_limit = 3
}

# --- ENTRY (role config -> Hetzner VM) ------------------------------------
module "entry_cfg" {
  source     = "../../modules/entry-node"
  name       = "${var.node_name}-entry"
  ssh_pubkey = var.ssh_pubkey
  ephemeral  = true
}

module "entry_vm" {
  source     = "../../modules/compute/hetzner"
  name       = "${var.node_name}-entry"
  region     = var.entry_region
  size       = var.entry_size
  ssh_pubkey = var.ssh_pubkey
  user_data  = module.entry_cfg.user_data
  tags       = module.entry_cfg.labels
}

# --- EXIT (role config -> Vultr VM) ---------------------------------------
# allowed_entry_ip is the entry's real IP, threaded in at plan time — the exit
# firewall will accept ingress from nowhere else. No IP is ever hardcoded.
module "exit_cfg" {
  source           = "../../modules/exit-node"
  name             = "${var.node_name}-exit"
  ssh_pubkey       = var.ssh_pubkey
  allowed_entry_ip = module.entry_vm.ipv4
}

module "exit_vm" {
  source     = "../../modules/compute/vultr"
  name       = "${var.node_name}-exit"
  region     = var.exit_region
  size       = var.exit_size
  ssh_pubkey = var.ssh_pubkey
  user_data  = module.exit_cfg.user_data
  tags       = module.exit_cfg.labels
}
