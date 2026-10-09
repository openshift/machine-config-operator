package extended

import (
	"fmt"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// ManagedUserDataSecret abstracts the differences between MAPI and CAPI user-data secrets.
// MAPI secrets use "userData"/"disableTemplating" keys while CAPI secrets use "value"/"format" keys.
// Both implementations embed *Secret, so all Secret methods (Delete, Exists, GetDataValue, etc.) are available.
type ManagedUserDataSecret interface {
	// GetUserData returns the user-data content from the secret using the appropriate key
	GetUserData() (string, error)
	// SetUserData sets the user-data content in the secret using the appropriate key
	SetUserData(userData string) error
	// Duplicate creates a clone of this secret with a new name, optionally modifying the user-data content
	Duplicate(newName string, modifyUserData func(string) (string, error)) (ManagedUserDataSecret, error)
	// Delete deletes the secret
	Delete(extraParams ...string) error
	// Exists returns true if the secret exists
	Exists() bool
	// GetName returns the secret name
	GetName() string
	// GetNamespace returns the secret namespace
	GetNamespace() string
}

// MAPIUserDataSecret handles MAPI user-data secrets with keys "userData" and "disableTemplating"
type MAPIUserDataSecret struct {
	*Secret
}

// CAPIUserDataSecret handles CAPI user-data secrets with keys "value" and "format"
type CAPIUserDataSecret struct {
	*Secret
}

// NewMAPIUserDataSecret creates a ManagedUserDataSecret for MAPI
func NewMAPIUserDataSecret(secret *Secret) *MAPIUserDataSecret {
	return &MAPIUserDataSecret{Secret: secret}
}

// NewCAPIUserDataSecret creates a ManagedUserDataSecret for CAPI
func NewCAPIUserDataSecret(secret *Secret) *CAPIUserDataSecret {
	return &CAPIUserDataSecret{Secret: secret}
}

// GetUserData returns the user-data from the "userData" key
func (s *MAPIUserDataSecret) GetUserData() (string, error) {
	return s.GetDataValue("userData")
}

// SetUserData sets the user-data in the "userData" key
func (s *MAPIUserDataSecret) SetUserData(userData string) error {
	return s.SetDataValue("userData", userData)
}

// Duplicate creates a new MAPI user-data secret preserving "userData" and "disableTemplating" keys
func (s *MAPIUserDataSecret) Duplicate(newName string, modifyUserData func(string) (string, error)) (ManagedUserDataSecret, error) {
	newSecret, err := duplicateUserDataSecret(s.Secret, newName, "userData", "disableTemplating", modifyUserData)
	if err != nil {
		return nil, err
	}
	return NewMAPIUserDataSecret(newSecret), nil
}

// GetUserData returns the user-data from the "value" key
func (s *CAPIUserDataSecret) GetUserData() (string, error) {
	return s.GetDataValue("value")
}

// SetUserData sets the user-data in the "value" key
func (s *CAPIUserDataSecret) SetUserData(userData string) error {
	return s.SetDataValue("value", userData)
}

// Duplicate creates a new CAPI user-data secret preserving "value" and "format" keys
func (s *CAPIUserDataSecret) Duplicate(newName string, modifyUserData func(string) (string, error)) (ManagedUserDataSecret, error) {
	newSecret, err := duplicateUserDataSecret(s.Secret, newName, "value", "format", modifyUserData)
	if err != nil {
		return nil, err
	}
	return NewCAPIUserDataSecret(newSecret), nil
}

// duplicateUserDataSecret creates a new secret with the given name, copying the dataKey and extraKey from the source secret.
// If modifyUserData is not nil, it is applied to the dataKey value before creating the new secret.
func duplicateUserDataSecret(src *Secret, newName, dataKey, extraKey string, modifyUserData func(string) (string, error)) (*Secret, error) {
	namespace := src.GetNamespace()

	userData, err := src.GetDataValue(dataKey)
	if err != nil {
		logger.Errorf("Error getting %s info from secret %s -n %s.\n%s", dataKey, src.GetName(), namespace, err)
		return nil, err
	}

	extraValue, err := src.GetDataValue(extraKey)
	if err != nil {
		logger.Errorf("Error getting %s info from secret %s -n %s.\n%s", extraKey, src.GetName(), namespace, err)
		return nil, err
	}

	if modifyUserData != nil {
		userData, err = modifyUserData(userData)
		if err != nil {
			logger.Errorf("Error modifying the %s content with the provided modification function", dataKey)
			return nil, err
		}
	}

	oc := src.GetOC()
	logger.Debugf("New %s info:\n%s", dataKey, userData)
	oc.NotShowInfo()
	defer oc.SetShowInfo()

	_, err = oc.AsAdmin().WithoutNamespace().Run("create").Args("secret", "generic", newName, "-n", namespace,
		"--from-literal", fmt.Sprintf("%s=%s", dataKey, userData),
		"--from-literal", fmt.Sprintf("%s=%s", extraKey, extraValue)).Output()
	if err != nil {
		return nil, err
	}

	return NewSecret(oc.AsAdmin(), namespace, newName), nil
}

// Compile-time interface checks
var _ ManagedUserDataSecret = &MAPIUserDataSecret{}
var _ ManagedUserDataSecret = &CAPIUserDataSecret{}
