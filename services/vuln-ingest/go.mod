module github.com/hummingbird-org/vuln-ingest

go 1.26.1

require (
	github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler v0.0.0
	github.com/jackc/pgx/v5 v5.7.5
	go.yaml.in/yaml/v4 v4.0.0-rc.4
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler => ../../workqueue/sdk/go/reconciler
