package extended

import (
	"fmt"
	"sort"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega/types"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

// Event struct is used to handle Event resources in OCP
type Event struct {
	Resource
}

// EventList handles list of nodes
type EventList struct {
	ResourceList
}

// NewEvent create a Event struct
func NewEvent(oc *exutil.CLI, namespace, name string) *Event {
	return &Event{Resource: *NewNamespacedResource(oc, "Event", namespace, name)}
}

// String implements the Stringer interface
func (e Event) String() string {
	e.oc.NotShowInfo()
	defer e.oc.SetShowInfo()

	description, err := e.Get(`{.metadata.creationTimestamp} {.lastTimestamp} Type: {.type} Reason: {.reason} Namespace: {.metadata.namespace} Involves: {.involvedObject.kind}/{.involvedObject.name}`)
	if err != nil {
		logger.Errorf("Event %s/%s does not exist anymore", e.GetNamespace(), e.GetName())
		return ""
	}

	return description
}

// GetLastTimestamp returns the last occurrence of this event
func (e Event) GetLastTimestamp() (time.Time, error) {
	lastOccurrence, err := e.Get(`{.lastTimestamp}`)
	if err != nil {
		logger.Errorf("Error parsing event %s/%s. Error: %s", e.GetNamespace(), e.GetName(), err)
		return time.Time{}, err
	}

	parsedLastOccurrence, perr := time.Parse(time.RFC3339, lastOccurrence)
	if perr != nil {
		logger.Errorf("Error parsing event '%s' -n '%s' lastTimestamp: %s", e.GetName(), e.GetNamespace(), perr)
		return time.Time{}, perr

	}

	return parsedLastOccurrence, nil
}

// NewEventList construct a new node list struct to handle all existing nodes
func NewEventList(oc *exutil.CLI, namespace string) *EventList {
	return &EventList{*NewNamespacedResourceList(oc, "Event", namespace)}
}

// GetAll returns a []Event list with all existing events sorted by last occurrence
// the first element will be the most recent one
func (el *EventList) GetAll() ([]Event, error) {
	el.SortByLastTimestamp()
	allEventResources, err := el.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allEvents := make([]Event, 0, len(allEventResources))

	for _, eventRes := range allEventResources {
		allEvents = append(allEvents, *NewEvent(el.oc, eventRes.namespace, eventRes.name))
	}
	// We want the first element to be the more recent
	allEvents = reverseEventsList(allEvents)

	return allEvents, nil
}

// SortByLastTimestamp configures the list to be sorted by lastTimestamp field
func (el *EventList) SortByLastTimestamp() {
	el.ResourceList.SortBy("lastTimestamp")
}

// GetLatest returns the latest event that occurred. Nil if no event exists.
func (el *EventList) GetLatest() (*Event, error) {

	allEvents, lerr := el.GetAll()
	if lerr != nil {
		logger.Errorf("Error getting events %s", lerr)
		return nil, lerr
	}
	if len(allEvents) == 0 {
		return nil, nil
	}

	return &(allEvents[0]), nil
}

// GetAllEventsSinceEvent returns all events that occurred since a given event (not included)
func (el *EventList) GetAllEventsSinceEvent(sinceEvent *Event) ([]Event, error) {
	allEvents, lerr := el.GetAll()
	if lerr != nil {
		logger.Errorf("Error getting events %s", lerr)
		return nil, lerr
	}

	if sinceEvent == nil {
		return allEvents, nil
	}

	returnEvents := []Event{}
	for _, event := range allEvents {
		if event.name == sinceEvent.name {
			break
		}
		returnEvents = append(returnEvents, event)
	}

	return returnEvents, nil
}

// GetAllSince return a list of the events that happened since the provided duration
func (el *EventList) GetAllSince(since time.Time) ([]Event, error) {
	// Remove log noise
	el.oc.NotShowInfo()
	defer el.oc.SetShowInfo()

	allEvents, lerr := el.GetAll()
	if lerr != nil {
		logger.Errorf("Error getting events %s", lerr)
		return nil, lerr
	}

	returnEvents := []Event{}
	for _, loopEvent := range allEvents {
		event := loopEvent // this is to make sure that we execute defer in all events, and not only in the last one
		// Remove log noise
		event.oc.NotShowInfo()
		defer event.oc.SetShowInfo()

		lastOccurrence, err := event.GetLastTimestamp()
		if err != nil {
			logger.Errorf("Error getting lastTimestamp in event %s/%s. Error: %s", event.GetNamespace(), event.GetName(), err)
			continue
		}

		if lastOccurrence.Before(since) {
			break
		}

		returnEvents = append(returnEvents, event)
	}

	return returnEvents, nil
}

// from https://github.com/golang/go/wiki/SliceTricks#reversing
func reverseEventsList(a []Event) []Event {

	for i := len(a)/2 - 1; i >= 0; i-- {
		opp := len(a) - 1 - i
		a[i], a[opp] = a[opp], a[i]
	}

	return a
}

// HaveEventsSequence returns a gomega matcher that checks if a list of Events contains a given sequence of reasons
func HaveEventsSequence(sequence ...string) types.GomegaMatcher {
	return &haveEventsSequenceMatcher{sequence: sequence}
}

// struct to cache and sort events information
type tmpEvent struct {
	lastTimestamp time.Time
	reason        string
}

func (t tmpEvent) String() string { return fmt.Sprintf("%s - %s", t.lastTimestamp, t.reason) }

// sorter to sort the chache event list
type byLastTimestamp []tmpEvent

func (a byLastTimestamp) Len() int      { return len(a) }
func (a byLastTimestamp) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byLastTimestamp) Less(i, j int) bool {
	return a[i].lastTimestamp.Before(a[j].lastTimestamp)
}

