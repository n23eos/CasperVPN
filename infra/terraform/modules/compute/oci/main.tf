# Oracle Cloud instance. Provider auth (tenancy/user/key/region) is configured in
# the env's provider block from env vars — never in code.

locals {
  is_flex = length(regexall("Flex", var.size)) > 0
}

resource "oci_core_instance" "this" {
  availability_domain = var.availability_domain
  compartment_id      = var.compartment_ocid
  shape               = var.size
  display_name        = var.name
  freeform_tags       = var.tags

  create_vnic_details {
    subnet_id        = var.subnet_ocid
    assign_public_ip = true
    hostname_label   = replace(var.name, "/[^a-z0-9]/", "")
  }

  source_details {
    source_type = "image"
    source_id   = var.image_ocid
  }

  metadata = {
    ssh_authorized_keys = var.ssh_pubkey
    user_data           = base64encode(var.user_data)
  }

  # Only meaningful for *.Flex shapes; harmless otherwise.
  dynamic "shape_config" {
    for_each = local.is_flex ? [1] : []
    content {
      ocpus         = var.flex_ocpus
      memory_in_gbs = var.flex_memory_gbs
    }
  }
}
