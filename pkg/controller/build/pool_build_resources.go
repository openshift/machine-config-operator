package build

// This was arose from the need to keep track of resources the pool should ensure the presence of.
// It is not as elegant as it should be, but the problem I am trying to solve is capturing:
// 1.) what resources the pool should ensure the presence of and
// 2.) the relationships between them

import (
	"bytes"
	"text/template"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

type PoolResourceNames struct {
	ImageStream PoolImageStreamList
	BuildConfig PoolBuildConfigList
}

type PoolImageStreamList struct {
	// Base image for all of our builds
	Base string
	// Base image supplied externally, takes precedence over default CoreOS stream
	ExternalBase string
	// Where the render controller renders its tiny config image
	RenderedConfig string
	// Where the MCO outputs its multi-stage build with machineconfig to
	Content string
	// Where the image goes if a user uses the custom buildconfig
	CustomContent string
	// Hypothetically if you built a working image outside the cluster, you would tag it here
	External string
	// Where we look for a per-node config image
	PerNode string
}

type PoolBuildConfigList struct {
	Content       PoolBuildConfig
	CustomContent PoolBuildConfig
}

type PoolBuildConfig struct {
	Name               string
	Source             string
	Target             string
	TriggeredByStreams []string
	DockerfileContent  string
}

func PoolBuildResources(pool *mcfgv1.MachineConfigPool) *PoolResourceNames {

	pisl := PoolImageStreamList{
		Base:           pool.Name + ctrlcommon.ImageStreamSuffixCoreOS,
		ExternalBase:   pool.Name + ctrlcommon.ImageStreamSuffixExternalBase,
		RenderedConfig: pool.Name + ctrlcommon.ImageStreamSuffixRenderedConfig,
		Content:        pool.Name + ctrlcommon.ImageStreamSuffixMCOContent,
		CustomContent:  pool.Name + ctrlcommon.ImageStreamSuffixMCOContentCustom,
		External:       pool.Name + ctrlcommon.ImageStreamSuffixMCOContentExternal,
		PerNode:        pool.Name + ctrlcommon.ImageStreamSuffixMCOContentPerNode,
	}

	// Templated dockerfile for the complicated mco-content buildconfig that applies the rendered-config
	t, _ := template.New(machineConfigContentDockerfile).Parse(machineConfigContentDockerfile)
	var tpl bytes.Buffer
	t.Execute(&tpl, pisl)

	bcl := PoolBuildConfigList{
		Content: PoolBuildConfig{
			Name:               pool.Name + "-build" + ctrlcommon.ImageStreamSuffixMCOContent,
			Source:             pisl.Base,
			Target:             pisl.Content,
			TriggeredByStreams: []string{pisl.RenderedConfig + ":latest"},
			DockerfileContent:  tpl.String(),
		},
		CustomContent: PoolBuildConfig{
			Name:               pool.Name + "-build" + ctrlcommon.ImageStreamSuffixMCOContentCustom,
			Source:             pisl.Content,
			Target:             pisl.CustomContent,
			TriggeredByStreams: []string{},
			DockerfileContent:  dummyDockerfile,
		},
	}

	return &PoolResourceNames{pisl, bcl}
}

// IsManagedImageStream tells us if a given image stream name is one of the names we think we should be managing. This is used to tell if
// someone has assigned some completely unmanaged imagestream to our layered pool.
func (prn *PoolResourceNames) IsManagedImageStream(imageStreamName string) bool {
	// TODO(jkyros): The longer this goes on, the more I feel this should be a map
	if imageStreamName == prn.ImageStream.Base ||
		imageStreamName == prn.ImageStream.Content ||
		imageStreamName == prn.ImageStream.CustomContent ||
		imageStreamName == prn.ImageStream.External ||
		imageStreamName == prn.ImageStream.PerNode {
		return true
	}
	return false
}
