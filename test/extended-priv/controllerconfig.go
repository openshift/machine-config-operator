package extended

import (
	"encoding/json"
	"fmt"

	b64 "encoding/base64"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

// ControllerConfig struct is used to handle ControllerConfig resources in OCP
type ControllerConfig struct {
	Resource
}

// CertificateInfo stores the information regarding a given certificate
type CertificateInfo struct {
	// subject is the cert subject
	Subject string `json:"subject"`

	// signer is the  cert Issuer
	Signer string `json:"signer"`

	// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
	// notBefore is the lower boundary for validity
	NotBefore string `json:"notBefore"`

	// notAfter is the upper boundary for validity
	NotAfter string `json:"notAfter"`

	// bundleFile is the larger bundle a cert comes from
	BundleFile string `json:"bundleFile"`
}

// NewControllerConfig create a ControllerConfig struct
func NewControllerConfig(oc *exutil.CLI, name string) *ControllerConfig {
	return &ControllerConfig{Resource: *NewResource(oc, "ControllerConfig", name)}
}

// GetKubeAPIServerServingCAData return the base64 decoded value of the kubeAPIServerServingCAData bundle stored in the ControllerConfig
func (cc *ControllerConfig) GetKubeAPIServerServingCAData() (string, error) {
	b64KubeAPIServerServingData, err := cc.Get(`{.spec.kubeAPIServerServingCAData}`)
	if err != nil {
		return "", err
	}

	kubeAPIServerServingCAData, err := b64.StdEncoding.DecodeString(b64KubeAPIServerServingData)
	if err != nil {
		return "", err
	}
	return string(kubeAPIServerServingCAData), err
}

// GetRootCAData return the base64 decoded value of the rootCA bundle stored in the ControllerConfig
func (cc *ControllerConfig) GetRootCAData() (string, error) {
	b64RootCAData, err := cc.Get(`{.spec.rootCAData}`)
	if err != nil {
		return "", err
	}

	rootCAData, err := b64.StdEncoding.DecodeString(b64RootCAData)
	if err != nil {
		return "", err
	}
	return string(rootCAData), err
}

// GetImageRegistryBundleData returns a map[string]string containing the filenames and values of the image registry bundle data
func (cc *ControllerConfig) GetImageRegistryBundleData() (map[string]string, error) {
	return cc.GetImageRegistryBundle("imageRegistryBundleData")
}

// GetImageRegistryBundleUserData returns a map[string]string containing the filenames and values of the image registry bundle user data
func (cc *ControllerConfig) GetImageRegistryBundleUserData() (map[string]string, error) {
	return cc.GetImageRegistryBundle("imageRegistryBundleUserData")
}

// GetImageRegistryBundle returns a map[string]string containing the filenames and values of the image registry certificates in a Bundle field
func (cc *ControllerConfig) GetImageRegistryBundle(bundleField string) (map[string]string, error) {
	certs := map[string]string{}

	bundleData, err := cc.Get(`{.spec.` + bundleField + `}`)
	if err != nil {
		return nil, err
	}

	parsedBundleData := gjson.Parse(bundleData)

	var b64Err error
	parsedBundleData.ForEach(func(_, item gjson.Result) bool {
		file := item.Get("file").String()
		data64 := item.Get("data").String()

		data, b64Err := b64.StdEncoding.DecodeString(data64)
		if err != nil {
			logger.Infof("Error decoding data for image registry bundle file %s: %s", file, b64Err)
			return false // stop iterating
		}

		certs[file] = string(data)
		return true // keep iterating
	})
	if b64Err != nil {
		return nil, b64Err
	}

	return certs, nil
}

// GetImageRegistryBundleByFileName returns the image registry bundle searching by bundle filename
func (cc *ControllerConfig) GetImageRegistryBundleDataByFileName(fileName string) (string, error) {
	certs, err := cc.GetImageRegistryBundleData()
	if err != nil {
		return "", err
	}

	data, ok := certs[fileName]
	if !ok {
		return "", fmt.Errorf("There is no image registry bundle with file name %s", fileName)
	}

	return data, nil
}

// GetImageRegistryUserBundleByFileName returns the image registry bundle searching by bundle filename
func (cc *ControllerConfig) GetImageRegistryBundleUserDataByFileName(fileName string) (string, error) {
	certs, err := cc.GetImageRegistryBundleUserData()
	if err != nil {
		return "", err
	}

	data, ok := certs[fileName]
	if !ok {
		return "", fmt.Errorf("There is no image registry bundle with file name %s", fileName)
	}

	return data, nil
}

// Returns a list of CertificateInfo structs with the information of all the certificates tracked by ControllerConfig
func (cc *ControllerConfig) GetCertificatesInfo() ([]CertificateInfo, error) {
	certsInfoString := cc.GetOrFail(`{.status.controllerCertificates}`)

	logger.Debugf("CERTIFICATES: %s", certsInfoString)

	var certsInfo []CertificateInfo

	jsonerr := json.Unmarshal([]byte(certsInfoString), &certsInfo)

	if jsonerr != nil {
		return nil, jsonerr
	}

	return certsInfo, nil
}

func (cc *ControllerConfig) GetCertificatesInfoByBundleFileName(bundleFile string) ([]CertificateInfo, error) {

	var certsInfo []CertificateInfo

	allCertsInfo, err := cc.GetCertificatesInfo()
	if err != nil {
		return nil, err
	}

	for _, ciLoop := range allCertsInfo {
		ci := ciLoop
		if ci.BundleFile == bundleFile {
			certsInfo = append(certsInfo, ci)
		}
	}

	return certsInfo, nil
}
