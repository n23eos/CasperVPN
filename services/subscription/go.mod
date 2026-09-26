module github.com/caspervpn/subscription

go 1.27.1

require (
	github.com/caspervpn/contracts v0.0.0
	github.com/caspervpn/platform v0.0.0
	github.com/jackc/pgx/v5 v5.9.2
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.39.0 // indirect
)

// Resolved via the workspace (go.work) during the scaffolding wave.
replace github.com/caspervpn/contracts => ../../packages/contracts

replace github.com/caspervpn/platform => ../../packages/platform
