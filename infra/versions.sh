# Single source of truth for pinned infra tool versions, shared by bash + e2e.
#
# SINGBOX_VERSION is the ONE authoritative sing-box pin. real-node.sh sources this
# file (no separate literal), and the ansible singbox role default is held EQUAL to
# it by test/infra/versions-pin-guard.sh (run in CI) so the two can never drift.
# To bump: change SINGBOX_VERSION here AND the role default in the same commit; the
# guard fails the build otherwise.
SINGBOX_VERSION=1.11.11
