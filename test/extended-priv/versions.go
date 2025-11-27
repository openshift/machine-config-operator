package extended

import (
	"strconv"
	"strings"

	e2e "k8s.io/kubernetes/test/e2e/framework"
)

var operators = map[string][]int{
	"<":  {1},
	">":  {-1},
	"=":  {0},
	"==": {0},
	"<=": {0, 1},
	"=<": {0, 1},
	">=": {0, -1},
	"=>": {0, -1},
}

func validateOperator(operator string) {
	keys := make([]string, 0, len(operators))
	for k := range operators {
		keys = append(keys, k)
		if operator == k {
			return
		}
	}

	e2e.Failf("Operator %s not permitted. Permitted operators: %s", operator, keys)
}

func compareVer(l, r string) (ret int) {
	ls := strings.Split(l, ".")
	rs := strings.Split(r, ".")
	maxlen := len(ls)
	if len(rs) > len(ls) {
		maxlen = len(rs)
	}
	for i := 0; i < maxlen; i++ {
		var tmpl, tmpr string
		if len(ls) > i {
			tmpl = ls[i]
		}
		if len(rs) > i {
			tmpr = rs[i]
		}
		li, _ := strconv.Atoi(tmpl)
		ri, _ := strconv.Atoi(tmpr)
		if li > ri {
			return -1
		} else if li < ri {
			return 1
		}
	}
	return 0
}

// CompareVersions returns the result of comparing 2 versions using the given operator to compare
// i.e CompareVersions("3.1", ">", "3.0") return true
func CompareVersions(l, operator, r string) bool {
	validateOperator(operator)
	expectedResults := operators[operator]
	result := compareVer(l, r)

	for _, res := range expectedResults {
		if result == res {
			return true
		}
	}

	return false
}
