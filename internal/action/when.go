package action

import (
	"errors"
	"fmt"
	"time"
)

// AtLayout is the shape an absolute schedule arrives in: a naive wall clock
// with no offset, which is exactly what <input type="datetime-local"> produces.
// The absence of an offset is the point — the time means what it says on the
// PC's clock, not on the clock of whatever device is holding the browser.
const AtLayout = "2006-01-02T15:04"

// MaxHorizon caps how far ahead an action may be scheduled. A schedule further
// out than a week is likelier to be forgotten than honoured.
const MaxHorizon = 7 * 24 * time.Hour

var (
	ErrConflictingWhen = errors.New("action: give either delaySeconds or at, not both")
	ErrDelayRange      = fmt.Errorf("action: delaySeconds must be between 0 and %d", int(MaxHorizon/time.Second))
	ErrBadAtFormat     = fmt.Errorf("action: at must look like %s, with no timezone", AtLayout)
	ErrAtInPast        = errors.New("action: at is in the past")
	ErrAtTooFar        = fmt.Errorf("action: at is more than %d days ahead", int(MaxHorizon/(24*time.Hour)))
)

// ResolveWhen turns a request's optional timing fields into the absolute
// instant the action should fire. Both fields absent means the configured
// fallback delay, which is what keeps every existing caller working unchanged.
//
// now carries the server's location, and at is parsed in it. That single
// detail is the whole of the "the PC's clock decides" decision.
func ResolveWhen(now time.Time, delaySeconds *int, at string, fallback time.Duration) (time.Time, error) {
	if delaySeconds != nil && at != "" {
		return time.Time{}, ErrConflictingWhen
	}

	switch {
	case delaySeconds != nil:
		d := time.Duration(*delaySeconds) * time.Second
		if *delaySeconds < 0 || d > MaxHorizon {
			return time.Time{}, ErrDelayRange
		}
		return now.Add(d), nil

	case at != "":
		firesAt, err := time.ParseInLocation(AtLayout, at, now.Location())
		if err != nil {
			return time.Time{}, ErrBadAtFormat
		}
		if firesAt.Before(now) {
			// Quoting the PC's clock is what lets a timezone mix-up diagnose
			// itself: the operator sees the machine disagreeing with the phone.
			return time.Time{}, fmt.Errorf("%w; it is currently %s on the PC",
				ErrAtInPast, now.Format("2006-01-02 15:04"))
		}
		if firesAt.Sub(now) > MaxHorizon {
			return time.Time{}, ErrAtTooFar
		}
		return firesAt, nil

	default:
		return now.Add(fallback), nil
	}
}