// struct implementing gomaega matcher interface
type haveEventsSequenceMatcher struct {
	sequence []string
}

func (matcher *haveEventsSequenceMatcher) Match(actual interface{}) (success bool, err error) {

	logger.Infof("Start verifying events sequence: %s", matcher.sequence)
	events, ok := actual.([]Event)
	if !ok {
		return false, fmt.Errorf("HaveSequence matcher expects a slice of Events in test case %v", g.CurrentSpecReport().FullText())
	}

	// To avoid too many "oc" executions we store the events information in a cached struct list with "lastTimestamp" and "reason" fields.
	tmpEvents := make([]tmpEvent, len(events))
	for index, event := range events {
		event.oc.NotShowInfo()
		defer event.oc.SetShowInfo()

		reason, err := event.Get(`{.reason}`)
		if err != nil {
			return false, err
		}

		lastTimestamp, err := event.GetLastTimestamp()
		if err != nil {
			return false, err
		}
		tmpEvents[index] = tmpEvent{lastTimestamp: lastTimestamp, reason: reason}
	}

	// We sort the cached list. Oldest event first
	sort.Sort(byLastTimestamp(tmpEvents))

	// Several events can be created in the same second, hence, we need to take into account
	// that 2 events in the same second can match any order.
	// If 2 events have the same timestamp
	// we consider that the order is right no matter what.
	lastEventTime := time.Time{}
	for _, seqReason := range matcher.sequence {
		found := false
		for _, event := range tmpEvents {
			if seqReason == event.reason &&
				(lastEventTime.Before(event.lastTimestamp) || lastEventTime.Equal(event.lastTimestamp)) {
				logger.Infof("Found! %s event in time %s", seqReason, event.lastTimestamp)

				lastEventTime = event.lastTimestamp
				found = true
				break
			}
		}

		// Could not find an event with the sequence's reason. We fail the match
		if !found {
			logger.Infof("%s event NOT Found after time %s", seqReason, lastEventTime)
			return false, nil
		}
	}

	return true, nil
}

func (matcher *haveEventsSequenceMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	events, _ := actual.([]Event)

	var output strings.Builder

	if len(events) == 0 {
		output.WriteString("No events in the list\n")
	} else {
		output.WriteString("Expecte events\n")
		for _, event := range events {
			output.WriteString(fmt.Sprintf("-  %s\n", event))
		}
	}
	output.WriteString(fmt.Sprintf("to contain this reason sequence\n\t%s\n", matcher.sequence))

	return output.String()
}

func (matcher *haveEventsSequenceMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	events, _ := actual.([]Event)

	var output strings.Builder
	output.WriteString("Expecte events\n")
	for _, event := range events {
		output.WriteString(fmt.Sprintf("-  %s\n", event))
	}
	output.WriteString(fmt.Sprintf("NOT to contain this reason sequence\n\t%s\n", matcher.sequence))

	return output.String()
}
