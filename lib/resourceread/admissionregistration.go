package resourceread

import (
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	admissionregistrationv1beta1Scheme = runtime.NewScheme()
	admissionregistrationv1beta1Codec  = serializer.NewCodecFactory(admissionregistrationv1beta1Scheme)
)

func init() {
	if err := admissionregistrationv1beta1.AddToScheme(admissionregistrationv1beta1Scheme); err != nil {
		panic(err)
	}
}
func ReadValidatingAdmissionPolicyV1OrDie(objBytes []byte) *admissionregistrationv1beta1.ValidatingAdmissionPolicy {
	requiredObj, err := runtime.Decode(admissionregistrationv1beta1Codec.UniversalDecoder(admissionregistrationv1beta1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*admissionregistrationv1beta1.ValidatingAdmissionPolicy)
}

func ReadValidatingAdmissionPolicyBindingV1OrDie(objBytes []byte) *admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding {
	requiredObj, err := runtime.Decode(admissionregistrationv1beta1Codec.UniversalDecoder(admissionregistrationv1beta1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding)
}
