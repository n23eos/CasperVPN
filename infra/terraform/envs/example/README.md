# envs/example — reference entry+exit root

One **entry** (Hetzner) + one **exit** (Vultr), on purpose on different clouds to
demonstrate the `entry != exit` separation. Not applied in CI (validate/plan only).

```bash
export HCLOUD_TOKEN=...      # entry
export VULTR_API_KEY=...     # exit
cp terraform.tfvars.example terraform.tfvars   # fill ssh_pubkey etc.
terraform init
terraform plan
terraform apply
terraform output entry_ip exit_ip
```

Move a role to another cloud by swapping only the `source` of `entry_vm`/`exit_vm`
to another `../../modules/compute/<cloud>` — the role modules do not change.
Adding a brand-new cloud = copy `modules/compute/<existing>/` and swap the
resource, keeping the same variables/outputs.
