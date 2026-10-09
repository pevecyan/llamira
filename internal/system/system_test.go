package system

import "testing"

func TestListedActive(t *testing.T) {
	out := "llamira-build-b11429.service loaded failed failed  llamira: build llama.cpp b11429\n" +
		"llamira-build-b11521.service loaded active running llamira: build llama.cpp b11521\n"
	cases := map[string]bool{
		"llamira-build-b11521.service": true,
		"llamira-build-b11429.service": false, // failed
		"llamira-build-b1.service":     false, // not loaded: not listed
	}
	for unit, want := range cases {
		if got := listedActive(out, unit); got != want {
			t.Errorf("%s: got %v, want %v", unit, got, want)
		}
	}
	if listedActive("", "x.service") {
		t.Error("empty output: want false")
	}
}
