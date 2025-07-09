package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/openshift/machine-config-operator/pkg/secrets"
)

type secretConverter struct {
	of outputFormat
	is secrets.ImageRegistrySecret
}

func newSecretConverter(secretFile string, of outputFormat) (*secretConverter, error) {
	of = outputFormat(strings.ToLower(string(of)))

	secretBytes, err := os.ReadFile(secretFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read secret file '%s': %w", secretFile, err)
	}

	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, fmt.Errorf("could not get ImageRegistrySecret: %w", err)
	}

	return &secretConverter{is: is, of: of}, nil
}

func (s *secretConverter) convert() ([]byte, error) {
	switch s.of {
	case dockercfgOutputFormat:
		return s.getPrettyJSONForSecretType(corev1.SecretTypeDockercfg)
	case dockerconfigjsonOutputFormat:
		return s.getPrettyJSONForSecretType(corev1.SecretTypeDockerConfigJson)
	case k8sDockercfgOutputFormat:
		return s.getYAMLK8sSecretForType(corev1.SecretTypeDockercfg)
	case k8sDockerconfigjsonOutputFormat:
		return s.getYAMLK8sSecretForType(corev1.SecretTypeDockerConfigJson)
	case k8sJSONDockercfgOutputFormat:
		return s.getJSONK8sSecretForType(corev1.SecretTypeDockercfg)
	case k8sJSONDockerconfigjsonOutputFormat:
		return s.getJSONK8sSecretForType(corev1.SecretTypeDockerConfigJson)
	default:
		return nil, fmt.Errorf("unsupported output format: %s. Supported formats are: %v", s.of, outputFormats)
	}
}

func (s *secretConverter) getYAMLK8sSecretForType(secretType corev1.SecretType) ([]byte, error) {
	k8sSecret, err := s.is.K8sSecret(secretType)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(k8sSecret)
}

func (s *secretConverter) getJSONK8sSecretForType(secretType corev1.SecretType) ([]byte, error) {
	k8sSecret, err := s.is.K8sSecret(secretType)
	if err != nil {
		return nil, err
	}

	jsonBytes, err := json.Marshal(k8sSecret)
	if err != nil {
		return nil, err
	}

	return s.prettyPrintJSON(jsonBytes)
}

func (s *secretConverter) getPrettyJSONForSecretType(secretType corev1.SecretType) ([]byte, error) {
	jsonBytes, err := s.is.JSONBytes(secretType)
	if err != nil {
		return nil, err
	}

	jsonBytes, err = s.prettyPrintJSON(jsonBytes)
	if err != nil {
		return nil, err
	}

	return jsonBytes, nil
}

func (s *secretConverter) prettyPrintJSON(jsonBytes []byte) ([]byte, error) {
	prettyJSON := bytes.NewBuffer([]byte{})

	if err := json.Indent(prettyJSON, jsonBytes, "", "  "); err != nil {
		return nil, fmt.Errorf("could not pretty-print JSON: %w", err)
	}

	return prettyJSON.Bytes(), nil

}
