package bootimage

import (
	"context"
	"fmt"

	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	capav1beta2 "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
)

// reconcileAWSCAPIMachineInfraTemplate reconciles an AWSMachineTemplate for a CAPI MachineSet or MachineDeployment.
func reconcileAWSCAPIMachineInfraTemplate(infra *osconfigv1.Infrastructure, resourceKind, msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (bool, bool, *unstructured.Unstructured, string, error) {
	ctx := context.TODO()
	klog.Infof("Reconciling CAPI %s %s on AWS with arch %s", resourceKind, msName, arch)

	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, false, nil, "", err
	}

	if infra.Status.PlatformStatus.AWS == nil {
		return false, false, nil, "", fmt.Errorf("AWS platform status is nil in Infrastructure object")
	}
	region := infra.Status.PlatformStatus.AWS.Region

	awsTemplate := &capav1beta2.AWSMachineTemplate{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(currentTemplate.Object, awsTemplate); err != nil {
		return false, false, nil, "", fmt.Errorf("failed to convert AWSMachineTemplate %s: %w", currentTemplate.GetName(), err)
	}

	if awsTemplate.Spec.Template.Spec.AMI.ID == nil {
		klog.Infof("current AMI.ID is undefined in infrastructure template for CAPI MachineSet %s, skipping", msName)
		return false, true, nil, "", nil
	}
	currentAMI := *awsTemplate.Spec.Template.Spec.AMI.ID

	ec2Client, err := getAWSEC2Client(ctx, region, secretClient)
	if err != nil {
		return false, false, nil, "", err
	}

	newAMI, rhcosVersion, reconcileSkipped, err := resolveAWSTargetAMI(ctx, ec2Client, streamData, arch, region, currentAMI, msName)
	if err != nil {
		return false, false, nil, "", err
	}
	if reconcileSkipped {
		return false, true, nil, "", nil
	}

	if newAMI == currentAMI {
		return false, false, nil, rhcosVersion, nil
	}

	klog.Infof("Current image: %s: %s", region, currentAMI)
	klog.Infof("New target boot image: %s: %s", region, newAMI)

	newAWSTemplate := awsTemplate.DeepCopy()
	newAWSTemplate.Spec.Template.Spec.AMI = capav1beta2.AMIReference{ID: &newAMI}

	newObj, err := kruntime.DefaultUnstructuredConverter.ToUnstructured(newAWSTemplate)
	if err != nil {
		return false, false, nil, "", fmt.Errorf("failed to convert updated AWSMachineTemplate to unstructured: %w", err)
	}
	return true, false, &unstructured.Unstructured{Object: newObj}, rhcosVersion, nil
}
