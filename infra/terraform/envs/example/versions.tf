terraform {
  required_version = ">= 1.5.0"
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = ">= 1.45"
    }
    vultr = {
      source  = "vultr/vultr"
      version = ">= 2.19"
    }
  }
}
