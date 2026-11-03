// Package docs embeds the OpenAPI 3.1 contract for the gateway API (ТЗ §18,
// S17 T17.1). The canonical file is openapi.json in this directory.
package docs

import _ "embed"

// OpenAPI is the raw OpenAPI 3.1 document describing all API endpoints.
//
//go:embed openapi.json
var OpenAPI []byte
