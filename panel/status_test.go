package main

import "testing"

const (
	lineTunnels1 = `Sep 15 18:28:53 h ConsoleClient[1]: {"data":{"count":1},"noticeType":"Tunnels","timestamp":"2026-09-15T18:28:53.271Z"}`
	lineTunnels0 = `Sep 15 18:40:00 h ConsoleClient[1]: {"data":{"count":0},"noticeType":"Tunnels","timestamp":"2026-09-15T18:40:00.000Z"}`
	lineBytes    = `Sep 15 18:33:53 h ConsoleClient[1]: {"data":{"diagnosticID":"x","received":0,"sent":0},"noticeType":"TotalBytesTransferred","timestamp":"2026-09-15T18:33:53.271Z"}`
	lineRegionUS = `Sep 15 18:28:53 h ConsoleClient[1]: {"data":{"serverRegion":"US"},"noticeType":"ConnectedServerRegion","timestamp":"2026-09-15T18:28:53.273Z"}`
	lineRegionDE = `Sep 15 19:28:53 h ConsoleClient[1]: {"data":{"serverRegion":"DE"},"noticeType":"ConnectedServerRegion","timestamp":"2026-09-15T19:28:53.273Z"}`
	lineInfo     = `Sep 15 18:28:53 h ConsoleClient[1]: {"data":{"message":"updated server abc"},"noticeType":"Info","timestamp":"2026-09-15T18:28:53.000Z"}`
)

func TestScanNotices(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		state  string
		region string
		found  bool
	}{
		{"connected", lineTunnels1, "active", "", true},
		{"only periodic bytes", lineBytes, "active", "", true},
		{"dropped", lineTunnels1 + "\n" + lineTunnels0, "connecting", "", true},
		{"reconnected after drop", lineTunnels0 + "\n" + lineTunnels1, "connecting", "", true}, // later timestamp wins, not input order
		{"region only", lineRegionUS, "", "US", false},
		{"state and region", lineInfo + "\n" + lineTunnels1 + "\n" + lineRegionUS, "active", "US", true},
		{"newest region wins", lineRegionDE + "\n" + lineRegionUS, "", "DE", false},
		{"noise only", lineInfo, "", "", false},
		{"empty", "-- No entries --", "", "", false},
	}
	for _, c := range cases {
		s, r, f := scanNotices(c.in)
		if s != c.state || r != c.region || f != c.found {
			t.Errorf("%s: got (%q,%q,%v) want (%q,%q,%v)", c.name, s, r, f, c.state, c.region, c.found)
		}
	}
}

func TestParseUnitStates(t *testing.T) {
	out := "Id=psi-tunnel@de1.service\nActiveState=active\nInvocationID=aaa111\n\n" +
		"Id=psi-tunnel@us1.service\nActiveState=inactive\nInvocationID=\n\n" +
		"Id=psi-tunnel@gone.service\nActiveState=failed\nInvocationID=ccc333\n"
	got := parseUnitStates(out)
	want := map[string]unitState{
		"de1":  {Active: "active", InvocationID: "aaa111"},
		"us1":  {Active: "inactive"},
		"gone": {Active: "failed", InvocationID: "ccc333"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %+v want %+v", k, got[k], v)
		}
	}
	if len(parseUnitStates("")) != 0 {
		t.Error("empty output should yield no units")
	}
}
