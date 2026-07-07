locals {
  labels = merge(
    {
      role      = "entry"
      ephemeral = tostring(var.ephemeral)
      managed   = "caspervpn"
    },
    var.extra_labels,
  )

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    hostname   = var.name
    ops_user   = var.ops_user
    ssh_pubkey = var.ssh_pubkey
    role       = "entry"
    # Entry forwards decrypted client traffic on to the paired exit, so it must
    # route packets. It exposes the mimicry transports publicly (firewall role).
    ip_forward = true
  })
}
