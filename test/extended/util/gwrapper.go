package util

import (
	g "github.com/onsi/ginkgo/v2"
)

// when 4.14 synch with k1.27, there is gingkgo upgrade from 2.4 to 26
// By method changes and it does not print "STEP:" information. some tester want to use it. so, make this wrapper to
// print if you want to get "STEP:", you need to change g.By to exutil. By text is the string you want to describe
// the step.
func By(text string) {
	// TODO: To be removed. Leftover of porting QE tests to the MCO OTE binary
	g.By(text)

}
