package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type FileProcessorImpl struct {
	redactor Redactor
}

func NewFileProcessor(redactor Redactor) *FileProcessorImpl {
	return &FileProcessorImpl{redactor: redactor}
}

func (p *FileProcessorImpl) Process(_ context.Context, path string) (bool, error) {
	unmarshaller, marshaller := getMarshaller(path)
	if unmarshaller == nil || marshaller == nil {
		// Unsupported file format
		return false, nil
	}

	inputData, resources := fetchInput(path, unmarshaller)
	if inputData == nil || resources == nil {
		// Turned out that the file extension was not matching the file content
		// or the file is corrupt
		// Ignore the file
		return false, nil
	}

	changed, err := p.redactor.Redact(inputData, resources, marshaller)
	if !changed || err != nil {
		return changed, err
	}

	outFile, _ := os.Create(path)
	defer outFile.Close()
	err = writeOutput(outFile, inputData, marshaller)
	return changed, err
}

func writeOutput(outputWriter io.Writer, outputContent interface{}, marshaller contentMarshaler) error {
	encoded, err := marshaller(outputContent)
	if err != nil {
		return fmt.Errorf("could not encode redacted data back to the original format: %v", err)
	}
	_, err = outputWriter.Write(encoded)
	if err != nil {
		// todo handler
		return err
	}
	return nil
}

func getMarshaller(path string) (contentUnmarshaller, contentMarshaler) {
	if strings.HasSuffix(path, ".json") {
		return json.Unmarshal, json.Marshal
	} else if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		return yaml.Unmarshal, yaml.Marshal
	}
	return nil, nil
}

func fetchInput(path string, unmarshaler contentUnmarshaller) (interface{}, []KubernetesMetaResource) {
	inFile, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer inFile.Close()

	inputBytes, err := io.ReadAll(inFile)
	if err != nil {
		return nil, nil
	}

	var resource KubernetesMetaResource
	err = unmarshaler(inputBytes, &resource)
	if err != nil || resource.Kind == "" || resource.ApiVersion == "" {
		return nil, nil
	}

	// check if the input was a list type
	if strings.HasSuffix(resource.Kind, "List") && resource.ApiVersion == "v1" {
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
