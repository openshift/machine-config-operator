package daemon

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
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

	err := dn.syncControllerConfigHandler(key)
	dn.handleControllerConfigErr(err, key)

	return true
}

func (dn *Daemon) handleControllerConfigErr(err error, key string) {
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

		// If the ControllerConfig version changed, we should sync our OS image
		// pull secrets, since they could have changed.
		if err := dn.syncInternalRegistryPullSecrets(controllerConfig); err != nil {
			return err
		}

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
							klog.Infof("Writing new Data to /etc/kubernetes/kubeconfig")
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

		mergedData := append([]mcfgv1.ImageRegistryBundle{}, append(controllerConfig.Spec.ImageRegistryBundleData, controllerConfig.Spec.ImageRegistryBundleUserData...)...)

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

// Syncs the OS image pull secrets to disk under
// /etc/mco/internal-registry-pull-secret.json using the contents of the
// ControllerConfig. This will run during the certificate_writer sync loop
// as well as during an OS update. Because this can execute across multiple
// Goroutines, a Daemon-level mutex (osImageMux) is used to ensure that only
// one call can execute at any given time.
func (dn *Daemon) syncInternalRegistryPullSecrets(controllerConfig *mcfgv1.ControllerConfig) error {
	dn.osImageMux.Lock()
	defer dn.osImageMux.Unlock()

	if controllerConfig == nil {
		cfg, err := dn.ccLister.Get(ctrlcommon.ControllerConfigName)
		if err != nil {
			return fmt.Errorf("could not get ControllerConfig: %v", err)
		}

		controllerConfig = cfg
	}

	if err := writeToDisk(map[string][]byte{internalRegistryAuthFile: controllerConfig.Spec.InternalRegistryPullSecret}); err != nil {
		return fmt.Errorf("could not write image pull secret data to node filesystem: %w", err)
	}

	klog.V(4).Infof("Synced image registry secrets to node filesystem in %s", internalRegistryAuthFile)

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
