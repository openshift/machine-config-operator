package osrelease

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOSReleaseFromImageLabels(t *testing.T) {
	t.Parallel()

	// $ skopeo inspect --no-tags "docker://$(oc adm release info --pullspecs "quay.io/openshift-release-dev/ocp-release:4.12.3-x86_64" -o json | jq -r '.references.spec.tags[] | select(.name == "rhel-coreos-8") | .from.name')" | jq '.Labels'
	rhcos86ImageLabels := map[string]string{
		"coreos-assembler.image-config-checksum": "1b48fcfc2329e3b643345459fa579d094ab4cf32bbe02e6af7f75bed2d795664",
		"coreos-assembler.image-input-checksum":  "958ec627288ced8977d29fe56b753fddccb22b5574674dd466ca36ee0a65ef7b",
		"org.opencontainers.image.revision":      "87d66dba922c2b82490f582d3b40042892fd71d9",
		"org.opencontainers.image.source":        "https://github.com/openshift/os",
		"ostree.bootable":                        "true",
		"ostree.commit":                          "f6f57efc27907e9cb02b25abe8f662c83e5518573f8ac72db56d352f0e53132a",
		"ostree.final-diffid":                    "sha256:dc44336569c692fdab109180b59f6cfd33d4276d7c463b1939a453c547407e91",
		"ostree.linux":                           "4.18.0-372.43.1.el8_6.x86_64",
		"rpmostree.inputhash":                    "d22d3ff9f440dd92013e08b273feaf2ca1c7add9d86989d1ac5cb39914810fbe",
		"version":                                "412.86.202302091057-0",
	}

	// $ skopeo inspect --no-tags 'docker://registry.ci.openshift.org/ocp/4.13:rhel-coreos-9' | jq '.Labels'
	rhcos92ImageLabels := map[string]string{
		"coreos-assembler.image-config-checksum": "01bebb5709ab54cef0e7bac1f94e6ee333b25868ab022f8618cdb4443de99355",
		"coreos-assembler.image-input-checksum":  "cd8d504aea915c32fce40e0dbf3c2424cf9612df6e21a00874668abb8ab75c10",
		"org.opencontainers.image.revision":      "d717505b88821990fee2d96688c337995d849ecd",
		"org.opencontainers.image.source":        "https://github.com/openshift/os",
		"ostree.bootable":                        "true",
		"ostree.commit":                          "75a403bab1c8e65291f8b905d6080789a123961d341b56a3b8988cf87fa50ee4",
		"ostree.final-diffid":                    "sha256:896666c665dc31ed85f0cad0a23b6d8fed5fdcb913c5d3f21d2157a3c3c72883",
		"ostree.linux":                           "5.14.0-252.el9.x86_64",
		"rpmostree.inputhash":                    "ff03479ce560a2e7d33518f56151c798de8af5cec9c80fc6849586f2a46df30d",
		"version":                                "413.92.202302081904-0",
	}

	// $ skopeo inspect --no-tags "docker://$(oc adm release info --pullspecs "registry.ci.openshift.org/origin/release-scos:4.13.0-0.okd-scos-2023-02-13-084859" -o json | jq -r '.references.spec.tags[] | select(.name == "centos-stream-coreos-9") | .from.name')" | jq '.Labels'
	scosImageLabels := map[string]string{
		"coreos-assembler.image-config-checksum":   "60de8e2e7e531654866c9b4cb39f25ab40f7c1c6a228c67094532582fff32517",
		"coreos-assembler.image-input-checksum":    "242aa41d018c8898be12f52920ced133d8c113663a73f73d3f9ff3f4cba616ec",
		"io.openshift.build.version-display-names": "machine-os=CentOS Stream CoreOS",
		"io.openshift.build.versions":              "machine-os=413.9.202302130811-0",
		"org.opencontainers.image.revision":        "0d059dc913d80034c4947c83b45cab9134a4b76b",
		"org.opencontainers.image.source":          "https://github.com/openshift/os.git",
		"ostree.bootable":                          "true",
		"ostree.commit":                            "bf556ab10cfd284ce9af1760bb3c58e021eeb53e3dad0a7067fc27cc00005229",
		"ostree.final-diffid":                      "sha256:6bab3208d1cc275dde2344b7b821b2c42c2bb9839316d9f8e7dec7856f3ce95b",
		"ostree.linux":                             "5.14.0-252.el9.x86_64",
		"rpmostree.inputhash":                      "b0a41cc20b028c5480fef2d4fc91c1b26d3b99cdef18e3812e6c290f7f4bb942",
		"version":                                  "413.9.202302130811-0",
	}

	// $ skopeo inspect --no-tags "docker://$(oc adm release info --pullspecs "registry.ci.openshift.org/origin/release:4.13.0-0.okd-2023-02-13-013048" -o json | jq -r '.references.spec.tags[] | select(.name == "fedora-coreos") | .from.name')" | jq '.Labels'
	fcosImageLabels := map[string]string{
		"coreos-assembler.image-config-checksum":   "12fcea940941bb0fdfc7e693af5d5b80c20103cad5b2f7d1b325695c23e267bb",
		"coreos-assembler.image-input-checksum":    "545e10a3b7b9678dee912f65946bae68115239c77060427e56c9be36725822e4",
		"fedora-coreos.stream":                     "testing-devel",
		"io.buildah.version":                       "1.26.4",
		"io.openshift.build.commit.author":         "",
		"io.openshift.build.commit.date":           "",
		"io.openshift.build.commit.id":             "dca734c49f2d3b72877059d0efe4b0ebb46bf0cf",
		"io.openshift.build.commit.message":        "",
		"io.openshift.build.commit.ref":            "master",
		"io.openshift.build.name":                  "",
		"io.openshift.build.namespace":             "",
		"io.openshift.build.source-context-dir":    "",
		"io.openshift.build.source-location":       "https://github.com/openshift/okd-machine-os",
		"io.openshift.build.version-display-names": "machine-os=Fedora CoreOS",
		"io.openshift.build.versions":              "machine-os=37.20230211.20",
		"io.openshift.release.operator":            "true",
		"org.opencontainers.image.revision":        "93c169993547dacc9a3db20c5aa9b010edc9b7fe",
		"org.opencontainers.image.source":          "https://github.com/coreos/fedora-coreos-config",
		"ostree.bootable":                          "true",
		"ostree.commit":                            "a9cae909177102060c848b4088c046274fcfb8297ff1544bd80b0c6c8ce43f47",
		"ostree.final-diffid":                      "sha256:e6fbb4f4b828c4cefb957fd625b1205c15615303a82f019db1f66c80901ca08b",
		"ostree.linux":                             "6.1.10-200.fc37.x86_64",
		"rpmostree.inputhash":                      "4042bcea14f8713abf293302c26b60bca32438e6b59c7ec96761f6b28a0762b2",
		"vcs-ref":                                  "dca734c49f2d3b72877059d0efe4b0ebb46bf0cf",
		"vcs-type":                                 "git",
		"vcs-url":                                  "https://github.com/openshift/okd-machine-os",
		"version":                                  "37.20230211.20.0",
	}

	// $ skopeo inspect --no-tags 'docker://registry.access.redhat.com/ubi8/ubi:latest' | jq '.Labels'
	ubi8ImageLabels := map[string]string{
		"architecture":                 "x86_64",
		"build-date":                   "2023-02-07T17:57:16",
		"com.redhat.component":         "ubi8-container",
		"com.redhat.license_terms":     "https://www.redhat.com/en/about/red-hat-end-user-license-agreements#UBI",
		"description":                  "The Universal Base Image is designed and engineered to be the base layer for all of your containerized applications, middleware and utilities. This base image is freely redistributable, but Red Hat only supports Red Hat technologies through subscriptions for Red Hat products. This image is maintained by Red Hat and updated regularly.",
		"distribution-scope":           "public",
		"io.buildah.version":           "1.27.3",
		"io.k8s.description":           "The Universal Base Image is designed and engineered to be the base layer for all of your containerized applications, middleware and utilities. This base image is freely redistributable, but Red Hat only supports Red Hat technologies through subscriptions for Red Hat products. This image is maintained by Red Hat and updated regularly.",
		"io.k8s.display-name":          "Red Hat Universal Base Image 8",
		"io.openshift.expose-services": "",
		"io.openshift.tags":            "base rhel8",
		"maintainer":                   "Red Hat, Inc.",
		"name":                         "ubi8",
		"release":                      "1054.1675788412",
		"summary":                      "Provides the latest release of Red Hat Universal Base Image 8.",
		"url":                          "https://access.redhat.com/containers/#/registry.access.redhat.com/ubi8/images/8.7-1054.1675788412",
		"vcs-ref":                      "a995512a05037e3b60bbb1bf9fa6e394063131c3",
		"vcs-type":                     "git",
		"vendor":                       "Red Hat, Inc.",
		"version":                      "8.7",
	}

	// $ skopeo inspect --no-tags 'docker://registry.access.redhat.com/ubi9/ubi:latest' | jq '.Labels'
	ubi9ImageLabels := map[string]string{
		"architecture":                 "x86_64",
		"build-date":                   "2023-02-07T16:24:49",
		"com.redhat.component":         "ubi9-container",
		"com.redhat.license_terms":     "https://www.redhat.com/en/about/red-hat-end-user-license-agreements#UBI",
		"description":                  "The Universal Base Image is designed and engineered to be the base layer for all of your containerized applications, middleware and utilities. This base image is freely redistributable, but Red Hat only supports Red Hat technologies through subscriptions for Red Hat products. This image is maintained by Red Hat and updated regularly.",
		"distribution-scope":           "public",
		"io.buildah.version":           "1.27.3",
		"io.k8s.description":           "The Universal Base Image is designed and engineered to be the base layer for all of your containerized applications, middleware and utilities. This base image is freely redistributable, but Red Hat only supports Red Hat technologies through subscriptions for Red Hat products. This image is maintained by Red Hat and updated regularly.",
		"io.k8s.display-name":          "Red Hat Universal Base Image 9",
		"io.openshift.expose-services": "",
		"io.openshift.tags":            "base rhel9",
		"maintainer":                   "Red Hat, Inc.",
		"name":                         "ubi9",
		"release":                      "1750.1675784955",
		"summary":                      "Provides the latest release of Red Hat Universal Base Image 9.",
		"url":                          "https://access.redhat.com/containers/#/registry.access.redhat.com/ubi9/images/9.1.0-1750.1675784955",
		"vcs-ref":                      "cf87ad00feaef3d9d7a442dad55ab6a14f6a3f81",
		"vcs-type":                     "git",
		"vendor":                       "Red Hat, Inc.",
		"version":                      "9.1.0",
	}

	testCases := []struct {
		Name                   string
		OSImageLabels          map[string]string
		IsEL                   bool
		IsEL9                  bool
		IsFCOS                 bool
		IsSCOS                 bool
		IsCoreOSVariant        bool
		IsLikeTraditionalRHEL7 bool
		ToPrometheusLabel      string
		ErrorExpected          bool
		ExpectedVersion        string
		ExpectedID             string
	}{
		{
			Name:                   "RHCOS 8.6",
			OSImageLabels:          rhcos86ImageLabels,
			IsEL:                   true,
			IsEL9:                  false,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "RHCOS",
			ExpectedVersion:        "86.202302091057-0",
			ExpectedID:             rhcos,
		},
		{
			Name:                   "RHCOS 9.2",
			OSImageLabels:          rhcos92ImageLabels,
			IsEL:                   true,
			IsEL9:                  true,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "RHCOS",
			ExpectedVersion:        "92.202302081904-0",
			ExpectedID:             rhcos,
		},
		{
			Name:                   "SCOS",
			OSImageLabels:          scosImageLabels,
			IsEL:                   true,
			IsEL9:                  true,
			IsFCOS:                 false,
			IsSCOS:                 true,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "SCOS",
			ExpectedVersion:        "9.202302130811-0",
			ExpectedID:             scos,
		},
		{
			Name:                   "FCOS",
			OSImageLabels:          fcosImageLabels,
			IsEL:                   false,
			IsEL9:                  false,
			IsFCOS:                 true,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "FEDORA",
			ExpectedVersion:        "37.20230211.20.0",
			ExpectedID:             fedora,
		},
		{
			Name: "Unidentifiable OS - Unknown Name",
			OSImageLabels: map[string]string{
				"coreos-assembler.image-input-checksum":    "",
				"coreos-assembler.image-config-checksum":   "",
				"org.opencontainers.image.revision":        "",
				"org.opencontainers.image.source":          "",
				"version":                                  "",
				"io.openshift.build.version-display-names": "machine-os=Unknown Operating System",
			},
			ErrorExpected: true,
		},
		{
			Name:          "Unidentifiable OS - Empty labels",
			OSImageLabels: map[string]string{},
			ErrorExpected: true,
		},
		{
			Name: "Unidentifiable OS - Invalid RHCOS Version ID",
			OSImageLabels: map[string]string{
				"coreos-assembler.image-input-checksum":  "",
				"coreos-assembler.image-config-checksum": "",
				"org.opencontainers.image.revision":      "",
				"org.opencontainers.image.source":        "",
				"version":                                "37.20230211.20.0",
			},
			ErrorExpected: true,
		},
		{
			Name: "Unidentifiable OS - Invalid SCOS Version ID",
			OSImageLabels: map[string]string{
				"coreos-assembler.image-input-checksum":    "",
				"coreos-assembler.image-config-checksum":   "",
				"org.opencontainers.image.revision":        "",
				"org.opencontainers.image.source":          "",
				"version":                                  "37.20230211.20.0",
				"io.openshift.build.version-display-names": "machine-os=CentOS Stream CoreOS",
			},
			ErrorExpected: true,
		},
		{
			Name: "Unidentifiable OS - Fedora 37 Container",
			OSImageLabels: map[string]string{
				// $ skopeo inspect --no-tags 'docker://registry.fedoraproject.org/fedora:latest' | jq '.Labels'
				"license": "MIT",
				"name":    "fedora",
				"vendor":  "Fedora Project",
				"version": "37",
			},
			ErrorExpected: true,
		},
		{
			Name:          "Unidentifiable OS - Red Hat UBI 8",
			OSImageLabels: ubi8ImageLabels,
			ErrorExpected: true,
		},
		{
			Name:          "Unidentifiable OS - Red Hat UBI 9",
			OSImageLabels: ubi9ImageLabels,
			ErrorExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			t.Parallel()
			os, err := InferFromOSImageLabels(testCase.OSImageLabels)
			if testCase.ErrorExpected {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			assert.Equal(t, testCase.IsEL, os.IsEL(), "expected IsEL() to be %v", testCase.IsEL)
			assert.Equal(t, testCase.IsEL9, os.IsEL9(), "expected IsEL9() to be %v", testCase.IsEL9)
			assert.Equal(t, testCase.IsCoreOSVariant, os.IsCoreOSVariant(), "expected IsCoreOSVariant() to be %v", testCase.IsCoreOSVariant)
			assert.Equal(t, testCase.IsFCOS, os.IsFCOS(), "expected IsFCOS() to be %v", testCase.IsFCOS)
			assert.Equal(t, testCase.IsSCOS, os.IsSCOS(), "expected IsSCOS() to be %v", testCase.IsSCOS)
			assert.Equal(t, testCase.IsLikeTraditionalRHEL7, os.IsLikeTraditionalRHEL7(), "expected IsLikeTraditionalRHEL7() to be %v", testCase.IsLikeTraditionalRHEL7)
			assert.Equal(t, testCase.ToPrometheusLabel, os.ToPrometheusLabel(), "expected ToPrometheusLabel() to be %s, got %s", testCase.ToPrometheusLabel, os.ToPrometheusLabel())
			assert.Equal(t, ImageLabelInfoSource, os.Source())
			assert.Equal(t, testCase.OSImageLabels, os.Values())
			assert.Equal(t, testCase.ExpectedID, os.id)
			assert.Equal(t, testCase.ExpectedVersion, os.version)
			assert.Equal(t, coreos, os.variantID)
		})
	}
}
