package daemon

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ghodss/yaml"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"
)

var ccRequeueDelay = 1 * time.Minute

const (
	osImagePullSecretDir string = "/run/os-image-pull-secrets"
)

func (dn *Daemon) handleControllerConfigEvent(obj interface{}) {
	controllerConfig := obj.(*mcfgv1.ControllerConfig)
	klog.V(4).Infof("Updating ControllerConfig %s", controllerConfig.Name)
	dn.enqueueControllerConfig(controllerConfig)
}

func (dn *Daemon) enqueueControllerConfig(controllerConfig *mcfgv1.ControllerConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerConfig, err))
		return
	}
	dn.ccQueue.AddRateLimited(key)
}

func (dn *Daemon) enqueueControllerConfigAfter(controllerConfig *mcfgv1.ControllerConfig, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerConfig, err))
		return
	}

	dn.ccQueue.AddAfter(key, after)
}

func (dn *Daemon) controllerConfigWorker() {
	for dn.processNextControllerConfigWorkItem() {
	}
}

func (dn *Daemon) processNextControllerConfigWorkItem() bool {
	key, quit := dn.ccQueue.Get()
	if quit {
		return false
	}
	defer dn.ccQueue.Done(key)

	err := dn.syncControllerConfigHandler(key.(string))
	dn.handleControllerConfigErr(err, key)

	return true
}

func (dn *Daemon) handleControllerConfigErr(err error, key interface{}) {
	if err == nil {
		dn.ccQueue.Forget(key)
		return
	}

	if err := dn.updateErrorState(err); err != nil {
		klog.Errorf("Could not update annotation: %v", err)
	}
	// This is at V(2) since the updateErrorState() call above ends up logging too
	klog.V(2).Infof("Error syncing ControllerConfig %v (retries %d): %v", key, dn.ccQueue.NumRequeues(key), err)
	dn.ccQueue.AddRateLimited(key)
}

