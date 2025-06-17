package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	e2e "k8s.io/kubernetes/test/e2e/framework"

	// Include cloud providers at vendor time
	_ "k8s.io/kubernetes/test/e2e"
)

const (
	testProviderConfigEnvVar = "TEST_PROVIDER"
)

func InitializeProvider(context *e2e.TestContextType) error {
	envVar := os.Getenv(testProviderConfigEnvVar)
	if envVar == "" {
		// No provider given and nothing to do. k8s e2e framework will default to skeleton
		return nil
	}
	isJSON := json.Valid([]byte(envVar))
	if !isJSON && slices.Contains(e2e.GetProviders(), envVar) {
		// Just point to the provider and continue
		context.Provider = envVar
		return nil
	}
	if !isJSON {
		return fmt.Errorf("%s is neither a valid JSON object nor a supported provider", envVar)
	}

	providerInfo := &ClusterConfiguration{}
	if err := json.Unmarshal([]byte(envVar), &providerInfo); err != nil {
		return fmt.Errorf("provider must be a JSON object with the 'type' key at a minimum: %v", err)
	}

	// update context with loaded config
	context.Provider = providerInfo.ProviderName
	context.CloudConfig = e2e.CloudConfig{
		ProjectID:   providerInfo.ProjectID,
		Region:      providerInfo.Region,
		Zone:        providerInfo.Zone,
		Zones:       providerInfo.Zones,
		NumNodes:    providerInfo.NumNodes,
		MultiMaster: providerInfo.MultiMaster,
		MultiZone:   providerInfo.MultiZone,
		ConfigFile:  providerInfo.ConfigFile,
	}
	return nil
}
