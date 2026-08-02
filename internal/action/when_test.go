package action

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func intPtr(n int) *int { return &n }

// A fixed non-UTC location, so a bug that resolves "at" in UTC instead of the
// server's location shows up as a three-hour error rather than passing.
var testLoc = time.FixedZone("test", 3*60*60)

func TestResolveWhen(t *testing.T) {
	now := time.Date(2026, 8, 2, 14, 30, 0, 0, testLoc)
	fallback := 45 * time.Second

	tests := []struct {
		name         string
		delaySeconds *int
		at           string
		want         time.Time
		wantErr      error
	}{
		{
			name: "neither field falls back to the configured delay",
			want: now.Add(45 * time.Second),
		},
		{
			name:         "delaySeconds is relative to now",
			delaySeconds: intPtr(7200),
			want:         now.Add(2 * time.Hour),
		},
		{
			name:         "a zero delay is legal and means now",
			delaySeconds: intPtr(0),
			want:         now,
		},
		{
			name: "at is interpreted in the server's location",
			at:   "2026-08-03T03:00",
			want: time.Date(2026, 8, 3, 3, 0, 0, 0, testLoc),
		},
		{
			name: "at may be later the same day",
			at:   "2026-08-02T23:15",
			want: time.Date(2026, 8, 2, 23, 15, 0, 0, testLoc),
		},
		{
			name:         "both fields is a conflict",
			delaySeconds: intPtr(60),
			at:           "2026-08-03T03:00",
			wantErr:      ErrConflictingWhen,
		},
		{
			name:         "a negative delay is refused",
			delaySeconds: intPtr(-1),
			wantErr:      ErrDelayRange,
		},
		{
			name:         "a delay past the horizon is refused",
			delaySeconds: intPtr(int(MaxHorizon/time.Second) + 1),
			wantErr:      ErrDelayRange,
		},
		{
			name:         "a delay exactly at the horizon is allowed",
			delaySeconds: intPtr(int(MaxHorizon / time.Second)),
			want:         now.Add(MaxHorizon),
		},
		{
			name:    "an unparseable at is refused",
			at:      "tomorrow please",
			wantErr: ErrBadAtFormat,
		},
		{
			name:    "an at carrying an offset is refused, since it is meant to be naive",
			at:      "2026-08-03T03:00:00+05:00",
			wantErr: ErrBadAtFormat,
		},
		{
			name:    "an at in the past is refused",
			at:      "2026-08-02T14:29",
			wantErr: ErrAtInPast,
		},
		{
			name:    "an at past the horizon is refused",
			at:      "2026-08-10T00:00",
			wantErr: ErrAtTooFar,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWhen(now, tt.delaySeconds, tt.at, fallback)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolveWhen() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWhen() error = %v, want nil", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ResolveWhen() = %s, want %s", got, tt.want)
			}
		})
	}
}

// The error text is what the operator sees when their phone and the PC
// disagree about the time, so it has to name the PC's clock.
func TestResolveWhenPastErrorQuotesThePCsClock(t *testing.T) {
	now := time.Date(2026, 8, 2, 14, 30, 0, 0, testLoc)
	_, err := ResolveWhen(now, nil, "2026-08-02T09:00", 45*time.Second)
	if err == nil {
		t.Fatal("ResolveWhen() error = nil, want a past-time error")
	}
	if !strings.Contains(err.Error(), "14:30") {
		t.Errorf("error %q does not quote the PC's current time", err)
	}
}
