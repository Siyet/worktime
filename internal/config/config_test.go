package config

import "testing"

func TestLoadRequiresExplicitAdminsAndSupportsNoEgress(t *testing.T) {
	t.Setenv("WORKTIME_ADMIN_EMAILS", " Owner@Example.COM, second@example.com ")
	t.Setenv("WORKTIME_UPDATE_CHECKS", "0")
	t.Setenv("WORKTIME_TRUST_PROXY", "1")
	config := Load()
	if config.UpdateChecks {
		t.Fatal("WORKTIME_UPDATE_CHECKS=0 did not disable checks")
	}
	if len(config.AdminEmails) != 2 ||
		config.AdminEmails[0] != "owner@example.com" ||
		config.AdminEmails[1] != "second@example.com" {
		t.Fatalf("admin emails = %#v", config.AdminEmails)
	}
	if !config.TrustProxy {
		t.Fatal("WORKTIME_TRUST_PROXY=1 did not enable trusted proxy headers")
	}
}

func TestLoadDefaultsToNoAdministrators(t *testing.T) {
	t.Setenv("WORKTIME_ADMIN_EMAILS", "")
	config := Load()
	if len(config.AdminEmails) != 0 {
		t.Fatalf("unexpected implicit administrators: %#v", config.AdminEmails)
	}
}
