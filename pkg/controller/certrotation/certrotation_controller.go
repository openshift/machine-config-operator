package certrotationcontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/vincent-petithory/dataurl"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	configv1 "github.com/openshift/api/config/v1"
	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/certrotation"
	"github.com/openshift/library-go/pkg/operator/events"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	componentName    = "machineconfigcontroller-certrotationcontroller"
	oneYear          = 365 * 24 * time.Hour
	mcsCAExpiry      = 10 * oneYear
	mcsCARefresh     = 8 * oneYear
	mcsTLSKeyExpiry  = mcsCAExpiry
	mcsTLSKeyRefresh = mcsCARefresh

	// This is used to key in to the user-data secret
	userDataKey = "userData"

	// ign* are for the user-data ignition fields
	ignFieldIgnition = "ignition"
	ignFieldSource   = "source"
)

type CertRotationController struct {
	kubeClient   kubernetes.Interface
	configClient configclientset.Interface

	mcoConfigMapInfomer coreinformersv1.ConfigMapInformer
	maoSecretInformer   coreinformersv1.SecretInformer

	mcoSecretLister corelisterv1.SecretLister
	maoSecretLister corelisterv1.SecretLister

	certRotators []factory.Controller

	recorder events.Recorder

	cachesToSync []cache.InformerSynced
}

// New returns a new cert rotation controller.
func New(
	kubeClient kubernetes.Interface,
	configClient configclientset.Interface,
	machineClient machineclientset.Interface,
	maoSecretInformer coreinformersv1.SecretInformer,
	mcoSecretInformer coreinformersv1.SecretInformer,
	mcoConfigMapInfomer coreinformersv1.ConfigMapInformer,
) (*CertRotationController, error) {

	recorder := events.NewLoggingEventRecorder(componentName, clock.RealClock{})

	c := &CertRotationController{
		kubeClient:          kubeClient,
		configClient:        configClient,
		recorder:            recorder,
		maoSecretInformer:   maoSecretInformer,
		mcoConfigMapInfomer: mcoConfigMapInfomer,
		mcoSecretLister:     mcoSecretInformer.Lister(),
		maoSecretLister:     maoSecretInformer.Lister(),
		cachesToSync: []cache.InformerSynced{
			maoSecretInformer.Informer().HasSynced,
			mcoSecretInformer.Informer().HasSynced,
			mcoConfigMapInfomer.Informer().HasSynced,
		},
	}

	// This is required for the machine-config-server-tls secret rotation
	cfg, err := configClient.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get cluster infrastructure resource: %w", err)
	}

	serverIPs := getServerIPsFromInfra(cfg)

	if cfg.Status.APIServerInternalURL == "" {
		return nil, fmt.Errorf("no APIServerInternalURL found in cluster infrastructure resource")
	}
	apiserverIntURL, err := url.Parse(cfg.Status.APIServerInternalURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", apiserverIntURL, err)
	}

	// The cert controller will begin creating "machine-config-server-ca" configmap & secret in the MCO namespace.
	// The *-user-data secrets will be updated based on these configmap/secrets.
	// For the *-user-data-managed secrets, the operator will begin to use "machine-config-server-ca" configmap
	// instead of root-CA bundle configmap

	machineConfigServerCertRotator := certrotation.NewCertRotationController(
		"MachineConfigServerCertRotator",
		certrotation.RotatedSigningCASecret{
			Namespace: ctrlcommon.MCONamespace,
			Name:      ctrlcommon.MachineConfigServerCAName,
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: "Machine Config Operator",
				Description:   "CA used to sign the MachineConfigServer TLS certificate",
			},
			Validity:      mcsCAExpiry,
			Refresh:       mcsCARefresh,
			Informer:      mcoSecretInformer,
			Lister:        c.mcoSecretLister,
			Client:        kubeClient.CoreV1(),
			EventRecorder: recorder,
		},
		certrotation.CABundleConfigMap{
			Namespace: ctrlcommon.MCONamespace,
			Name:      ctrlcommon.MachineConfigServerCAName,
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: "Machine Config Operator",
				Description:   "CA bundle that stores all valid CAs for the MachineConfigServer TLS certificate",
			},
			Informer:      mcoConfigMapInfomer,
			Lister:        mcoConfigMapInfomer.Lister(),
			Client:        kubeClient.CoreV1(),
			EventRecorder: recorder,
		},
		certrotation.RotatedSelfSignedCertKeySecret{
			Namespace: ctrlcommon.MCONamespace,
			Name:      ctrlcommon.MachineConfigServerTLSSecretName,
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: "Machine Config Operator",
				Description:   "Secret containing the MachineConfigServer TLS certificate and key",
			},
			Validity: mcsTLSKeyExpiry,
			Refresh:  mcsTLSKeyRefresh,
			CertCreator: &certrotation.ServingRotation{
				Hostnames: func() []string { return append([]string{apiserverIntURL.Hostname()}, serverIPs...) },
			},
			Informer:      mcoSecretInformer,
			Lister:        c.mcoSecretLister,
			Client:        kubeClient.CoreV1(),
			EventRecorder: recorder,
		},
		recorder,
		NewCertRotationStatusReporter(),
	)
	// Skip rotating this cert if the cluster does not use MachineSets
	if hasFunctionalMachineAPI(machineClient) || hasFunctionalClusterAPI() {
		klog.Infof("Adding MCS CA/TLS cert rotator")
		c.certRotators = append(c.certRotators, machineConfigServerCertRotator)
	} else {
		klog.Infof("MCS CA/TLS cert rotator not added")
	}

	return c, nil
}

