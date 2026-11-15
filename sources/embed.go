// Package sources embeds third-party files afford ships, so the copy in the
// repository is the copy in the binary.
package sources

import _ "embed"

// AxeJS is axe-core, injected into pages for the accessibility pass.
//
//go:embed axe.min.js
var AxeJS string
