//go:build windows

package volume

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is the only place Shutdowner talks COM, and it runs only inside the
// logged-in user's session, never in the service.
//
// Go has no bindings for IAudioEndpointVolume and the project holds itself to
// three dependencies, so the interfaces are called through their vtables by
// hand. Every COM interface starts with IUnknown's three methods, so an
// interface's own methods begin at slot 3.
//
// Only the step-based volume methods are used. The scalar ones take a float,
// which travels in an XMM register on amd64 while syscall.SyscallN uses integer
// registers — that call would silently set a garbage volume.

var (
	modole32 = windows.NewLazySystemDLL("ole32.dll")

	procCoInitializeEx   = modole32.NewProc("CoInitializeEx")
	procCoUninitialize   = modole32.NewProc("CoUninitialize")
	procCoCreateInstance = modole32.NewProc("CoCreateInstance")
)

const (
	coinitApartmentThreaded = 0x2
	clsctxAll               = 0x17

	// EDataFlow and ERole, from mmdeviceapi.h.
	eRender     = 0
	eMultimedia = 1

	// A device is unlikely to report more steps than this; a wildly larger
	// number means we misread the struct, and stepping that many times would
	// hang the helper until its timeout.
	maxPlausibleSteps = 1000
)

// CLSID_MMDeviceEnumerator {BCDE0395-E52F-467C-8E3D-C4579291692E}
var clsidMMDeviceEnumerator = windows.GUID{
	Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C,
	Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E},
}

// IID_IMMDeviceEnumerator {A95664D2-9614-4F35-A746-DE8DB63617E6}
var iidIMMDeviceEnumerator = windows.GUID{
	Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35,
	Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6},
}

// IID_IAudioEndpointVolume {5CDF2C82-841E-4546-9722-0CF74078229A}
var iidIAudioEndpointVolume = windows.GUID{
	Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546,
	Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A},
}

// call invokes vtable slot on the COM object, passing it as the implicit
// `this`. It treats a negative HRESULT as an error.
//
// The object is held as unsafe.Pointer rather than uintptr because that is what
// it is: a pointer to memory Windows allocated outside the Go heap. Holding it
// as uintptr would require converting back, which the unsafeptr analyzer
// rightly flags — a uintptr keeps nothing alive.
func call(obj unsafe.Pointer, slot int, args ...uintptr) error {
	vtbl := *(**[64]uintptr)(obj)
	all := append([]uintptr{uintptr(obj)}, args...)
	hr, _, _ := syscall.SyscallN(vtbl[slot], all...)
	if int32(hr) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// release drops a reference. IUnknown::Release is slot 2 and returns a
// reference count rather than an HRESULT, so it does not go through call.
func release(obj unsafe.Pointer) {
	if obj == nil {
		return
	}
	vtbl := *(**[64]uintptr)(obj)
	_, _, _ = syscall.SyscallN(vtbl[2], uintptr(obj))
}

// RunHelper executes one audio operation and returns the single line of JSON
// the service reads from the helper's stdout. args excludes HelperFlag.
func RunHelper(args []string) string {
	op, want, err := ParseHelperArgs(args)
	if err != nil {
		return EncodeResult(State{}, err)
	}
	state, err := runOp(op, want)
	return EncodeResult(state, err)
}

func runOp(op Op, want State) (State, error) {
	// COM is initialised per thread, and the goroutine must not migrate to
	// another one while the interfaces are live.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_FALSE means COM was already initialised on this thread, which is fine;
	// only a negative HRESULT is a failure.
	if int32(hr) < 0 {
		return State{}, fmt.Errorf("volume: CoInitializeEx: HRESULT 0x%08X", uint32(hr))
	}
	defer procCoUninitialize.Call()

	endpoint, err := defaultEndpointVolume()
	if err != nil {
		return State{}, err
	}
	defer release(endpoint)

	switch op {
	case OpGet:
		return readState(endpoint)
	case OpSet:
		if err := writeState(endpoint, want); err != nil {
			return State{}, err
		}
		// Report what the device actually settled on rather than what was
		// asked for: a device with coarse steps cannot hit every percentage,
		// and the UI should show the truth.
		return readState(endpoint)
	}
	return State{}, fmt.Errorf("volume: unknown operation %q", op)
}

// defaultEndpointVolume returns an IAudioEndpointVolume for the default
// playback device. The caller releases it.
func defaultEndpointVolume() (unsafe.Pointer, error) {
	var enumerator unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator)),
	)
	if int32(hr) < 0 {
		return nil, fmt.Errorf("volume: creating the device enumerator: HRESULT 0x%08X", uint32(hr))
	}
	defer release(enumerator)

	var device unsafe.Pointer
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint, slot 4.
	if err := call(enumerator, 4, eRender, eMultimedia, uintptr(unsafe.Pointer(&device))); err != nil {
		return nil, fmt.Errorf("volume: no default playback device: %w", err)
	}
	defer release(device)

	var endpoint unsafe.Pointer
	// IMMDevice::Activate, slot 3.
	if err := call(device, 3,
		uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)),
		clsctxAll,
		0,
		uintptr(unsafe.Pointer(&endpoint)),
	); err != nil {
		return nil, fmt.Errorf("volume: activating the volume interface: %w", err)
	}
	return endpoint, nil
}

// stepInfo reads the device's current step and how many steps it has.
func stepInfo(endpoint unsafe.Pointer) (step, stepCount uint32, err error) {
	// IAudioEndpointVolume::GetVolumeStepInfo, slot 16.
	if err := call(endpoint, 16,
		uintptr(unsafe.Pointer(&step)),
		uintptr(unsafe.Pointer(&stepCount)),
	); err != nil {
		return 0, 0, fmt.Errorf("volume: reading the step info: %w", err)
	}
	if stepCount > maxPlausibleSteps {
		return 0, 0, fmt.Errorf("volume: the device reported %d steps, which is not plausible", stepCount)
	}
	return step, stepCount, nil
}

func readState(endpoint unsafe.Pointer) (State, error) {
	step, stepCount, err := stepInfo(endpoint)
	if err != nil {
		return State{}, err
	}
	var muted int32
	// IAudioEndpointVolume::GetMute, slot 15.
	if err := call(endpoint, 15, uintptr(unsafe.Pointer(&muted))); err != nil {
		return State{}, fmt.Errorf("volume: reading the mute flag: %w", err)
	}
	return State{Level: LevelFromStep(step, stepCount), Muted: muted != 0}, nil
}

func writeState(endpoint unsafe.Pointer, want State) error {
	step, stepCount, err := stepInfo(endpoint)
	if err != nil {
		return err
	}

	target := StepFromLevel(want.Level, stepCount)
	// Walk to the target one step at a time. There is no integer method that
	// sets a step directly; the scalar one that would takes a float and cannot
	// be called correctly through SyscallN.
	for step < target {
		// IAudioEndpointVolume::VolumeStepUp, slot 17.
		if err := call(endpoint, 17, 0); err != nil {
			return fmt.Errorf("volume: stepping up: %w", err)
		}
		step++
	}
	for step > target {
		// IAudioEndpointVolume::VolumeStepDown, slot 18.
		if err := call(endpoint, 18, 0); err != nil {
			return fmt.Errorf("volume: stepping down: %w", err)
		}
		step--
	}

	var muted int32
	if want.Muted {
		muted = 1
	}
	// IAudioEndpointVolume::SetMute, slot 14.
	if err := call(endpoint, 14, uintptr(muted), 0); err != nil {
		return fmt.Errorf("volume: setting the mute flag: %w", err)
	}
	return nil
}
