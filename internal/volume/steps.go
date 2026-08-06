package volume

// Windows exposes master volume as a number of discrete steps rather than a
// percentage, and the step-based COM methods are the only ones whose arguments
// are all integers — the scalar ones take a float, which cannot cross
// syscall.SyscallN correctly on amd64. These two functions are the whole
// translation, and being ordinary arithmetic they are the one part of the audio
// path that can be tested without Windows.

// LevelFromStep converts a device step index into whole percent. stepCount is
// the total number of steps, so the highest valid index is stepCount-1.
func LevelFromStep(step, stepCount uint32) int {
	if stepCount == 0 {
		return 0
	}
	if stepCount == 1 {
		// A device with a single step is either on or off, and it is on.
		return MaxLevel
	}
	top := stepCount - 1
	if step > top {
		step = top
	}
	// Rounded rather than truncated so a halfway step reads as 50, not 49.
	return int((uint64(step)*uint64(MaxLevel) + uint64(top)/2) / uint64(top))
}

// StepFromLevel converts whole percent into a device step index.
func StepFromLevel(level int, stepCount uint32) uint32 {
	if stepCount <= 1 {
		return 0
	}
	level = Clamp(level)
	top := stepCount - 1
	return uint32((uint64(level)*uint64(top) + MaxLevel/2) / MaxLevel)
}
