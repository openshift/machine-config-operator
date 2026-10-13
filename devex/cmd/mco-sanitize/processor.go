package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3" // v3 is required. v2 doesn't handle map/array key/index types properly
	"k8s.io/klog/v2"
)

// FileProcessorImpl implements the FileProcessor interface and handles
// the processing of individual files for sanitization.
type FileProcessorImpl struct {
	redactor Redactor
}

// NewFileProcessor creates a new FileProcessorImpl instance with the provided redactor.
func NewFileProcessor(redactor Redactor) *FileProcessorImpl {
	return &FileProcessorImpl{redactor: redactor}
}

// Process handles the sanitization of a single file at the given path.
// Returns true if the file was modified, false otherwise, along with any error encountered.
func (p *FileProcessorImpl) Process(_ context.Context, path string) (changed bool, err error) {
	unmarshaller, marshaller := getMarshaller(path)
	if unmarshaller == nil || marshaller == nil {
		// Unsupported file format
		// Ignore the file
		return false, nil
	}

	inputData, resources := fetchInput(path, unmarshaller)
	if inputData == nil || resources == nil {
		// Turned out that the file extension was not matching the file content
		// or the file is corrupt/cannot read
		// Ignore the file
		return false, err
	}

	changed, err = p.redactor.Redact(inputData, resources, marshaller)
	if !changed || err != nil {
		// If the file didn't change or an error occurred do not touch the original file
		return changed, err
	}

	outFile, _ := os.Create(path)
	defer func() {
		if closeErr := outFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	err = writeOutput(outFile, inputData, marshaller)
	return changed, err
}

// writeOutput encodes the sanitized content and writes it to the provided writer
func writeOutput(outputWriter io.Writer, outputContent interface{}, marshaller contentMarshaler) error {
	encoded, err := marshaller(outputContent)
	if err != nil {
		return fmt.Errorf("could not encode redacted data back to the original format: %v", err)
	}

	if _, err = outputWriter.Write(encoded); err != nil {
		return err
	}
	return nil
}

// getMarshaller returns the appropriate unmarshaller and marshaller functions
// based on the file extension. Returns nil for unsupported formats.
func getMarshaller(path string) (contentUnmarshaller, contentMarshaler) {
	if strings.HasSuffix(path, ".json") {
		return json.Unmarshal, json.Marshal
	} else if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		return yaml.Unmarshal, yaml.Marshal
	}
	return nil, nil
}

// fetchInput reads and parses a file, returning the parsed content and metadata.
// Handles both individual resources and Kubernetes List types.
func fetchInput(path string, unmarshaler contentUnmarshaller) (interface{}, []KubernetesMetaResource) {
	inFile, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer func() {
		if closeErr := inFile.Close(); closeErr != nil {
			klog.Errorf("error closing input file %q: %v", path, closeErr)
		}
	}()

	inputBytes, err := io.ReadAll(inFile)
	if err != nil {
		return nil, nil
	}

	var resource KubernetesMetaResource
	err = unmarshaler(inputBytes, &resource)
	if err != nil || resource.Kind == "" || resource.APIVersion == "" {
		return nil, nil
	}

	// check if the input was a list type
	if strings.HasSuffix(resource.Kind, "List") && resource.APIVersion == "v1" {
		var kubernetesList KubernetesMetaListInterface
		err = unmarshaler(inputBytes, &kubernetesList)
		if err != nil {
			return nil, nil
		}
		return kubernetesList.Items, resource.Items
	}
	var inputData map[string]interface{}
	err = unmarshaler(inputBytes, &inputData)
	if err != nil {
		return nil, nil
	}
	return inputData, []KubernetesMetaResource{resource}
}
