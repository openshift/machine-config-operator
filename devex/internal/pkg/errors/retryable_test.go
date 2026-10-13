package errors

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMaxCountRetryable(t *testing.T) {
	t.Parallel()

	mar := NewMaxAttemptRetryer(10)

	assert.Nil(t, mar.Current())

	innerTestLoop := func() {
		for i := 0; i <= 10; i++ {
			if i < 10 {
				assert.False(t, mar.IsReached())
			} else {
				assert.True(t, mar.IsReached())
			}

			current := mar.Current().(*int)
			assert.Equal(t, i+1, *current)
		}
	}

	for i := 0; i <= 10; i++ {
		innerTestLoop()
		assert.NotNil(t, mar.Current())
		assert.False(t, mar.IsEmpty())
		mar.Clear()
		assert.Nil(t, mar.Current())
		assert.True(t, mar.IsEmpty())
	}
}

func TestTimeRetryable(t *testing.T) {
	t.Parallel()

	tr := NewTimeRetryer(time.Millisecond)

	innerTestLoop := func() {
		for i := 0; i <= 10; i++ {
			assert.False(t, tr.IsReached())
			assert.NotNil(t, tr.Current())
			assert.False(t, tr.IsEmpty())
		}
	}

	for i := 0; i <= 10; i++ {
		innerTestLoop()
		time.Sleep(time.Millisecond)
		assert.True(t, tr.IsReached())
		assert.NotNil(t, tr.Current())
		assert.False(t, tr.IsEmpty())
		tr.Clear()
		assert.Nil(t, tr.Current())
		assert.True(t, tr.IsEmpty())
	}
}
