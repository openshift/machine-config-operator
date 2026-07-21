package systemd

import (
	_ "embed"
)

//go:embed kubens.service
var Service string

//go:embed kubens-dropin.conf
var Dropin string

//go:embed mkWrapperDropin
var WrapperScript string

//go:embed Makefile
var Makefile string
