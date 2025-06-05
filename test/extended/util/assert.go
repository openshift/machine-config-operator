package util

import (
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
	if strings.Compare(e.Error(), "timed out waiting for the condition") == 0 || strings.Compare(e.Error(), "context deadline exceeded") == 0 {
		err = fmt.Errorf("case: %v\nerror: %s", g.CurrentSpecReport().FullText(), msg)
	} else {
		err = fmt.Errorf("case: %v\nerror: %s", g.CurrentSpecReport().FullText(), e.Error())
	}
	o.Expect(err).NotTo(o.HaveOccurred())

}
