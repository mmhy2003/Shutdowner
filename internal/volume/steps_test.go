package volume

import "testing"

func TestLevelFromStep(t *testing.T) {
	tests := []struct {
		name      string
		step      uint32
		stepCount uint32
		want      int
	}{
		// Windows commonly reports 101 steps, which maps exactly onto 0-100.
		{"101 steps, silent", 0, 101, 0},
		{"101 steps, halfway", 50, 101, 50},
		{"101 steps, full", 100, 101, 100},
		{"17 steps, halfway", 8, 17, 50},
		{"17 steps, full", 16, 17, 100},
		{"rounds to nearest", 1, 3, 50},
		// Degenerate device reports must not divide by zero.
		{"one step is always full", 0, 1, 100},
		{"no steps reported", 0, 0, 0},
		// A step beyond the count is saturated rather than exceeding 100.
		{"step past the end", 200, 101, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LevelFromStep(tt.step, tt.stepCount); got != tt.want {
				t.Errorf("LevelFromStep(%d, %d) = %d, want %d", tt.step, tt.stepCount, got, tt.want)
			}
		})
	}
}

func TestStepFromLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     int
		stepCount uint32
		want      uint32
	}{
		{"101 steps, silent", 0, 101, 0},
		{"101 steps, halfway", 50, 101, 50},
		{"101 steps, full", 100, 101, 100},
		{"17 steps, halfway", 50, 17, 8},
		{"17 steps, full", 100, 17, 16},
		{"out-of-range level is clamped first", 250, 101, 100},
		{"negative level is clamped first", -5, 101, 0},
		{"one step", 50, 1, 0},
		{"no steps reported", 50, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StepFromLevel(tt.level, tt.stepCount); got != tt.want {
				t.Errorf("StepFromLevel(%d, %d) = %d, want %d", tt.level, tt.stepCount, got, tt.want)
			}
		})
	}
}

func TestStepRoundTripIsStable(t *testing.T) {
	// Converting a level to a step and back must not drift, or repeatedly
	// nudging the slider would walk the volume away from where it was put.
	for _, stepCount := range []uint32{101, 51, 17, 2} {
		for level := 0; level <= 100; level++ {
			step := StepFromLevel(level, stepCount)
			back := LevelFromStep(step, stepCount)
			again := StepFromLevel(back, stepCount)
			if again != step {
				t.Fatalf("stepCount=%d level=%d: step %d -> level %d -> step %d, want a stable round trip",
					stepCount, level, step, back, again)
			}
		}
	}
}
