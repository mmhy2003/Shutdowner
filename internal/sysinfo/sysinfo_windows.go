//go:build windows

package sysinfo

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// osVersion reads the version through RtlGetVersion rather than shelling out,
// which avoids parsing localized command output. The friendly name is a best
// effort; the build number beside it is exact.
func osVersion() string {
	v := windows.RtlGetVersion()
	name := "Windows"
	switch {
	case v.MajorVersion == 10 && v.BuildNumber >= 22000:
		name = "Windows 11"
	case v.MajorVersion == 10:
		name = "Windows 10"
	}
	return fmt.Sprintf("%s (build %d)", name, v.BuildNumber)
}

func uptime() time.Duration {
	ms, _, _ := procGetTickCount64.Call()
	return time.Duration(ms) * time.Millisecond
}
