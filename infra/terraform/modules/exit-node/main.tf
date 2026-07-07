locals {
  labels = merge(
    {
      role    = "exit"
      managed = "caspervpn"
    },
    var.extra_labels,
  )

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    hostname         = var.name
    ops_user         = var.ops_user
    ssh_pubkey       = var.ssh_pubkey
    role             = "exit"
    allowed_entry_ip = var.allowed_entry_ip
  })
}
