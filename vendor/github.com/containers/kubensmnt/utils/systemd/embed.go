package systemd

//go:embed kubens.service
var Service string

//go:embed kubens-dropin.conf
var Dropin string
