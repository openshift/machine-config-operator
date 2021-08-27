package manifests

import "embed"

//go:embed *
// Static contains the embedded manifests
var Static embed.FS
