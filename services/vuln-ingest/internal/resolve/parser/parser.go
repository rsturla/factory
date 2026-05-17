package parser

import "github.com/hummingbird-org/vuln-ingest/internal/model"

// Parser normalizes raw vulnerability data into domain model.
// Multiple sources can share one parser (e.g., all OSV-format feeds).
type Parser interface {
	Parse(data []byte) ([]model.Vulnerability, error)
}
