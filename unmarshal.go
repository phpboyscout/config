package config

import (
	"reflect"
)

// Section is the result of unmarshalling an optional configuration section.
type Section[T any] struct {
	Value  T
	Exists bool
}

// Unmarshal decodes the full resolved configuration into target.
func UnmarshalSection[T any](cfg Reader, key string) (Section[T], error) {
	var out T

	if cfg == nil {
		return Section[T]{}, nil
	}

	exists := cfg.SectionExists(key) || targetHasResolvedFields(cfg, key, reflect.TypeOf(out))
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

func targetHasResolvedFields(cfg Reader, prefix string, typ reflect.Type) bool {
	if typ == nil {
		return false
	}

	typ = deref(typ)

	if typ.Kind() != reflect.Struct {
		return cfg.IsSet(prefix)
	}

	for i := range typ.NumField() {
		if fieldIsResolved(cfg, prefix, typ.Field(i)) {
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
func fieldIsResolved(cfg Reader, prefix string, field reflect.StructField) bool {
	if field.PkgPath != "" {
		return false
	}

	canonical, aliases, inline, skip := fieldKeys(field)
	if skip {
		return false
	}

	if inline {
		return targetHasResolvedFields(cfg, prefix, field.Type)
	}

	if resolvedUnder(cfg, prefix, canonical, field.Type) {
		return true
	}

	for _, alias := range aliases {
		if resolvedUnder(cfg, prefix, alias, field.Type) {
			return true
		}
	}

	return false
}

// resolvedUnder reports whether one spelling of a field has a value.
func resolvedUnder(cfg Reader, prefix, name string, typ reflect.Type) bool {
	key := joinPath(prefix, name)

	return cfg.IsSet(key) || targetHasResolvedFields(cfg, key, typ)
}