// nolint:gocyclo
func (dn *Daemon) syncControllerConfigHandler(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing ControllerConfig %q (%v)", key, startTime)

	if key != ctrlcommon.ControllerConfigName {
		// In theory there are no other ControllerConfigs other than the machine-config one
		// but to future-proof just in case, we don't need to sync on other changes
		return nil
	}

	controllerConfig, err := dn.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %v", err)
	}

	if dn.node == nil {
		// Node has not yet initialized, wait to resync
		dn.enqueueControllerConfigAfter(controllerConfig, ccRequeueDelay)
		return nil
	}

	// Write the latest cert to disk, if the controllerconfig resourceVersion has updated
	// Also annotate the latest config we've seen, so as to not write unnecessarily
	currentNodeControllerConfigResource := dn.node.Annotations[constants.ControllerConfigResourceVersionKey]
	var cmErr error
	var data []byte
	kubeConfigDiff := false
	allCertsThere := true
	onDiskKC := clientcmdv1.Config{}

	if currentNodeControllerConfigResource != controllerConfig.ObjectMeta.ResourceVersion || controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] == ctrlcommon.ServiceCARotateTrue {
		pathToData := make(map[string][]byte)
		kubeAPIServerServingCABytes := controllerConfig.Spec.KubeAPIServerServingCAData
		cloudCA := controllerConfig.Spec.CloudProviderCAData
		pathToData[caBundleFilePath] = kubeAPIServerServingCABytes
		pathToData[cloudCABundleFilePath] = cloudCA
		var err error
		var cm *corev1.ConfigMap
		var fullCA []string

		imageRegistryPullSecretData, err := reconcileOSImageRegistryPullSecretData(dn.node, controllerConfig, osImagePullSecretDir)
		if err != nil {
			return fmt.Errorf("could not reconcile OS image registry pull secret data: %w", err)
		}

		pathToData[imageRegistryAuthFile] = imageRegistryPullSecretData

		if controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] == ctrlcommon.ServiceCARotateTrue && dn.node.Annotations[constants.ControllerConfigSyncServerCA] != controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] {
			cm, cmErr = dn.kubeClient.CoreV1().ConfigMaps("openshift-machine-config-operator").Get(context.TODO(), "kubeconfig-data", v1.GetOptions{})
			if cmErr != nil {
				klog.Errorf("Error retrieving kubeconfig-data. %v", cmErr)
			} else {
				data, err = cmToData(cm, "ca-bundle.crt")
				if err != nil {
					klog.Errorf("kubeconfig-data ConfigMap not populated yet. %v", err)
				} else if data != nil {
					kcBytes, err := os.ReadFile(kubeConfigPath)
					if err != nil {
						return err
					}
					if kcBytes != nil {
						err = yaml.Unmarshal(kcBytes, &onDiskKC)
						if err != nil {
							return fmt.Errorf("could not unmarshal kubeconfig into struct. Data: %s, Error: %v", string(kcBytes), err)
						}
						kubeConfigDiff = !bytes.Equal(bytes.TrimSpace(onDiskKC.Clusters[0].Cluster.CertificateAuthorityData), bytes.TrimSpace(data))

						// We should always write the latest certs from the configmap onto disk, but we should check what was changed/modified
						// if any certs were added or updated, determine if we need to defer the kubelet restarting
						certsConfigmap := strings.SplitAfter(strings.TrimSpace(string(data)), "-----END CERTIFICATE-----")
						certsDisk := strings.SplitAfter(strings.TrimSpace(string(onDiskKC.Clusters[0].Cluster.CertificateAuthorityData)), "-----END CERTIFICATE-----")
						var addedOrUpdatedCAs []string

						for _, cert := range certsConfigmap {
							found := false
							for _, onDiskCert := range certsDisk {
								if onDiskCert == cert {
									found = true
									break
								}
							}
							if !found {
								addedOrUpdatedCAs = append(addedOrUpdatedCAs, cert)
								allCertsThere = false
							}

							b, _ := pem.Decode([]byte(cert))
							if b == nil {
								klog.Infof("Unable to decode cert into a pem block. Cert is either empty or invalid.")
								break
							}
							_, err := x509.ParseCertificate(b.Bytes)
							if err != nil {
								logSystem("Malformed Cert, not syncing: %s", cert)
								continue
							}

							fullCA = append(fullCA, cert)
						}

						dn.deferKubeletRestart = true
						for _, cert := range addedOrUpdatedCAs {
							b, _ := pem.Decode([]byte(cert))
							if b == nil {
								klog.Infof("Unable to decode cert into a pem block. Cert is either empty or invalid.")
								break
							}
							c, err := x509.ParseCertificate(b.Bytes)
							if err != nil {
								logSystem("Malformed Cert, not syncing: %s", cert)
								continue
							}
							logSystem("Cert not found in kubeconfig. This means we need to write to disk. Subject is: %s", c.Subject.CommonName)
							// these rotate randomly during upgrades. need to ignore. DO NOT restart kubelet until we error.
							// TODO(jerzhang): handle this better
							if !strings.Contains(c.Subject.CommonName, "kube-apiserver-localhost-signer") && !strings.Contains(c.Subject.CommonName, "openshift-kube-apiserver-operator_localhost-recovery-serving-signer") && !strings.Contains(c.Subject.CommonName, "kube-apiserver-lb-signer") {
								logSystem("Need to restart kubelet")
								dn.deferKubeletRestart = false
							} else {
								logSystem("Skipping kubelet restart")
							}
						}

						if kubeConfigDiff && !allCertsThere {
							var newData []byte
							if onDiskKC.Clusters == nil {
								return errors.New("clusters cannot be nil")
							}
							// use ALL data we have, including new certs.
							onDiskKC.Clusters[0].Cluster.CertificateAuthorityData = []byte(strings.Join(fullCA, ""))
							newData, err = yaml.Marshal(onDiskKC)
							if err != nil {
								return fmt.Errorf("could not marshal kubeconfig into bytes. Error: %v", err)
							}

							pathToData[kubeConfigPath] = newData
							klog.Infof("Writing new Data to /etc/kubernetes/kubeconfig: %s", string(newData))
						}
					} else {
						klog.Info("Could not read kubeconfig file, or data does not need to be changed")
					}
				}
			}
		}
		if err := writeToDisk(pathToData); err != nil {
			return err
		}

		mergedData := append(controllerConfig.Spec.ImageRegistryBundleData, controllerConfig.Spec.ImageRegistryBundleUserData...)

		entries, err := os.ReadDir("/etc/docker/certs.d")
		if err != nil {
			klog.Errorf("/etc/docker/certs.d does not exist yet: %v", err)
		} else {
			for _, entry := range entries {
				if entry.IsDir() {
					stillExists := false
					for _, CA := range mergedData {
						// if one of our spec CAs matches the existing file, we are good.
						if CA.File == entry.Name() {
							stillExists = true
						}
					}
					if !stillExists {
						if err := os.RemoveAll(filepath.Join("/etc/docker/certs.d", entry.Name())); err != nil {
							klog.Warningf("Could not remove old certificate: %s", filepath.Join("/etc/docker/certs.d", entry.Name()))
						}
					}
				}
			}

			for _, CA := range controllerConfig.Spec.ImageRegistryBundleData {
				caFile := strings.ReplaceAll(CA.File, "..", ":")
				if err := os.MkdirAll(filepath.Join(imageCAFilePath, caFile), defaultDirectoryPermissions); err != nil {
					return err
				}
				if err := writeFileAtomicallyWithDefaults(filepath.Join(imageCAFilePath, caFile, "ca.crt"), CA.Data); err != nil {
					return err
				}
			}

			for _, CA := range controllerConfig.Spec.ImageRegistryBundleUserData {
				caFile := strings.ReplaceAll(CA.File, "..", ":")
				if err := os.MkdirAll(filepath.Join(imageCAFilePath, caFile), defaultDirectoryPermissions); err != nil {
					return err
				}
				if err := writeFileAtomicallyWithDefaults(filepath.Join(imageCAFilePath, caFile, "ca.crt"), CA.Data); err != nil {
					return err
				}
			}

		}
	}

	oldAnno := dn.node.Annotations[constants.ControllerConfigSyncServerCA]
	annos := map[string]string{
		constants.ControllerConfigResourceVersionKey: controllerConfig.ObjectMeta.ResourceVersion,
	}
	if dn.node.Annotations[constants.ControllerConfigSyncServerCA] != controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] {
		annos[constants.ControllerConfigSyncServerCA] = controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation]

	}
	if _, err := dn.nodeWriter.SetAnnotations(annos); err != nil {
		return fmt.Errorf("failed to set annotations on node: %w", err)
	}

	klog.Infof("Certificate was synced from controllerconfig resourceVersion %s", controllerConfig.ObjectMeta.ResourceVersion)
	if controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] == ctrlcommon.ServiceCARotateTrue && oldAnno != controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] && cmErr == nil && kubeConfigDiff && !allCertsThere && !dn.deferKubeletRestart {
		if len(onDiskKC.Clusters[0].Cluster.CertificateAuthorityData) > 0 {
			logSystem("restarting kubelet due to server-ca rotation")
			if err := runCmdSync("systemctl", "stop", "kubelet"); err != nil {
				return err
			}
			f, err := os.ReadFile("/var/lib/kubelet/kubeconfig")
			if err != nil && os.IsNotExist(err) {
				klog.Warningf("Failed to get kubeconfig file: %v", err)
				return err
			} else if err != nil {
				return fmt.Errorf("unexpected error reading kubeconfig file, %v", err)
			}
			kubeletKC := clientcmdv1.Config{}
			err = yaml.Unmarshal(f, &kubeletKC)
			if err != nil {
				return err
			}
			// set CA data to the one we just parsed above, the rest of the data should be preserved.
			kubeletKC.Clusters[0].Cluster.CertificateAuthorityData = onDiskKC.Clusters[0].Cluster.CertificateAuthorityData
			newData, err := yaml.Marshal(kubeletKC)
			if err != nil {
				return fmt.Errorf("could not marshal kubeconfig into bytes. Error: %v", err)
			}
			filesToWrite := make(map[string][]byte)
			filesToWrite["/var/lib/kubelet/kubeconfig"] = newData
			err = writeToDisk(filesToWrite)
			if err != nil {
				return err
			}

			if err := runCmdSync("systemctl", "daemon-reload"); err != nil {
				return err
			}

			if err := runCmdSync("systemctl", "start", "kubelet"); err != nil {
				return err
			}
		}

		klog.V(4).Infof("Finished syncing ControllerConfig %q (%v)", key, time.Since(startTime))
	}

	klog.V(4).Infof("Finished syncing ControllerConfig %q (%v)", key, time.Since(startTime))
	return nil
}

