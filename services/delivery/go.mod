module github.com/caspervpn/delivery

go 1.22

require (
	github.com/caspervpn/contracts v0.0.0
	github.com/caspervpn/platform v0.0.0
)

// Resolved via the workspace (go.work) during the scaffolding wave.
replace github.com/caspervpn/contracts => ../../packages/contracts

replace github.com/caspervpn/platform => ../../packages/platform
