package suiteenv

import "testing"

func TestOr_answersTheFallbackOnlyWhenNothingIsSet(t *testing.T) {
	t.Run("a variable that carries a value", func(t *testing.T) {
		t.Setenv("SUITEENV_PROBE", "from the environment")
		if got := Or("SUITEENV_PROBE", "the fallback"); got != "from the environment" {
			t.Errorf("value of a set variable = %q, want the one from the environment", got)
		}
	})

	t.Run("a variable that is set to nothing", func(t *testing.T) {
		t.Setenv("SUITEENV_PROBE", "")
		if got := Or("SUITEENV_PROBE", "the fallback"); got != "the fallback" {
			t.Errorf("value of an empty variable = %q, want the fallback", got)
		}
	})

	t.Run("a variable that is not set at all", func(t *testing.T) {
		if got := Or("SUITEENV_ABSENT", "the fallback"); got != "the fallback" {
			t.Errorf("value of an absent variable = %q, want the fallback", got)
		}
	})
}

func TestDatabaseURL_landsOnTheSuiteDatabaseWhenNothingSaysOtherwise(t *testing.T) {
	t.Run("nothing in the environment", func(t *testing.T) {
		t.Setenv(DatabaseURLKey, "")
		if got := DatabaseURL(); got != SuiteDatabaseURL {
			t.Errorf("database of a bare environment = %q, want the suite database", got)
		}
	})

	t.Run("an override in the environment", func(t *testing.T) {
		t.Setenv(DatabaseURLKey, "postgres://elsewhere")
		if got := DatabaseURL(); got != "postgres://elsewhere" {
			t.Errorf("database under an override = %q, want the override", got)
		}
	})
}
