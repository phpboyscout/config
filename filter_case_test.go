package config_test

import (
	"context"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// A Store folds every key, so a pattern has to match every spelling of the key
// it names. Matching the backend's raw spelling instead let a deny be bypassed
// by case, and folding only the key would stop a mixed-case pattern matching
// the spelling it was written for. Spec 0015 D10.

func TestFiltered_DenyMatchesEverySpelling(t *testing.T) {
	t.Parallel()

	spellings := map[string]map[string]any{
		"lower": {"db": map[string]any{"password": "s3cret", "user": "app"}},
		"upper": {"DB": map[string]any{"PASSWORD": "s3cret", "USER": "app"}},
		"mixed": {"Db": map[string]any{"Password": "s3cret", "User": "app"}},
	}

	for _, pattern := range []string{"db.password", "DB.Password", "*.PASSWORD"} {
		for name, values := range spellings {
			t.Run(pattern+"/"+name, func(t *testing.T) {
				t.Parallel()

				view := filteredStoreOver(t, values, config.Deny(pattern)).View()

				if got := view.GetString("db.password"); got != "" {
					t.Errorf("db.password = %q, want it hidden by Deny(%q)", got, pattern)
				}

				if got := view.GetString("db.user"); got != "app" {
					t.Errorf("db.user = %q, want app; only the password was denied", got)
				}
			})
		}
	}
}

func TestFiltered_AllowMatchesEverySpelling(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"db.user", "DB.User"} {
		for _, values := range []map[string]any{
			{"db": map[string]any{"password": "s3cret", "user": "app"}},
			{"DB": map[string]any{"PASSWORD": "s3cret", "USER": "app"}},
		} {
			view := filteredStoreOver(t, values, config.Allow(pattern)).View()

			if got := view.GetString("db.user"); got != "app" {
				t.Errorf("Allow(%q) over %v: db.user = %q, want app", pattern, values, got)
			}

			if got := view.GetString("db.password"); got != "" {
				t.Errorf("Allow(%q) over %v: db.password = %q, want it hidden", pattern, values, got)
			}
		}
	}
}

func TestFiltered_AMisCasedWriteToADeniedSecretIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		held  map[string]any
		write string
	}{
		{map[string]any{"db": map[string]any{"password": "s3cret"}}, "DB.Password"},
		{map[string]any{"DB": map[string]any{"PASSWORD": "s3cret"}}, "db.password"},
		{map[string]any{"DB": map[string]any{"PASSWORD": "s3cret"}}, "Db.Password"},
	} {
		store := filteredStoreOver(t, tc.held, config.Deny("db.password"))

		_, err := store.Apply(context.Background(), config.Set(tc.write, "new"))
		if !errors.Is(err, config.ErrSensitiveLeak) {
			t.Errorf("Apply(Set(%q)) over %v: err = %v, want ErrSensitiveLeak", tc.write, tc.held, err)
		}
	}
}

func TestConstrained_ForbidMatchesEverySpelling(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"credentials.*", "Credentials.*", "CREDENTIALS.Token"} {
		for _, yaml := range []string{
			"credentials:\n  token: literal-secret\n",
			"Credentials:\n  Token: literal-secret\n",
		} {
			_, err := constrainedStore(t, yaml, config.Forbid(pattern))
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Errorf("Forbid(%q) over %q: err = %v, want ErrInvalidConfig", pattern, yaml, err)
			}
		}
	}
}
