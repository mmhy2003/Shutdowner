//go:build !windows

package sysinfo

import (
	"runtime"
	"time"
)

var processStart = time.Now()

func osVersion() string { return runtime.GOOS }

// uptime reports time since process start off Windows. The dashboard needs a
// plausible number during development, not an accurate one.
func uptime() time.Duration { return time.Since(processStart) }
