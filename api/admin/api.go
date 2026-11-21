// Package admin holds the OpenAPI description of rosetta's admin API.
package admin

import _ "embed"

// OpenAPI is api/admin/openapi.yaml, the source of truth for /admin/v1.
//
//go:embed openapi.yaml
var OpenAPI []byte