func (c *CertRotationController) WaitForReady(stopCh <-chan struct{}) {
	klog.Infof("Waiting for %s", componentName)
	defer klog.Infof("Finished waiting for %s", componentName)

	if !cache.WaitForCacheSync(stopCh, c.cachesToSync...) {
		utilruntime.HandleError(fmt.Errorf("caches did not sync"))
		return
	}

}

func (c *CertRotationController) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	klog.Infof("Starting %s", componentName)
	defer klog.Infof("Shutting down %s", componentName)
	c.WaitForReady(ctx.Done())

	if len(c.certRotators) == 0 {
		// If there are no cert rotators, we can shutdown
		klog.Infof("No cert rotators needed, shutting down")
		return
	}

	if err := c.PreFlightChecks(); err != nil {
		utilruntime.HandleError(err)
	}

	if err := c.StartInformers(); err != nil {
		utilruntime.HandleError(err)
	}

	for _, certRotator := range c.certRotators {
		go certRotator.Run(ctx, workers)
	}

	<-ctx.Done()
}

// This should not be directly called; it is only to be used for unit tests.
func (c *CertRotationController) Sync() error {
	syncCtx := factory.NewSyncContext("mco-cert-rotation-sync", c.recorder)
	for _, certRotator := range c.certRotators {
		if err := certRotator.Sync(context.TODO(), syncCtx); err != nil {
			return err
		}
	}
	return nil

}

func getServerIPsFromInfra(cfg *configv1.Infrastructure) []string {
	if cfg.Status.PlatformStatus == nil {
		return []string{}
	}
	switch cfg.Status.PlatformStatus.Type {
	case configv1.BareMetalPlatformType:
		if cfg.Status.PlatformStatus.BareMetal == nil {
			return []string{}
		}
		return cfg.Status.PlatformStatus.BareMetal.APIServerInternalIPs
	case configv1.OvirtPlatformType:
		if cfg.Status.PlatformStatus.Ovirt == nil {
			return []string{}
		}
		return cfg.Status.PlatformStatus.Ovirt.APIServerInternalIPs
	case configv1.OpenStackPlatformType:
		if cfg.Status.PlatformStatus.OpenStack == nil {
			return []string{}
		}

		return cfg.Status.PlatformStatus.OpenStack.APIServerInternalIPs
	case configv1.VSpherePlatformType:
		if cfg.Status.PlatformStatus.VSphere == nil {
			return []string{}
		}
		if cfg.Status.PlatformStatus.VSphere.APIServerInternalIPs != nil {
			return cfg.Status.PlatformStatus.VSphere.APIServerInternalIPs
		}
		return cfg.Status.PlatformStatus.VSphere.APIServerInternalIPs
	case configv1.NutanixPlatformType:
		if cfg.Status.PlatformStatus.Nutanix == nil {
			return []string{}
		}
		return cfg.Status.PlatformStatus.Nutanix.APIServerInternalIPs
	default:
		return []string{}
	}
}

