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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
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
	dontRestartKubelet := false
	currentKC := clientcmdv1.Config{}

	if currentNodeControllerConfigResource != controllerConfig.ObjectMeta.ResourceVersion || controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] == ctrlcommon.ServiceCARotateTrue {
		pathToData := make(map[string][]byte)
		kubeAPIServerServingCABytes := controllerConfig.Spec.KubeAPIServerServingCAData
		cloudCA := controllerConfig.Spec.CloudProviderCAData
		pathToData[caBundleFilePath] = kubeAPIServerServingCABytes
		pathToData[cloudCABundleFilePath] = cloudCA
		var err error
		var cm *corev1.ConfigMap
		var FullCA []string

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
						err = yaml.Unmarshal(kcBytes, &currentKC)
						if err != nil {
							return fmt.Errorf("could not unmarshal kubeconfig into struct. Data: %s, Error: %v", string(kcBytes), err)
						}
						kubeConfigDiff = !bytes.Equal(bytes.TrimSpace(currentKC.Clusters[0].Cluster.CertificateAuthorityData), bytes.TrimSpace(data))
						// if bytes.Compare is 1 AND theres no new data we do not want to write to disk. Just keep the old contents they will work.
						// disk (currentKC) should be longer than configmap (data)
						// we need to get the current data (kubeconfig-data) and compare it to clusters[0] on disk
						// if ALL of kubeconfig-data (per cert) is contained on disk, do nothing
						numericalDiff := bytes.Compare(currentKC.Clusters[0].Cluster.CertificateAuthorityData, data)
						// sometimes it seems some of the data here might be invalid.
						if numericalDiff == 1 && len(currentKC.Clusters[0].Cluster.CertificateAuthorityData) > 0 {
							certsConfigmap := strings.SplitAfter(strings.TrimSpace(string(data)), "-----END CERTIFICATE-----")
							certsDisk := strings.SplitAfter(strings.TrimSpace(string(currentKC.Clusters[0].Cluster.CertificateAuthorityData)), "-----END CERTIFICATE-----")
							// worst case scenario we are doubling the length
							FullCA = make([]string, len(certsDisk)+len(certsConfigmap))
							// write the whole old KC to this array. Then splice in new certs.
							for i, ca := range certsDisk {
								FullCA[i] = ca
							}
							for i, cert := range certsConfigmap {
								// for each cert in the CM, we either ignore it OR splice it into the CertificateAuthorityData
								// if on disk contains this cert, we are good
								// if not, then that means we are adding certs.
								if strings.Contains(strings.TrimSpace(string(currentKC.Clusters[0].Cluster.CertificateAuthorityData)), cert) {
									continue
								}
								// we need to check for the subject.
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
								replaced := false
								for i, diskCert := range FullCA {
									b, _ := pem.Decode([]byte(diskCert))
									if b == nil {
										klog.Infof("Unable to decode cert into a pem block. Cert is either empty or invalid.")
										break
									}
									cDisk, err := x509.ParseCertificate(b.Bytes)
									if err != nil {
										logSystem("Malformed Cert, not syncing: %s", diskCert)
										continue
									}
									// there seems to be some spacing issues. might need to fix this. Certs are clearly in both the on disk
									// and the new data, but string comparisons return false.
									if c.Subject.CommonName == cDisk.Subject.CommonName {
										// this is the same cert, BUT since the above check didn't catch this, it means the content has changed
										// the one thing we do not want is two certs, same subject, but one is incorrect.
										// so we replace the final Array's entry with this one instead.
										FullCA[i] = cert
										replaced = true
										break

									}
								}
								logSystem("Cert not found in kubeconfig. This means we need to write to disk")

								logSystem("Cert subject is: %s possibly skipping kubelet restart", c.Subject.CommonName)
								if c.Subject.CommonName == "kube-apiserver-localhost-signer" || strings.Contains(c.Subject.CommonName, "openshift-kube-apiserver-operator_localhost") {
									// this rotates randomly during upgrades. need to ignore. DO NOT restart kubelet until we error.
									dontRestartKubelet = true
									dn.deferKubeletRestart = true
								} else {
									dn.deferKubeletRestart = false
								}
								if replaced {
									continue
								}
								// splice it in, best attempt at putting this where it should go
								FullCA = append(FullCA[:i+1], FullCA[i:]...)
								FullCA[i] = cert
								allCertsThere = false

							}

						}
						if kubeConfigDiff && !allCertsThere {
							// we can still have a scenario where we are adding 1 CA but dont want to remove all the old ones.
							klog.Infof("On disk cert and configmap cert differ. Diff: %d (0 means same legnth, -1 means on disk is shorter than configmap 1 means on disk is longer than configmap)", numericalDiff)
							var newData []byte
							if currentKC.Clusters == nil {
								return errors.New("clusters cannot be nil")
							}
							// use ALL data we have, including new certs.
							currentKC.Clusters[0].Cluster.CertificateAuthorityData = []byte(strings.Join(FullCA, ""))
							// take data from currentKC which now has new ca in it and marshal it
							newData, err = yaml.Marshal(currentKC)
							if err != nil {
								return fmt.Errorf("could not marshal kubeconfig into bytes. Error: %v", err)
							}
							// take old kubeconfig-data cm and marshal it
							oldCMBin, err := json.Marshal(cm)
							if err != nil {
								return fmt.Errorf("could not marshal old configmap %v", err)
							}
							// newCM == old kubeconfig-data and add in a kubeconfig field
							newCM := cm
							newCM.BinaryData["kubeconfig"], err = yaml.Marshal(currentKC)
							if err != nil {
								return fmt.Errorf("could not marshal new kubeconfig %v", err)
							}
							// marshal it
							newCMBin, err := json.Marshal(newCM)
							if err != nil {
								return fmt.Errorf("could not marshal new configmap %v", err)
							}
							// patch kc data with old kubeconfig-data, new kubeconfig-data, and old kubeconfig-data
							patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldCMBin, newCMBin, oldCMBin)
							if err != nil {
								return fmt.Errorf("could not create patch for kubeconfig-data %v", err)
							}
							_, err = dn.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Patch(context.TODO(), "kubeconfig-data", types.MergePatchType, patch, v1.PatchOptions{})
							if err != nil {
								return fmt.Errorf("could not patch existing kubeconfig-data %v", err)
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
	if controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] == ctrlcommon.ServiceCARotateTrue && oldAnno != controllerConfig.Annotations[ctrlcommon.ServiceCARotateAnnotation] && cmErr == nil && kubeConfigDiff && !allCertsThere && !dontRestartKubelet {
		if len(currentKC.Clusters[0].Cluster.CertificateAuthorityData) > 0 {
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
			kubeletKC.Clusters[0].Cluster.CertificateAuthorityData = currentKC.Clusters[0].Cluster.CertificateAuthorityData
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

			if err := runCmdSync("systemctl", "restart", "kubelet"); err != nil {
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
