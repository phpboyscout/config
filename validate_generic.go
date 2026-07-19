package config

import (
	"fmt"
	"reflect"
	"sync"
)

// schemaCache memoises schemas derived from a type's struct tags. A schema for a
// given type built without extra options is immutable, so it is safe to reuse
// across calls and goroutines.
var schemaCache sync.Map // reflect.Type -> *Schema

// SchemaOf returns a Schema derived from the struct tags of T. For the
// option-free call the result is cached per type, so repeated calls for the same
// T do not re-reflect. When opts are supplied (for example WithStrictMode) the
// schema is built fresh and not cached, since options can change the result.
//
// T must be a struct; see WithStructSchema for the supported tags.
func SchemaOf[T any](opts ...SchemaOption) (*Schema, error) {
	if len(opts) == 0 {
		t := reflect.TypeFor[T]()
		if cached, ok := schemaCache.Load(t); ok {
			return cached.(*Schema), nil
		}

		schema, err := NewSchema(WithStructSchema(*new(T)))
		if err != nil {
			return nil, err
		}

		schemaCache.Store(t, schema)

		return schema, nil
	}

	return NewSchema(append([]SchemaOption{WithStructSchema(*new(T))}, opts...)...)
}

// ValidateStruct checks a view against the schema derived from T's struct tags,
// returning a formatted error if any rule fails and nil if it does not.
//
// It is the shortest way to validate the slice of configuration a command or
// feature cares about, without building a Schema by hand:
//
//	if err := config.ValidateStruct[MyConfig](store.View()); err != nil {
//		return err
//	}
//
// A scoped view validates its own subtree, so a schema written for a section
// can be applied to that section:
//
//	if err := config.ValidateStruct[Server](store.View().Sub("server")); err != nil {
//		return err
//	}
//
// Schema options such as [WithStrictMode] may be passed through.
func ValidateStruct[T any](cfg *View, opts ...SchemaOption) error {
	schema, err := SchemaOf[T](opts...)
	if err != nil {
		return err
	}

	if result := cfg.Validate(schema); !result.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, result.Error())
	}

	return nil
}
