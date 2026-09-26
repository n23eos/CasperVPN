# Periodic access reconciliation

The node access lease is at most five minutes. Install this host-side timer so
every manifest-backed entry receives a fresh authoritative snapshot every two
minutes. A failed run exits nonzero and is visible in systemd and the journal.

Install from a checkout at `/opt/caspervpn`:

```sh
sudo install -D -m 0644 infra/systemd/caspervpn-access-reconcile.service /etc/systemd/system/caspervpn-access-reconcile.service
sudo install -D -m 0644 infra/systemd/caspervpn-access-reconcile.timer /etc/systemd/system/caspervpn-access-reconcile.timer
sudo install -D -m 0600 /dev/null /etc/caspervpn/access-reconcile.env
```

Populate `/etc/caspervpn/access-reconcile.env` with deployment-specific values:

```sh
RUN_DIR=/var/lib/caspervpn/runs
CONTROL_PLANE_URL=https://control-plane.example.invalid
CONTROL_PLANE_TOKEN=<orchestrator-service-token>
PAIR_PSK=<entry-exit-psk>
```

The host also needs `bash`, `jq`, `curl`, `ansible`, `ansible-playbook`, and
`python3`. Manifests must be mode 0600 in `RUN_DIR`.
Provider credentials are not required for this access-only timer.

Validate the unit and run one local service attempt before enabling the timer:

```sh
sudo systemd-analyze verify /etc/systemd/system/caspervpn-access-reconcile.service /etc/systemd/system/caspervpn-access-reconcile.timer
sudo systemctl daemon-reload
sudo systemctl start caspervpn-access-reconcile.service
sudo systemctl status caspervpn-access-reconcile.service
sudo systemctl enable --now caspervpn-access-reconcile.timer
```

The service performs real SSH, control-plane, and client probe operations. Do
not start it until the env file and manifest fleet point at the intended launch
environment.
