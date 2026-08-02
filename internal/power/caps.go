package power

// systemPowerCapabilities mirrors the leading fields of the Win32
// SYSTEM_POWER_CAPABILITIES struct, which GetPwrCapabilities fills in. Every
// field is a byte, so the Go layout matches the C one without padding.
//
// It lives outside the Windows-only file, like the shutdown.exe argument
// builder, so that the mapping from these flags to Capabilities can be unit
// tested on any platform. Nothing here calls into Windows.
//
// The struct continues past what is transcribed with battery scales and wake
// states; the tail is padded generously rather than spelled out, because
// GetPwrCapabilities takes no size argument and writes all of it. Over-
// allocating is safe, under-allocating corrupts the stack.
type systemPowerCapabilities struct {
	PowerButtonPresent        byte
	SleepButtonPresent        byte
	LidPresent                byte
	SystemS1                  byte
	SystemS2                  byte
	SystemS3                  byte
	SystemS4                  byte
	SystemS5                  byte
	HiberFilePresent          byte
	FullWake                  byte
	VideoDimPresent           byte
	ApmPresent                byte
	UpsPresent                byte
	ThermalControl            byte
	ProcessorThrottle         byte
	ProcessorMinThrottle      byte
	ProcessorMaxThrottle      byte
	FastSystemS4              byte
	Hiberboot                 byte
	WakeAlarmPresent          byte
	AoAc                      byte
	DiskSpinDown              byte
	HiberFileType             byte
	AoAcConnectivitySupported byte
	_                         [512]byte
}

// capabilities reduces the raw flags to the two states Shutdowner offers.
//
// Sleep is the union of the two ways Windows suspends to a low power state.
// SystemS3 is the ACPI state a machine of any age suspends to RAM with. AoAc
// ("always on, always connected") is S0 low power idle, or modern standby,
// which is what recent hardware uses instead — and when the firmware supports
// it Windows deliberately turns S3 off, so a modern laptop reports SystemS3 = 0
// while sleeping perfectly well. Reading SystemS3 alone disables the sleep
// button on every machine built in the last several years.
//
// Hibernate needs both its state and the file: powercfg /h off leaves SystemS4
// set and deletes hiberfil.sys, and hibernating without it fails.
func (c systemPowerCapabilities) capabilities() Capabilities {
	return Capabilities{
		Sleep:     c.SystemS3 != 0 || c.AoAc != 0,
		Hibernate: c.SystemS4 != 0 && c.HiberFilePresent != 0,
	}
}
