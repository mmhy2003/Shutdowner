package sysinfo

import (
	"testing"
	"time"
)

func TestCollectReturnsUsableValues(t *testing.T) {
	info := Collect()

	if info.Hostname == "" {
		t.Error("Hostname is empty")
	}
	if info.OS == "" {
		t.Error("OS is empty")
	}
	if info.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds = %d, want >= 0", info.UptimeSeconds)
	}
	if _, err := time.Parse(time.RFC3339, info.LocalTime); err != nil {
		t.Errorf("LocalTime = %q, which does not parse as RFC3339: %v", info.LocalTime, err)
	}
}

func TestUptimeAdvances(t *testing.T) {
	first := Collect().UptimeSeconds
	time.Sleep(10 * time.Millisecond)
	if second := Collect().UptimeSeconds; second < first {
		t.Errorf("uptime went backwards: %d then %d", first, second)
	}
}
