package fixtures

// Using Golang embed
import _ "embed"

//go:embed compressed_ign_config.json.gz
var CompressedIgnConfig []byte

//go:embed compressed_and_encoded_ign_config.json.gz
var CompressedAndEncodedIgnConfig []byte

//go:embed ign_config.json
var IgnConfig []byte
