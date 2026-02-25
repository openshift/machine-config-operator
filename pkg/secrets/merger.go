package secrets

// SecretMerger implements a simple secret merger that adds secrets into a
// DockerConfigJSON, overwriting any existing secrets that have the same key.
type SecretMerger struct {
	cfg DockerConfigJSON
}

// NewSecretMerger creates and returns a new, initialized SecretMerger instance.
func NewSecretMerger() *SecretMerger {
	return &SecretMerger{cfg: newDockerConfigJSON()}
}

// Insert adds a given secret into the internal DockerConfigJSON.
// It accepts any type that can be converted to an ImageRegistrySecret and
// merges its authentication entries, overwriting entries with duplicate keys.
func (s *SecretMerger) Insert(val any) error {
	is, err := newImageRegistrySecretFromAny(val)
	if err != nil {
		return err
	}

	for key, val := range is.DockerConfigJSON().Auths {
		s.cfg.Auths[key] = val
	}

	return nil
}

// ImageRegistrySecret returns the merged DockerConfigJSON as an ImageRegistrySecret.
func (s *SecretMerger) ImageRegistrySecret() ImageRegistrySecret {
	return newImageRegistrySecretFromDockerConfigJSON(s.cfg)
}