func cmToData(cm *corev1.ConfigMap, key string) ([]byte, error) {
	if bd, bdok := cm.BinaryData[key]; bdok {
		return bd, nil
	}
	if d, dok := cm.Data[key]; dok {
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			return []byte(d), nil
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%s not found in %s/%s", key, cm.Namespace, cm.Name)
}

func writeToDisk(pathToData map[string][]byte) error {
	for bundle, data := range pathToData {
		if !strings.HasSuffix(string(data), "\n") {
			bString := string(data) + "\n"
			data = []byte(bString)
		}
		if Finfo, err := os.Stat(bundle); err == nil {
			var mode os.FileMode
			Dinfo, err := os.Stat(filepath.Dir(bundle))
			if err != nil {
				mode = defaultDirectoryPermissions
			} else {
				mode = Dinfo.Mode()
			}
			// we need to make sure we honor the mode of that file
			if err := writeFileAtomically(bundle, data, mode, Finfo.Mode(), -1, -1); err != nil {
				return err
			}
		} else {
			if err := writeFileAtomicallyWithDefaults(bundle, data); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reconciles and merges the secrets provided by the ControllerConfig along
// with any mounted image pull secrets that the MCO may have configured the MCD
// to use in a layered OS image scenario.
func reconcileOSImageRegistryPullSecretData(node *corev1.Node, controllerCfg *mcfgv1.ControllerConfig, pathPrefix string) ([]byte, error) {
	// First, check if the node is opted into layering by checking for the
	// presence of the desired image annotation key. If not present, then no
	// further reconciliation is necessary.
	if _, ok := node.Annotations[constants.DesiredImageAnnotationKey]; !ok {
		// Just use the internal image pull secrets to mimic the previous behavior.
		return controllerCfg.Spec.InternalRegistryPullSecret, nil
	}

	// Next, get all of the node-roles that this node might have so we can
	// resolve them to MachineConfigPools.
	nodeRoles := getNodeRoles(node)

	// This isn't likely to happen, but a guard clause here is nice.
	if len(nodeRoles) == 0 {
		return nil, fmt.Errorf("node %s has no node-role.kubernetes.io label", node.Name)
	}

	// Next, we need to read the secret from disk, if we can find one.
	mountedSecret, err := readMountedSecretByNodeRole(nodeRoles, pathPrefix)
	if err != nil {
		return nil, err
	}

	// If there are no on-disk secrets, just use the internal image pull secrets
	// as-is.
	if len(mountedSecret) == 0 {
		return controllerCfg.Spec.InternalRegistryPullSecret, nil
	}

	// Next, we need to merge the secret we just found with the contents of the
	// InternalImagePullSecret field on the ControllerConfig object, ensuring
	// that the values from the mounted secret override any values provided by
	// the InternalImagePullSecret field.
	merged, err := mergeMountedSecretsWithControllerConfig(mountedSecret, controllerCfg)
	if err != nil {
		return nil, fmt.Errorf("could not merge on-disk secrets with ControllerConfig: %w", err)
	}

	return merged, nil
}

// This iterates through all of the node roles until it finds a secret that
// matches. We do this because we have to mount all of the secrets for all of
// the MachineOSConfigs into each MCD pod. Additionally, we want to support the
// case where someone uses wither a .dockercfg or .dockerconfigjson style
// secret. However, it is possible that a different MachineOSConfig can specify
// a different image pull secret. And it is also possible that a node can
// belong to two MachineConfigPools.
func readMountedSecretByNodeRole(nodeRoles []string, pathPrefix string) ([]byte, error) {
	secretDir := filepath.Join(pathPrefix, osImagePullSecretDir)

	// If this directory does not exist, it means that the MCD DaemonSet does not
	// have the secrets mounted. In this case, just return an empty byte array.
	// With the above annotation check, this might be redundant, but it shouldn't
	// hurt anything by leaving it here.
	_, err := os.Stat(secretDir)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}

	// Support both image secret types.
	imagePullSecretKeys := []string{
		corev1.DockerConfigJsonKey,
		corev1.DockerConfigKey,
	}

	for _, key := range imagePullSecretKeys {
		for _, nodeRole := range nodeRoles {
			// This ends up being a concatentation of
			// /run/os-image-pull-secrets/<poolname>/.dockerconfigjson or
			// /run/os-image-pull-secrets/<poolname>/.dockercfg
			path := filepath.Join(secretDir, nodeRole, key)

			_, err := os.Stat(path)

			// If the file exists, we've found it. Read it and stop here.
			if err == nil {
				klog.Infof("Merging image pull secrets from ControllerConfig with mounted secrets from %s", path)
				return os.ReadFile(path)
			}

			// If the file does not exist, keep going until we find one that does.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			// If an unknown error has occurred, stop here and bail out.
			if err != nil {
				return nil, fmt.Errorf("could not stat %s: %w", path, err)
			}
		}
	}

	// If we get this far, it means we've exhausted all of our node roles and our
	// secret keys without finding a suitable image pull secret. So error out
	// here..
	return nil, fmt.Errorf("no suitable image pull secret found given node role(s): %v", nodeRoles)
}

// Merges the mounted secret with the ones from the ControllerConfig, with the mounted ones taking priority.
func mergeMountedSecretsWithControllerConfig(mountedSecret []byte, controllerCfg *mcfgv1.ControllerConfig) ([]byte, error) {
	mountedSecrets, err := ctrlcommon.ToDockerConfigJSON(mountedSecret)
	if err != nil {
		return nil, fmt.Errorf("could not parse mounted secret: %w", err)
	}

	internalRegistryPullSecrets, err := ctrlcommon.ToDockerConfigJSON(controllerCfg.Spec.InternalRegistryPullSecret)
	if err != nil {
		return nil, fmt.Errorf("could not parse internal registry pull secret from ControllerConfig: %w", err)
	}

	out := &ctrlcommon.DockerConfigJSON{
		Auths: ctrlcommon.DockerConfig{},
	}

	for key, internalRegistryAuth := range internalRegistryPullSecrets.Auths {
		out.Auths[key] = internalRegistryAuth
	}

	for key, mountedSecretAuth := range mountedSecrets.Auths {
		out.Auths[key] = mountedSecretAuth
	}

	return json.Marshal(out)
}

// Iterates through all of the labels on a given node, searching for the
// "node-role.kubernetes.io" label, and extracting the role name from the
// label. It is possible for a single node to have more than one node-role
// label, so we extract them all.
func getNodeRoles(node *corev1.Node) []string {
	if node.Labels == nil {
		return []string{}
	}

	nodeRoleLabel := "node-role.kubernetes.io/"

	out := []string{}

	for key := range node.Labels {
		if strings.Contains(key, nodeRoleLabel) {
			out = append(out, strings.ReplaceAll(key, nodeRoleLabel, ""))
		}
	}

	return out
}
