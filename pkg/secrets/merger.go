package secrets

// Implements a simple secret merger which will add any secrets into the
// DockerConfigJSON, overwriting any secrets that have the same key.
type SecretMerger struct {
	cfg DockerConfigJSON
}

func NewSecretMerger() *SecretMerger {
	return &SecretMerger{cfg: newDockerConfigJSON()}
}

// Inserts a given secret into the internal DockerConfigJSON.
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

// Returns the DockerConfigJSON as an ImageRegistrySecret.
func (s *SecretMerger) ImageRegistrySecret() ImageRegistrySecret {
	return newImageRegistrySecretFromDockerConfigJSON(s.cfg)
}
