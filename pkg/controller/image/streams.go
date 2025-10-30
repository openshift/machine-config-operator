package image

import (
	"fmt"

	imagev1 "github.com/openshift/api/image/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func ReadImageStreamV1O(objBytes []byte) (*imagev1.ImageStream, error) {
	requiredObj, err := runtime.Decode(imagesCodecs.UniversalDecoder(imagev1.SchemeGroupVersion), objBytes)
	if err != nil {
		return nil, err
	}
	stream, ok := requiredObj.(*imagev1.ImageStream)
	if !ok {
		return nil, fmt.Errorf("expected ImageStream, got %#v", requiredObj)
	}
	return stream, nil
}

func init() {
	if err := imagev1.AddToScheme(imagesScheme); err != nil {
		panic(err)
	}
}
