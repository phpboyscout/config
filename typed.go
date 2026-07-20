package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cast"
)

// Value reads a configuration value as T.
//
// This is the general form of the typed accessors: it decodes into whatever
// type is asked for, so it covers types this module has no method for and types
// it has never heard of. A struct, a slice, a map, an IP address, a URL, a
// timezone, or a consumer's own type implementing encoding.TextUnmarshaler all
// work, because the decoder is told how to read text into them.
//
//	port, err := config.Value[int](cfg, "server.port")
//	addr, err := config.Value[netip.Addr](cfg, "server.bind")
//	level, err := config.Value[LogLevel](cfg, "log.level")   // your own type
//
// It is a function rather than a method because Go does not allow type
// parameters on methods — the same reason [UnmarshalSection] and
// [ValidateStruct] are functions.
//
// An absent key yields the zero value and no error, matching the accessors: a
// caller distinguishing "absent" from "set to zero" asks [Reader.Has].
func Value[T any](cfg Reader, path string) (T, error) {
	var out T

	if cfg == nil {
		return out, nil
	}

	if err := cfg.UnmarshalKey(path, &out); err != nil {
		return out, fmt.Errorf("config: reading %s: %w", path, err)
	}

	return out, nil
}

// MustValue reads a configuration value as T, panicking if it cannot be
// decoded.
//
// For a value a program cannot run without, read at startup where a panic is a
// clear failure rather than a surprise later.
func MustValue[T any](cfg Reader, path string) T {
	out, err := Value[T](cfg, path)
	if err != nil {
		panic(err)
	}

	return out
}

// The accessors below exist alongside [Value] because they can be methods on an
// interface, which a generic function cannot: a consumer depending on [Reader]
// and a test supplying a mock both need them named. Each is a coercion, so a
// value the configuration holds as text reads as the type asked for.

// GetInt32 reads a value as an int32.
func (v *View) GetInt32(path string) int32 { return cast.ToInt32(v.Get(path)) }

// GetInt64 reads a value as an int64.
func (v *View) GetInt64(path string) int64 { return cast.ToInt64(v.Get(path)) }

// GetUint reads a value as a uint.
func (v *View) GetUint(path string) uint { return cast.ToUint(v.Get(path)) }

// GetUint8 reads a value as a uint8.
func (v *View) GetUint8(path string) uint8 { return cast.ToUint8(v.Get(path)) }

// GetUint16 reads a value as a uint16.
func (v *View) GetUint16(path string) uint16 { return cast.ToUint16(v.Get(path)) }

// GetUint32 reads a value as a uint32.
func (v *View) GetUint32(path string) uint32 { return cast.ToUint32(v.Get(path)) }

// GetUint64 reads a value as a uint64.
func (v *View) GetUint64(path string) uint64 { return cast.ToUint64(v.Get(path)) }

// GetFloat64 reads a value as a float64.
//
// The same as [View.GetFloat], under the name the incumbent used, so porting
// code does not have to be edited to say the same thing differently.
func (v *View) GetFloat64(path string) float64 { return v.GetFloat(path) }

// GetIntSlice reads a value as a list of ints.
func (v *View) GetIntSlice(path string) []int { return cast.ToIntSlice(v.Get(path)) }

// GetStringMap reads a value as a map of string to anything.
func (v *View) GetStringMap(path string) map[string]any {
	return cast.ToStringMap(v.Get(path))
}

// GetStringMapString reads a value as a map of string to string.
func (v *View) GetStringMapString(path string) map[string]string {
	return cast.ToStringMapString(v.Get(path))
}

// GetStringMapStringSlice reads a value as a map of string to list of string.
func (v *View) GetStringMapStringSlice(path string) map[string][]string {
	return cast.ToStringMapStringSlice(v.Get(path))
}

// GetSizeInBytes reads a value written as a size, such as "10MB", in bytes.
//
// Suffixes are the binary ones — kb, mb, gb, tb — each a multiple of 1024, and
// a bare number is a count of bytes. A value that cannot be read as a size is
// zero, which is the same answer the other accessors give.
func (v *View) GetSizeInBytes(path string) uint {
	return parseSize(v.GetString(path))
}

// parseSize reads a human-written size in bytes.
func parseSize(raw string) uint {
	const unit = 1024

	suffixes := []struct {
		name  string
		scale uint64
	}{
		{"tb", unit * unit * unit * unit},
		{"gb", unit * unit * unit},
		{"mb", unit * unit},
		{"kb", unit},
		{"b", 1},
	}

	trimmed := trimSpaceLower(raw)

	for _, s := range suffixes {
		digits, ok := cutSuffix(trimmed, s.name)
		if !ok {
			continue
		}

		size, err := cast.ToFloat64E(trimSpaceLower(digits))
		if err != nil || size < 0 {
			return 0
		}

		return uint(size * float64(s.scale))
	}

	size, err := cast.ToFloat64E(trimmed)
	if err != nil || size < 0 {
		return 0
	}

	return uint(size)
}

func trimSpaceLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func cutSuffix(s, suffix string) (string, bool) { return strings.CutSuffix(s, suffix) }
