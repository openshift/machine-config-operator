package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/openshift/machine-config-operator/pkg/secrets"
)

// secretConverter is a struct that encapsulates the logic for converting secrets.
// It holds the ImageRegistrySecret, configuration, and the determined Kubernetes secret type.
type secretConverter struct {
	is secrets.ImageRegistrySecret
	*config
	secretType corev1.SecretType
}

// newSecretConverter creates and initializes a new secretConverter instance.
// It takes a config struct as input, validates it, reads the secret file,
// and prepares the ImageRegistrySecret for conversion.
func newSecretConverter(cfg *config) (*secretConverter, error) {
	// Default to YAML for Kubernetes embedding and JSON for other cases.
	if cfg.outputFormat == "" {
		if cfg.embed {
			cfg.outputFormat = formatYAML
		} else {
			cfg.outputFormat = formatJSON
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	secretType, err := cfg.getSecretType()
	if err != nil {
		return nil, fmt.Errorf("could not get secret type: %w", err)
	}

	secretBytes, err := os.ReadFile(cfg.secretFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read secret file '%s': %w", cfg.secretFile, err)
	}

	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, fmt.Errorf("could not get ImageRegistrySecret from %s: %w", cfg.secretFile, err)
	}

	return &secretConverter{is: is, config: cfg, secretType: secretType}, nil
}

// doConversion performs the actual secret conversion based on the configured output type and embed option.
// It returns the converted secret as an interface{} or an error if the conversion fails.
func (s *secretConverter) doConversion() (interface{}, error) {
	if s.embed {
		return s.is.K8sSecret(s.secretType)
	}

	if s.outputType == typeDockerconfigJSON {
		return s.is.DockerConfigJSON(), nil
	}

	return s.is.DockerConfig(), nil
}

// convert orchestrates the secret conversion process.
// It calls doConversion, formats the output, and writes it to the specified output file or stdout.
func (s *secretConverter) convert() error {
	converted, err := s.doConversion()
	if err != nil {
		return err
	}

	outBytes, err := s.getOutputBytes(converted)
	if err != nil {
		return err
	}

	writeCloser, err := s.getWriteCloser()
	if err != nil {
		return err
	}

	defer writeCloser.Close()

	if _, err := writeCloser.Write(outBytes); err != nil {
		return err
	}

	return nil
}

// getOutputBytes converts the given data into a byte slice based on the configured output format (JSON or YAML).
// It returns an error if the output format is invalid or missing.
func (s *secretConverter) getOutputBytes(data interface{}) ([]byte, error) {
	if s.outputFormat != formatJSON && s.outputFormat != formatYAML {
		return nil, fmt.Errorf("invalid or missing output type %q", s.outputFormat)
	}

	if s.outputFormat == formatYAML {
		return yaml.Marshal(data)
	}

	return s.prettyPrintJSON(data)
}

// prettyPrintJSON marshals the given data to JSON and then indents it for pretty printing.
// It returns the pretty-printed JSON as a byte slice or an error if marshaling or indenting fails.
func (s *secretConverter) prettyPrintJSON(data interface{}) ([]byte, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	prettyJSON := bytes.NewBuffer([]byte{})

	if err := json.Indent(prettyJSON, jsonBytes, "", "  "); err != nil {
		return nil, fmt.Errorf("could not pretty-print JSON: %w", err)
	}

	return prettyJSON.Bytes(), nil
}

// getWriteCloser returns an io.WriteCloser for writing the output.
// It returns an os.File if an outputFile is specified in the config, otherwise it returns os.Stdout.
func (s *secretConverter) getWriteCloser() (io.WriteCloser, error) {
	if s.outputFile != "" {
		return os.Create(s.outputFile)
	}

	return os.Stdout, nil
}