func (c *CertRotationController) StartInformers() error {

	if _, err := c.mcoConfigMapInfomer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addConfigMap,
		UpdateFunc: c.updateConfigMap,
	}); err != nil {
		return fmt.Errorf("unable to attach configmap handler: %w", err)
	}
	if _, err := c.maoSecretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addSecret,
		UpdateFunc: c.updateSecret,
	}); err != nil {
		return fmt.Errorf("unable to attach secret handler: %w", err)
	}
	return nil
}

func (c *CertRotationController) PreFlightChecks() error {

	// Ensure machine-config-server-tls secret is of the kubernetes.io/tls type. The cert-rotation-controller expects this type
	// On a cluster where the MCS CA/cert is not managed, the secret will be of the opaque type, and we'll have to do this one
	// time change.

	// Retrieve the machine-config-server-tls secret
	mcsTLSSecret, err := c.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.Background(), ctrlcommon.MachineConfigServerTLSSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot get MCS TLS secret: %w", err)
	}

	// Check if the secret type needs to be updated to kubernetes.io/tls
	if mcsTLSSecret.Type != corev1.SecretTypeTLS {
		klog.Infof("Migration to %s for %s required", corev1.SecretTypeTLS, ctrlcommon.MachineConfigServerTLSSecretName)

		// Delete the existing secret
		if err := c.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(context.Background(), ctrlcommon.MachineConfigServerTLSSecretName, metav1.DeleteOptions{}); err != nil {
			return err
		}
		// Clone a new secret of the correct type, with the same data
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ctrlcommon.MachineConfigServerTLSSecretName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: mcsTLSSecret.Data,
			Type: corev1.SecretTypeTLS,
		}
		// Recreate the secret. is this necessary? It will be updated in the first loop
		if _, err := c.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.Background(), newSecret, metav1.CreateOptions{}); err != nil {
			return err
		}
		klog.Infof("Migration to %s for %s successful", corev1.SecretTypeTLS, ctrlcommon.MachineConfigServerTLSSecretName)
	}

	return nil
}

func (c *CertRotationController) addConfigMap(obj interface{}) {

	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the machine-config-server-ca configMap
	if configMap.Name != ctrlcommon.MachineConfigServerCAName {
		return
	}

	klog.Infof("configMap %s added, reconciling all user data secrets", configMap.Name)

	go func() {
		c.reconcileUserDataSecrets()
	}()
}

func (c *CertRotationController) updateConfigMap(oldCM, newCM interface{}) {

	oldConfigMap := oldCM.(*corev1.ConfigMap)
	newConfigMap := newCM.(*corev1.ConfigMap)

	// Take no action if this isn't the machine-config-server-ca configMap
	if oldConfigMap.Name != ctrlcommon.MachineConfigServerCAName {
		return
	}

	// Only take action if the there is an actual change in the configMap
	if oldConfigMap.ResourceVersion == newConfigMap.ResourceVersion {
		return
	}

	klog.Infof("configMap %s updated, reconciling all user data secrets", oldConfigMap.Name)

	// Reconcile all user data secrets
	go func() {
		c.reconcileUserDataSecrets()
	}()
}

