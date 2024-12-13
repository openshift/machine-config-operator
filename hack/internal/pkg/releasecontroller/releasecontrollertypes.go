package releasecontroller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// All types in this file were copy/pasted from the ReleaseController repository so I wouldn't have to fight with go mod.

// APIReleaseInfo encapsulates the release verification results and upgrade history for a release tag.
type APIReleaseInfo struct {
	// Name is the name of the release tag.
	Name string `json:"name"`
	// Phase is the phase of the release tag.
	Phase string `json:"phase"`
	// Results is the status of the release verification jobs for this release tag
	Results *VerificationJobsSummary `json:"results,omitempty"`
	// UpgradesTo is the list of UpgradeHistory "to" this release tag
	UpgradesTo []UpgradeHistory `json:"upgradesTo,omitempty"`
	// UpgradesFrom is the list of UpgradeHistory "from" this release tag
	UpgradesFrom []UpgradeHistory `json:"upgradesFrom,omitempty"`
	// //ChangeLog is the html representation of the changes included in this release tag
	// ChangeLog []byte `json:"changeLog,omitempty"`
	// //ChangeLogJson is the json representation of the changes included in this release tag
	// ChangeLogJson ChangeLog `json:"changeLogJson,omitempty"`
}

// VerificationJobsSummary an organized, by job type, collection of VerificationStatusMap objects
type VerificationJobsSummary struct {
	BlockingJobs  VerificationStatusMap `json:"blockingJobs,omitempty"`
	InformingJobs VerificationStatusMap `json:"informingJobs,omitempty"`
	PendingJobs   VerificationStatusMap `json:"pendingJobs,omitempty"`
}

type VerificationStatusMap map[string]*VerificationStatus

type VerificationStatus struct {
	State          string       `json:"state"`
	URL            string       `json:"url"`
	Retries        int          `json:"retries,omitempty"`
	TransitionTime *metav1.Time `json:"transitionTime,omitempty"`
}

type UpgradeHistory struct {
	From string
	To   string

	Success int
	Failure int
	Total   int

	History map[string]UpgradeResult
}

type UpgradeResult struct {
	State string `json:"state"`
	URL   string `json:"url"`
}
