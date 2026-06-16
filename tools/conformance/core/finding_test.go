package core

import "testing"

func TestSeverityString(t *testing.T) {
	if Must.String() != "must" || Should.String() != "should" || Info.String() != "info" {
		t.Fatalf("severity strings wrong: %s %s %s", Must, Should, Info)
	}
}

func TestStatusFailsCI(t *testing.T) {
	// must-violation fails CI; suspected/unverifiable/pass/na do not.
	cases := map[Status]bool{Violation: true, Suspected: false, Unverifiable: false, Pass: false, NA: false}
	for st, want := range cases {
		if st.CountsAsViolation() != want {
			t.Errorf("%v CountsAsViolation = %v, want %v", st, st.CountsAsViolation(), want)
		}
	}
}
