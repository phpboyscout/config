package config

import (
	"reflect"
)

// Section is the result of unmarshalling an optional configuration section.
type Section[T any] struct {
	Value  T
	Exists bool
}

// UnmarshalSection decodes the section at key into a T. Exists is false, with
// no error, when neither the section nor any of T's fields resolve under key.
//
// A Reader that does not implement [KeyCaseReporter] is probed for T's fields
// as folded, so a wrapper around a View from a [WithCaseSensitiveKeys] Store
// must forward CaseSensitiveKeys for the probe to match exactly.
func UnmarshalSection[T any](cfg Reader, key string) (Section[T], error) {
	var out T

	if cfg == nil {
		return Section[T]{}, nil
	}

	exists := cfg.SectionExists(key) || targetHasResolvedFields(cfg, key, reflect.TypeOf(out), readerKeyCase(cfg))
	if !exists {
		return Section[T]{}, nil
	}

	if err := cfg.UnmarshalKey(key, &out); err != nil {
		return Section[T]{}, err
	}

	return Section[T]{Value: out, Exists: true}, nil
}

// MustUnmarshalSection decodes key into T and panics if decoding fails.
func MustUnmarshalSection[T any](cfg Reader, key string) Section[T] {
	section, err := UnmarshalSection[T](cfg, key)
	if err != nil {
		panic(err)
	}

	return section
}

func targetHasResolvedFields(cfg Reader, prefix string, typ reflect.Type, keys keyCase) bool {
	if typ == nil {
		return false
	}

	typ = deref(typ)

	if typ.Kind() != reflect.Struct {
		return cfg.IsSet(prefix)
	}

	for i := range typ.NumField() {
		if fieldIsResolved(cfg, prefix, typ.Field(i), keys) {
			return true
		}
	}

	return false
}

// fieldIsResolved reports whether one struct field has a value configured under
// any spelling the decoder would accept.
//
// Every spelling, because "is this section configured" and "which key do I read
// it from" have to be the same question. Testing only one meant a field written
// under an alias reported the section absent, and the caller silently received
// a zero value the decoder would have filled in.
func fieldIsResolved(cfg Reader, prefix string, field reflect.StructField, keys keyCase) bool {
	if field.PkgPath != "" {
		return false
	}

	canonical, aliases, inline, skip := fieldKeys(field, keys)
	if skip {
		return false
	}

	if inline {
		return targetHasResolvedFields(cfg, prefix, field.Type, keys)
	}

	if resolvedUnder(cfg, prefix, canonical, field.Type, keys) {
		return true
	}

	for _, alias := range aliases {
		if resolvedUnder(cfg, prefix, alias, field.Type, keys) {
			return true
		}
	}

	return false
}

// resolvedUnder reports whether one spelling of a field has a value.
func resolvedUnder(cfg Reader, prefix, name string, typ reflect.Type, keys keyCase) bool {
	key := joinPath(prefix, name)

	return cfg.IsSet(key) || targetHasResolvedFields(cfg, key, typ, keys)
}
