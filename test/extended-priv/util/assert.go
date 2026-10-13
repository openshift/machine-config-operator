package util

import (
	"context"
	"errors"
	"fmt"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
)

// e is return value of Wait.Poll
// msg is the reason why time out
// the function assert return value of Wait.Poll, and expect NO error
// if e is Nil, just pass and nothing happen.
// if e is not Nil, will not print the default error message "timed out waiting for the condition" because it causes RP AA not to analysis result exactly.
// if e is "timed out waiting for the condition" or "context deadline exceeded", it is replaced by msg.
// if e is not "timed out waiting for the condition", it print e and then case fails.

func AssertWaitPollNoErr(e error, msg string) {
	if e == nil {
		return
	}
	var err error
	if errors.Is(e, context.DeadlineExceeded) ||
		strings.Compare(e.Error(), "timed out waiting for the condition") == 0 ||
		strings.Compare(e.Error(), "context deadline exceeded") == 0 {
		err = fmt.Errorf("case: %v\nerror: %s", g.CurrentSpecReport().FullText(), msg)
	} else {
		err = fmt.Errorf("case: %v\nerror: %s", g.CurrentSpecReport().FullText(), e.Error())
	}
	o.Expect(err).NotTo(o.HaveOccurred())
}

// OrFail function will process another function's return values and fail if any of those returned values is ane error != nil and returns the first value
// example: if we have: func getValued() (string, error)
//
//	we can do:  value := OrFail[string](getValue())
func OrFail[T any](vals ...any) T {

	for _, val := range vals {
		err, ok := val.(error)
		if ok {
			o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred())
		}
	}

	return vals[0].(T)
}
