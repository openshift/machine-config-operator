package errors

import "time"

type Retryer interface {
	Clear()
	IsReached() bool
	Current() interface{}
	IsEmpty() bool
}

type timeRetryer struct {
	threshold time.Duration
	start     *time.Time
}

func NewTimeRetryer(threshold time.Duration) Retryer {
	return &timeRetryer{
		threshold: threshold,
	}
}

func (t *timeRetryer) IsEmpty() bool {
	return t.start == nil
}

func (t *timeRetryer) Current() interface{} {
	return t.start
}

func (t *timeRetryer) IsReached() bool {
	if t.start == nil {
		now := time.Now()
		t.start = &now
	}

	return time.Since(*t.start) >= t.threshold
}

func (t *timeRetryer) Clear() {
	t.start = nil
}

type maxAttemptRetryer struct {
	threshold int
	start     *int
}

func NewMaxAttemptRetryer(threshold int) Retryer {
	return &maxAttemptRetryer{
		threshold: threshold,
	}
}

func (m *maxAttemptRetryer) IsEmpty() bool {
	return m.start == nil
}

func (m *maxAttemptRetryer) Current() interface{} {
	return m.start
}

func (m *maxAttemptRetryer) IsReached() bool {
	if m.start == nil {
		start := 0
		m.start = &start
	}

	*m.start++

	return *m.start > m.threshold
}

func (m *maxAttemptRetryer) Clear() {
	m.start = nil
}
