package main

import (
	"fmt"
	"net"
	"testing"
)

func TestFindFreePortSkipsPortsInUseByOtherSoftware(t *testing.T) {
	// Hold a real listener (as a 3x-ui panel would) on the first candidate.
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip("cannot listen:", err)
	}
	defer l.Close()
	held := l.Addr().(*net.TCPAddr).Port

	got, err := findFreePort(nil, held, held+50)
	if err != nil {
		t.Fatal(err)
	}
	if got == held {
		t.Fatalf("picked port %d that another program is listening on", held)
	}
	if got != held+1 {
		t.Logf("next free was %d (held+1 busy too)", got)
	}
}

func TestFindFreePortSkipsRegisteredPorts(t *testing.T) {
	old := portAvailable
	defer func() { portAvailable = old }()
	portAvailable = func(int) bool { return true }

	got, err := findFreePort([]Tunnel{{Name: "a", SocksPort: 19000}, {Name: "b", SocksPort: 19001}}, 19000, 19999)
	if err != nil || got != 19002 {
		t.Fatalf("got %d, %v; want 19002", got, err)
	}
}

func TestFindFreePortExhausted(t *testing.T) {
	old := portAvailable
	defer func() { portAvailable = old }()
	portAvailable = func(int) bool { return false }

	if _, err := findFreePort(nil, 19000, 19002); err == nil {
		t.Fatal(fmt.Sprint("expected an error when every port is busy"))
	}
}
