# Vultr instance. Credentials come from VULTR_API_KEY env var, never from code.
# os_id is resolved from the image slug so the interface stays cloud-agnostic.

data "vultr_os" "this" {
  filter {
    name   = "name"
    values = [var.image_os_name]
  }
}

resource "vultr_ssh_key" "this" {
  name    = var.name
  ssh_key = var.ssh_pubkey
}

resource "vultr_instance" "this" {
  label       = var.name
  hostname    = var.name
  region      = var.region
  plan        = var.size
  os_id       = data.vultr_os.this.id
  user_data   = var.user_data
  ssh_key_ids = [vultr_ssh_key.this.id]
  tags        = [for k, v in var.tags : "${k}:${v}"]

  backups = "disabled"
}
