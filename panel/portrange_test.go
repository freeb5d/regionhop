package main

import "testing"

func TestParsePortRange(t *testing.T) {
	for in, want := range map[string][2]int{
		"": {19000, 19999}, "30000-30999": {30000, 30999}, " 2000-2005 ": {2000, 2005},
		"abc": {19000, 19999}, "500-600": {19000, 19999}, "40000-30000": {19000, 19999}, "1024-70000": {19000, 19999},
	} {
		lo, hi := parsePortRange(in)
		if lo != want[0] || hi != want[1] {
			t.Errorf("%q: got %d-%d", in, lo, hi)
		}
	}
}
