package bootimage

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestGetVSphereCredentialsSecret(t *testing.T) {
	legacySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vsphere-creds", Namespace: "kube-system"},
	}
	dedicatedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.VSphereCredentialsSecretName, Namespace: ctrlcommon.MCONamespace},
	}

	t.Run("dedicated secret is read", func(t *testing.T) {
		client := fake.NewSimpleClientset(dedicatedSecret)

		got, err := getVSphereCredentialsSecret(context.Background(), client)

		require.NoError(t, err)
		assert.Equal(t, dedicatedSecret, got)
	})

	t.Run("legacy secret is not used as fallback", func(t *testing.T) {
		client := fake.NewSimpleClientset(legacySecret)

		_, err := getVSphereCredentialsSecret(context.Background(), client)

		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))
		assert.Contains(t, err.Error(), ctrlcommon.MCONamespace+"/"+ctrlcommon.VSphereCredentialsSecretName)
	})
}

func TestGetVSphereCredentialsForServer(t *testing.T) {
	tests := []struct {
		name             string
		data             map[string][]byte
		server           string
		expectedUsername string
		expectedPassword string
	}{
		{
			name: "single vCenter uses exact server keys",
			data: map[string][]byte{
				"vcenter-a.example.com.username": []byte("user-a"),
				"vcenter-a.example.com.password": []byte("password-a"),
			},
			server:           "vcenter-a.example.com",
			expectedUsername: "user-a",
			expectedPassword: "password-a",
		},
		{
			name: "multiple vCenters select exact server",
			data: map[string][]byte{
				"vcenter-a.example.com.username": []byte("user-a"),
				"vcenter-a.example.com.password": []byte("password-a"),
				"vcenter-b.example.com.username": []byte("user-b"),
				"vcenter-b.example.com.password": []byte("password-b"),
			},
			server:           "vcenter-b.example.com",
			expectedUsername: "user-b",
			expectedPassword: "password-b",
		},
		{
			name:   "missing server keys preserve empty values",
			data:   map[string][]byte{"vcenter-a.example.com.username": []byte("user-a")},
			server: "missing.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: tc.data}
			username, password := getVSphereCredentialsForServer(secret, tc.server)
			assert.Equal(t, tc.expectedUsername, username)
			assert.Equal(t, tc.expectedPassword, password)
		})
	}
}
