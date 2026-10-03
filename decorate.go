package config

import "time"

// What a decorator over a Backend forwards so the Store treats the wrapped
// source exactly as it would the bare one. Every interface here behaves the
// same absent as delegated to nothing, which is why Filtered and Constrained
// implement them all rather than varying their type on each. go/config#14.

func innerSourceKind(inner Backend) SourceKind {
	if d, ok := inner.(SourceKindDeclarer); ok {
		return d.SourceKind()
	}

	return SourceFile
}

func innerPollInterval(inner Backend) time.Duration {
	if h, ok := inner.(PollIntervalHinter); ok {
		return h.PollInterval()
	}

	return 0
}

func forwardWatchErrorHandler(inner Backend, fn func(error)) {
	if r, ok := inner.(WatchErrorReporter); ok {
		r.SetWatchErrorHandler(fn)
	}
}

func innerWatchPath(inner Backend) (string, bool) {
	if p, ok := inner.(WatchPathReporter); ok {
		return p.WatchPath()
	}

	return "", false
}

func innerPinOnly(inner Backend) bool {
	p, ok := inner.(pinOnlyBackend)

	return ok && p.pinOnlyLayers()
}

func innerHasConstraint(inner Backend) bool {
	c, ok := inner.(sourceConstraint)

	return ok && c.hasConstraint()
}

func innerConstrain(inner Backend, layers []Layer, r *ValidationResult) {
	if c, ok := inner.(sourceConstraint); ok {
		c.constrain(layers, r)
	}
}

func innerWithheldSensitive(inner Backend) []string {
	if w, ok := inner.(sensitiveWithholder); ok {
		return w.withheldSensitive()
	}

	return nil
}
