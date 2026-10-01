package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// Runs only with REGIONHOP_REAL_SYSTEMD=1 (CI, as root, on a machine with
// systemd): starts a transient psi-tunnel@citest unit that logs a
// Psiphon-style connect notice followed by 300 noise lines -- enough to push
// it out of the 200-line window -- and checks the real systemctl/journalctl
// paths still report it as active in the right region.
func TestRealSystemd(t *testing.T) {
	if os.Getenv("REGIONHOP_REAL_SYSTEMD") != "1" {
		t.Skip("REGIONHOP_REAL_SYSTEMD not set")
	}
	unit := "psi-tunnel@citest.service"
	script := `echo '{"data":{"count":1},"noticeType":"Tunnels","timestamp":"2026-01-01T00:00:00.000Z"}'
echo '{"data":{"serverRegion":"US"},"noticeType":"ConnectedServerRegion","timestamp":"2026-01-01T00:00:00.001Z"}'
i=0; while [ $i -lt 300 ]; do echo '{"data":{"message":"noise"},"noticeType":"Info","timestamp":"2026-01-01T00:00:01.000Z"}'; i=$((i+1)); done
sleep 120`
	if out, err := exec.Command("systemd-run", "--unit="+unit, "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("systemd-run: %v\n%s", err, out)
	}
	defer exec.Command("systemctl", "stop", unit).Run()
	time.Sleep(3 * time.Second)

	states := loadUnitStates([]string{"citest", "nonexistent"})
	st := states["citest"]
	if st.Active != "active" || st.InvocationID == "" {
		t.Fatalf("citest state = %+v (all: %+v)", st, states)
	}
	if ne, ok := states["nonexistent"]; !ok || ne.Active != "inactive" {
		t.Fatalf("nonexistent state = %+v ok=%v (all: %+v)", ne, ok, states)
	}

	out, _ := tunnelLogs("citest", 200)
	if _, _, found := scanNotices(out); found {
		t.Fatal("expected the connect notice to be outside the 200-line window")
	}
	state, region := tunnelRunInfo("citest", st.InvocationID)
	if state != "active" || region != "US" {
		t.Fatalf("tunnelRunInfo = (%q, %q), want (active, US)", state, region)
	}
	info := tunnelStatusInfo("citest", st)
	if info.State != "active" || info.Region != "US" {
		t.Fatalf("tunnelStatusInfo = %+v", info)
	}
}
