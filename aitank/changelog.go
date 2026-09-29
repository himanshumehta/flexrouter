// Package aitank holds files embedded into the binary.
package aitank

import _ "embed"

// Changelog is CHANGELOG.md, shown by `aitank changelog`.
//
//go:embed CHANGELOG.md
var Changelog string
