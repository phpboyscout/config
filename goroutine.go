package config

import (
	"runtime"
	"strconv"
)

// goroutineID returns an identifier for the calling goroutine.
//
// Go does not expose this, and the omission is deliberate: goroutine-local
// state is an anti-pattern, because anything keyed on it survives into code
// that never asked for it and cannot see it. This is not that. The identifier
// is never stored, never keyed on beyond the life of a single call, and never
// influences behaviour except to answer one question — is this write coming
// from inside an observer the Store itself is running?
//
// That question genuinely requires goroutine identity. Notification runs
// outside the Store lock, so an unrelated goroutine may legitimately write
// while observers execute. A flag or mutex cannot separate the two, and would
// refuse that legitimate write; only knowing *which* goroutine is asking can.
//
// The alternative considered was threading a marked context through
// Observable.Run. It is more idiomatic, but an observer calling
// context.Background() would defeat it silently — turning a guaranteed refusal
// into one that holds when the caller cooperates, which is not a guarantee.
//
// The cost is roughly 2µs, paid only when a notification is actually in flight
// (see notifier.insideObserver) and only on a path that is about to touch the
// filesystem anyway.
func goroutineID() uint64 {
	// "goroutine 123 [running]:\n..." — the identifier is the second field, and
	// the buffer only has to be long enough to reach the space after it.
	var buf [40]byte

	n := runtime.Stack(buf[:], false)

	const prefix = len("goroutine ")
	if n <= prefix {
		return 0
	}

	digits := buf[prefix:n]

	for i := range digits {
		if digits[i] == ' ' {
			id, err := strconv.ParseUint(string(digits[:i]), 10, 64)
			if err != nil {
				return 0
			}

			return id
		}
	}

	return 0
}
