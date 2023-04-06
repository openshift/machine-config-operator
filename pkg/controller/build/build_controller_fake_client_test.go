package build

import (
	"context"
	"fmt"
	"testing"

	buildv1 "github.com/openshift/api/build/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWrappedFakeClient(t *testing.T) {
	t.Parallel()

	namespace := "a-namespace"
	buildConfigName := "a-build-config"

	dockerfile := "FROM scratch"

	buildConfig := &buildv1.BuildConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      buildConfigName,
		},
		Spec: buildv1.BuildConfigSpec{
			CommonSpec: buildv1.CommonSpec{
				Source: buildv1.BuildSource{
					Type:       buildv1.BuildSourceDockerfile,
					Dockerfile: &dockerfile,
				},
			},
		},
	}

	client := newWrappedFakeBuildClientset(buildConfig)

	buildRequest := &buildv1.BuildRequest{
		ObjectMeta: metav1.ObjectMeta{Name: buildConfigName},
		TriggeredBy: []buildv1.BuildTriggerCause{
			{Message: "Unit test"},
		},
	}

	// Test that the FakeClient correctly increments the builds.
	buildCount := 10
	for i := 1; i <= buildCount; i++ {
		build, err := client.BuildV1().BuildConfigs(namespace).Instantiate(context.TODO(), buildRequest.Name, buildRequest, metav1.CreateOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, build)
		assert.Equal(t, buildConfig.Spec.CommonSpec, build.Spec.CommonSpec)
		assert.Equal(t, buildRequest.TriggeredBy, build.Spec.TriggeredBy)

		buildName := fmt.Sprintf("%s-%d", buildConfigName, i)
		fetchedBuild, err := client.BuildV1().Builds(namespace).Get(context.TODO(), buildName, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, fetchedBuild)
		assert.Equal(t, buildConfig.Spec.CommonSpec, fetchedBuild.Spec.CommonSpec)
		assert.Equal(t, buildRequest.TriggeredBy, fetchedBuild.Spec.TriggeredBy)
	}

	buildList, err := client.BuildV1().Builds(namespace).List(context.TODO(), metav1.ListOptions{})
	assert.NoError(t, err)
	assert.Len(t, buildList.Items, buildCount)
}
