package tools

import "testing"

func TestResultIsError(t *testing.T) {
	if NoData("none").IsError() {
		t.Fatal("NoData is not an error")
	}
	if !(Result{Errors: []string{"x"}}).IsError() {
		t.Fatal("errors without data is an error")
	}
	if (Result{Data: 1, Errors: []string{"x"}}).IsError() {
		t.Fatal("partial data is not an error")
	}
	if NoData("n").Data != nil || NoData("n").Note != "n" {
		t.Fatal("NoData shape")
	}
}
