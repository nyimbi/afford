package config

import "testing"

func TestPermitsIsClassBased(t *testing.T) {
	none := Default()
	if none.Safety.Permits("delete") {
		t.Error("deletion must be denied by default")
	}
	if none.Safety.Permits("side_effect") {
		t.Error("side effects must be denied by default")
	}

	del := Default()
	del.Safety.AllowDelete = true
	if !del.Safety.Permits("delete") {
		t.Error("allow_delete should permit deletion")
	}
	if del.Safety.Permits("side_effect") {
		t.Error("allow_delete must not permit side effects")
	}

	side := Default()
	side.Safety.AllowSideEffects = true
	if side.Safety.Permits("delete") {
		t.Error("allow_side_effects must not permit deletion")
	}
	if !side.Safety.Permits("side_effect") {
		t.Error("allow_side_effects should permit side effects")
	}

	umbrella := Default()
	umbrella.Safety.AllowDestructive = true
	if !umbrella.Safety.Permits("delete") || !umbrella.Safety.Permits("side_effect") {
		t.Error("allow_destructive should permit both classes")
	}
}

func TestUnknownClassIsDenied(t *testing.T) {
	cfg := Default()
	cfg.Safety.AllowDelete = true
	if cfg.Safety.Permits("") || cfg.Safety.Permits("nonsense") {
		t.Error("an unrecognised class must not be permitted")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cfg := Default()
	cfg.Run.BaseURL = "ftp://x.test"
	if err := cfg.Validate(); err == nil {
		t.Error("non-http base_url should be rejected")
	}
	cfg = Default()
	cfg.Run.BaseURL = "http://x.test"
	cfg.Limits.Concurrency = 0
	if err := cfg.Validate(); err == nil {
		t.Error("zero concurrency should be rejected")
	}
}
