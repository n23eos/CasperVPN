# Hetzner Cloud instance. General-purpose cloud — its address space is NOT a
# bulk-listed VPN pool, which is the point (see docs/mimicry-domains.md rationale
# and architecture.md: multi-cloud anti-fragility). Credentials come from the
# HCLOUD_TOKEN env var, never from code.

resource "hcloud_ssh_key" "this" {
  name       = var.name
  public_key = var.ssh_pubkey
}

resource "hcloud_server" "this" {
  name        = var.name
  server_type = var.size
  image       = var.image
  location    = var.region
  user_data   = var.user_data
  ssh_keys    = [hcloud_ssh_key.this.id]
  labels      = var.tags

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}