func (c *CertRotationController) addSecret(obj interface{}) {

	secret := obj.(*corev1.Secret)

	// Take no action if this is not a *-user-data secret
	if !isUserDataSecret(*secret) {
		return
	}

	klog.Infof("secret %s added, ensuring CA is up to date", secret.Name)

	go func() { c.reconcileSecret(*secret) }()
}

func (c *CertRotationController) updateSecret(oldS, newS interface{}) {

	oldSecret := oldS.(*corev1.Secret)
	newSecret := newS.(*corev1.Secret)

	// Take no action if this is not a *-user-data secret
	if !isUserDataSecret(*oldSecret) {
		return
	}

	// Only take action if the there is an actual change in the secret
	if oldSecret.ResourceVersion == newSecret.ResourceVersion {
		return
	}

	klog.Infof("secret %s updated, ensuring CA is up to date", newSecret.Name)

	go func() { c.reconcileSecret(*newSecret) }()
}

func (c *CertRotationController) reconcileUserDataSecrets() {

	mapiSecrets, err := c.maoSecretLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("cannot list MAO secrets: %v", err)
		return
	}
	for _, secret := range mapiSecrets {
		// Only reconcile *-user-data secrets
		if !isUserDataSecret(*secret) {
			continue
		}
		klog.V(4).Infof("Reconciling secret %s", secret.Name)
		if err := c.reconcileSecret(*secret); err != nil {
			klog.Errorf("error reconciling secret: %s %v", secret.Name, err)
		}
	}

}

func (c *CertRotationController) reconcileSecret(secret corev1.Secret) error {
	// Do a fresh get here since the lister will be likely out of date
	mcsCABundle, err := c.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot read MCS CA bundle configmap: %v", err)

	}

	// Extract the CAs from the machine-config-server-ca configmap bundle
	caData, err := ctrlcommon.GetCAsFromConfigMap(mcsCABundle, "ca-bundle.crt")
	if err != nil {
		return fmt.Errorf("could not find MCS CA bundle at %s", ctrlcommon.MachineConfigServerCAName)
	}

	// Extract user data field from *-user-data secret
	userData := secret.Data[userDataKey]
	var userDataIgn interface{}
	if err := json.Unmarshal(userData, &userDataIgn); err != nil {
		return fmt.Errorf("failed to unmarshal decoded user-data to json (secret %s): %w, skipping secret", secret.Name, err)
	}

	// Check if a content update to security.tls.certificateAuthorities is required
	ignCAPath := []string{ignFieldIgnition, "security", "tls", "certificateAuthorities"}
	caSlice, isSlice, err := unstructured.NestedFieldNoCopy(userDataIgn.(map[string]interface{}), ignCAPath...)
	if !isSlice || err != nil {
		return fmt.Errorf("failed to find certificateauthorities field in ignition (secret %s): %w", secret.Name, err)
	}
	if len(caSlice.([]interface{})) > 1 {
		return fmt.Errorf("additional CAs detected, cannot modify automatically. Aborting")
	}
	caSlice.([]interface{})[0].(map[string]interface{})[ignFieldSource] = dataurl.EncodeBytes(caData)

	updatedIgnition, err := json.Marshal(userDataIgn)
	if err != nil {
		return fmt.Errorf("failed to marshal updated ignition back to json (secret %s): %w", secret.Name, err)
	}

	if bytes.Equal(userData, updatedIgnition) {
		klog.V(4).Infof("Secret %s already updated to use the latest CA, nothing to do\n", secret.Name)
		return nil
	}

	// If an update is required, apply the new ignition content and update the secret
	secret.Data[userDataKey] = updatedIgnition
	_, err = c.kubeClient.CoreV1().Secrets(ctrlcommon.MachineAPINamespace).Update(context.TODO(), &secret, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update secret %s: %w", secret.Name, err)
	}

	klog.Infof("Successfully modified %s secret \n", secret.Name)
	return nil
}
