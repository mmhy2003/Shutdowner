package volume

// A level is whole percent.
const (
	MinLevel = 0
	MaxLevel = 100
)

// Apply resolves a partial request against the current state.
//
// A level change clears mute — that is the whole rule, in one place, so the
// browser and the server can never disagree about it. An explicit muted value
// still wins, because the caller said what it wanted.
func Apply(current State, level *int, muted *bool) State {
	next := current
	if level != nil {
		next.Level = Clamp(*level)
		next.Muted = false
	}
	if muted != nil {
		next.Muted = *muted
	}
	return next
}

// Clamp bounds a level to [MinLevel, MaxLevel].
func Clamp(level int) int {
	if level < MinLevel {
		return MinLevel
	}
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}
