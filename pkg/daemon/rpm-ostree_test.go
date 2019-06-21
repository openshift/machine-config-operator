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
	RunPivotReturns            []error
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

// RunPivot implements a test version of RpmOstreeClients RunPivot. It returns errors as defined
// in the instances RunPivotReturns field in order.
func (r RpmOstreeClientMock) RunPivot(string) error {
	err := r.RunPivotReturns[0]
	if len(r.RunPivotReturns) > 1 {
		r.RunPivotReturns = r.RunPivotReturns[1:]
	}
	return err
}

// PullAndRebase is a mock
func (r RpmOstreeClientMock) PullAndRebase(string, bool) (string, bool, error) {
	return "", false, nil
}

func (r RpmOstreeClientMock) GetStatus() (string, error) {
	return "rpm-ostree mock: blah blah some status here", nil
}
