module github.com/caspervpn/control-plane

go 1.27.1

require (
	github.com/caspervpn/contracts v0.0.0
	github.com/caspervpn/platform v0.0.0
	github.com/go-chi/chi/v5 v5.0.12
	github.com/jackc/pgx/v5 v5.9.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.6.1 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.39.0 // indirect
)

// Resolved via the workspace (go.work) during the scaffolding wave.
replace github.com/caspervpn/contracts => ../../packages/contracts

replace github.com/caspervpn/platform => ../../packages/platform
