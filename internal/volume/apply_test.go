package volume

import "testing"

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		current State
		level   *int
		muted   *bool
		want    State
	}{
		{"a level change clears mute", State{Level: 30, Muted: true}, intPtr(60), nil, State{Level: 60, Muted: false}},
		{"muting preserves the level", State{Level: 60}, nil, boolPtr(true), State{Level: 60, Muted: true}},
		{"unmuting restores the same level", State{Level: 60, Muted: true}, nil, boolPtr(false), State{Level: 60, Muted: false}},
		{"an explicit mute wins over the implicit unmute", State{Level: 30}, intPtr(60), boolPtr(true), State{Level: 60, Muted: true}},
		{"neither field leaves the state alone", State{Level: 42, Muted: true}, nil, nil, State{Level: 42, Muted: true}},
		{"a negative level clamps to zero", State{Level: 50}, intPtr(-5), nil, State{Level: 0}},
		{"an over-range level clamps to a hundred", State{Level: 50}, intPtr(105), nil, State{Level: 100}},
		// Sliding to zero is a volume of zero, not a mute — the device stays
		// unmuted so nudging the slider back up is audible immediately.
		{"zero is a level, not a mute", State{Level: 50, Muted: true}, intPtr(0), nil, State{Level: 0, Muted: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Apply(tt.current, tt.level, tt.muted); got != tt.want {
				t.Errorf("Apply(%+v, level, muted) = %+v, want %+v", tt.current, got, tt.want)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	for _, tt := range []struct{ in, want int }{
		{-1000, 0}, {-1, 0}, {0, 0}, {50, 50}, {100, 100}, {101, 100}, {1000, 100},
	} {
		if got := Clamp(tt.in); got != tt.want {
			t.Errorf("Clamp(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
