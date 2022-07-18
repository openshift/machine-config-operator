package daemon

/*
 * This file contains test code for the rpm-ostree client. It is meant to be used when
 * testing the daemon and mocking the responses that would normally be executed by the
 * client.
 */

// GetBootedOSImageURLReturn is a structure used for testing. The fields correspond with the
// return values in GetBootedOSImageURL implementations.
type GetBootedOSImageURLReturn struct {
	OsImageURL string
	Version    string
	Error      error
}

// RpmOstreeClientMock is a testing implementation of NodeUpdaterClient. Fields presented here
// hold return values that will be returned when their corresponding methods are called.
type RpmOstreeClientMock struct {
	GetBootedOSImageURLReturns []GetBootedOSImageURLReturn
}

func (r RpmOstreeClientMock) Initialize() error {
	return nil
}

// GetBootedOSImageURL implements a test version of RpmOStreeClients GetBootedOSImageURL.
// It returns an OsImageURL, Version, and Error as defined in GetBootedOSImageURLReturns in order.
func (r RpmOstreeClientMock) GetBootedOSImageURL() (string, string, error) {
	returnValues := r.GetBootedOSImageURLReturns[0]
	if len(r.GetBootedOSImageURLReturns) > 1 {
		r.GetBootedOSImageURLReturns = r.GetBootedOSImageURLReturns[1:]
	}
	return returnValues.OsImageURL, returnValues.Version, returnValues.Error
}

// PullAndRebase is a mock
func (r RpmOstreeClientMock) Rebase(string, string) (bool, error) {
	return false, nil
}

func (r RpmOstreeClientMock) GetStatus() (string, error) {
	return "rpm-ostree mock: blah blah some status here", nil
}

func (r RpmOstreeClientMock) CleanupRollback() error {
	return nil
}

func (r RpmOstreeClientMock) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	return &RpmOstreeDeployment{}, nil
}
