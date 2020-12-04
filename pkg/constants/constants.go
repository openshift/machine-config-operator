package constants

// constants defines some file paths that are shared outside of the
// MCO package; and thus consumed by other users

const (
	// APIServerURLFile is the path to the apiserver url environment file.
	// See templates/master/00-master/_base/files/apiserver-url-env.yaml
	APIServerURLFile = "/etc/kubernetes/apiserver-url.env"
)

// ConstantsByName is a map of constants for ease of templating
var ConstantsByName = map[string]string{
	"APIServerURLFile": APIServerURLFile,
}
