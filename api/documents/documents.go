// Package documents embeds the public privacy documents served by the API.
package documents

import _ "embed"

//go:embed consent.md
var Consent []byte

//go:embed privacy-policy.md
var PrivacyPolicy []byte
