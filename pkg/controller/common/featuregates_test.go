package common

import (
	"context"
	"errors"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

const (
	FeatureGatesTestExistingEnaFeatureGate1 configv1.FeatureGateName = "testExistingFeatureGate-enabled-1"
	FeatureGatesTestExistingEnaFeatureGate2 configv1.FeatureGateName = "testExistingFeatureGate-enabled-2"
	FeatureGatesTestExistingDisFeatureGate1 configv1.FeatureGateName = "testExistingFeatureGate-disabled-1"
)

type FeatureGatesHandlerStub struct {
	testing      *testing.T
	featureGates featuregates.FeatureGate
	featuresChan chan struct{}
	initTime     time.Duration
	fetchError   error
}

func NewFeatureGatesHandlerStub(t *testing.T, initTime time.Duration, fetchError error) *FeatureGatesHandlerStub {
	return &FeatureGatesHandlerStub{
		testing: t,
		featureGates: featuregates.NewFeatureGate(
			[]configv1.FeatureGateName{
				FeatureGatesTestExistingEnaFeatureGate1,
				FeatureGatesTestExistingEnaFeatureGate2,
			},
			[]configv1.FeatureGateName{
				FeatureGatesTestExistingDisFeatureGate1,
			}),
		featuresChan: make(chan struct{}),
		initTime:     initTime,
		fetchError:   fetchError,
	}
}

func (s *FeatureGatesHandlerStub) StartStub() {
	time.AfterFunc(s.initTime, func() {
		s.featuresChan <- struct{}{}
	})
}

func (s *FeatureGatesHandlerStub) SetChangeHandler(_ featuregates.FeatureGateChangeHandlerFunc) {
	// The handler does not override the default FeatureGateChangeHandlerFunc
	s.testing.Fatal("SetChangeHandler should not be called by the feature gates handler")
}

func (s *FeatureGatesHandlerStub) Run(_ context.Context) {
	// Run is never called from the handler. The entity that creates the FeatureGateAccess is responsible
	// for properly initializing it and creating the Go rutine.
	s.testing.Fatal("Run should not be called by the feature gates handler")
}

func (s *FeatureGatesHandlerStub) InitialFeatureGatesObserved() <-chan struct{} {
	return s.featuresChan
}

func (s *FeatureGatesHandlerStub) CurrentFeatureGates() (featuregates.FeatureGate, error) {
	return s.featureGates, s.fetchError
}

func (s *FeatureGatesHandlerStub) AreInitialFeatureGatesObserved() bool {
	// The handler does not rely on AreInitialFeatureGatesObserved as it uses the channel
	// to get the notification of the FeatureGates ready.
	s.testing.Fatal("AreInitialFeatureGatesObserved should not be called by the feature gates handler")
	return false
}

func TestFeatureGateHandlerAccess(t *testing.T) {
	fgStub := NewFeatureGatesHandlerStub(t, 100*time.Millisecond, nil)
	handler := NewFeatureGatesAccessHandler(fgStub)
	assert.NotNil(t, handler)
	assert.Empty(t, handler.KnownFeatures())
	fgStub.StartStub()
	assert.NoError(t, handler.Connect(context.Background()))

	checkFeatureGates(t, handler)

	// Check that a non-existing FG is reported as so and disabled by default
	const nonExistingFeatureGate = "test-non-existing"
	assert.False(t, handler.Exists(nonExistingFeatureGate))
	assert.False(t, handler.Enabled(nonExistingFeatureGate))
}

func TestFeatureGateHandlerAccessTimeout(t *testing.T) {
	fgStub := NewFeatureGatesHandlerStub(t, 100*time.Millisecond, nil)
	handler := NewFeatureGatesAccessHandler(fgStub)
	assert.NotNil(t, handler)
	// Set a really low timeout to make connection fail by timeout
	handler.connectTimeout = 10 * time.Millisecond
	fgStub.StartStub()

	err := handler.Connect(context.Background())
	assert.ErrorContains(t, err, "timed out")
}

func TestFeatureGateHandlerAccessCancel(t *testing.T) {
	fgStub := NewFeatureGatesHandlerStub(t, 100*time.Millisecond, nil)
	handler := NewFeatureGatesAccessHandler(fgStub)
	assert.NotNil(t, handler)
	fgStub.StartStub()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, func() { cancel() })
	err := handler.Connect(ctx)
	assert.ErrorContains(t, err, "context canceled")
}

func TestFeatureGateHandlerAccessFetchError(t *testing.T) {
	fetchError := errors.New("fetch error")
	fgStub := NewFeatureGatesHandlerStub(t, 1*time.Millisecond, fetchError)
	handler := NewFeatureGatesAccessHandler(fgStub)
	assert.NotNil(t, handler)
	fgStub.StartStub()

	err := handler.Connect(context.Background())
	assert.ErrorContains(t, err, fetchError.Error())
}

func TestFeatureGateHandlerHardcoded(t *testing.T) {
	handler := NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{
		FeatureGatesTestExistingEnaFeatureGate1,
		FeatureGatesTestExistingEnaFeatureGate2,
	},
		[]configv1.FeatureGateName{
			FeatureGatesTestExistingDisFeatureGate1,
		})
	assert.NotNil(t, handler)
	checkFeatureGates(t, handler)
}

func TestFeatureGateHandlerCR(t *testing.T) {
	fg := buildDummyFeatureGate()
	handler, err := NewFeatureGatesCRHandlerImpl(&fg, "v1")
	assert.NotNil(t, handler)
	assert.NoError(t, err)
	checkFeatureGates(t, handler)
}

func TestFeatureGateHandlerCRParseError(t *testing.T) {
	fg := buildDummyFeatureGate()
	_, err := NewFeatureGatesCRHandlerImpl(&fg, "v2")
	assert.ErrorContains(t, err, "unable to determine features")
}

func buildDummyFeatureGate() configv1.FeatureGate {
	fg := configv1.FeatureGate{
		Status: configv1.FeatureGateStatus{
			FeatureGates: []configv1.FeatureGateDetails{
				{
					Version: "v1",
					Enabled: []configv1.FeatureGateAttributes{
						{Name: FeatureGatesTestExistingEnaFeatureGate1},
						{Name: FeatureGatesTestExistingEnaFeatureGate2},
					},
					Disabled: []configv1.FeatureGateAttributes{
						{Name: FeatureGatesTestExistingDisFeatureGate1},
					},
				},
			},
		},
	}
	return fg
}

func checkFeatureGates(t *testing.T, handler *FeatureGatesHandlerImpl) {
	// Check that all the known FGs are reported as known
	assert.True(t, handler.Exists(FeatureGatesTestExistingEnaFeatureGate1))
	assert.True(t, handler.Exists(FeatureGatesTestExistingEnaFeatureGate2))
	assert.True(t, handler.Exists(FeatureGatesTestExistingDisFeatureGate1))

	// Check that all the known FGs are reported as enabled/disabled
	assert.True(t, handler.Enabled(FeatureGatesTestExistingEnaFeatureGate1))
	assert.True(t, handler.Enabled(FeatureGatesTestExistingEnaFeatureGate2))
	assert.False(t, handler.Enabled(FeatureGatesTestExistingDisFeatureGate1))
}
