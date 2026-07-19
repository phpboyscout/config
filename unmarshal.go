package config

import (
	"reflect"
	"strings"
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

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct {
		return cfg.IsSet(prefix)
	}

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}

		name, inline, skip := configFieldName(field)
		if skip {
			continue
		}

		fieldKey := prefix
		if !inline {
			fieldKey = joinConfigPath(prefix, name)
		}

		if cfg.IsSet(fieldKey) || targetHasResolvedFields(cfg, fieldKey, field.Type) {
			return true
		}
	}

	return false
}

func configFieldName(field reflect.StructField) (name string, inline bool, skip bool) {
	tag := field.Tag.Get("mapstructure")
	if tag == "" {
		tag = field.Tag.Get("json")
	}

	if tag == "" {
		tag = field.Tag.Get("yaml")
	}

	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", false, true
		}

		for _, opt := range parts[1:] {
			if opt == "squash" || opt == "inline" {
				inline = true
			}
		}

		if parts[0] != "" {
			return parts[0], inline, false
		}
	}

	return strings.ToLower(field.Name), inline, false
}

func joinConfigPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	if name == "" {
		return prefix
	}

	return prefix + "." + name
}
