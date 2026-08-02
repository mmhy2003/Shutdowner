// Package sysinfo reports the facts the dashboard shows about the machine.
package sysinfo

import (
	"os"
	"time"
)

type Info struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	LocalTime     string `json:"localTime"`
}

// Collect gathers the current machine facts. It never fails: a value that
// cannot be read is reported as "unknown" rather than breaking the dashboard.
func Collect() Info {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return Info{
		Hostname:      host,
		OS:            osVersion(),
		UptimeSeconds: int64(uptime() / time.Second),
		LocalTime:     time.Now().Format(time.RFC3339),
	}
}
