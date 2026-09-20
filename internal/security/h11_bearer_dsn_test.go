package security

import "testing"

// TestBearerTokenPlaceholderNeverBlocks is review H11: bearer token had no
// validator at all -- no entropy, placeholder, template or test-path gate --
// so "Bearer YOUR_TOKEN_HERE_XXXXXXXXXX" raised a critical BLOCKING finding
// even in a *_test.go file. It now gets the same placeholder/template gate
// as the generic assignment rule.
func TestBearerTokenPlaceholderNeverBlocks(t *testing.T) {
	placeholders := []string{
		"Bearer YOUR_TOKEN_HERE_XXXXXXXXXX",
		"Bearer EXAMPLE_BEARER_TOKEN_VALUE",
		"Bearer changeme_changeme_changeme",
	}
	for _, text := range placeholders {
		if FindCredential(text) != "" {
			t.Errorf("placeholder bearer token %q was matched as a credential", text)
		}
		matches := FindCredentialsInFile("handler.go", text)
		for _, m := range matches {
			if m.Kind == "bearer token" {
				t.Errorf("placeholder bearer token %q raised a bearer token finding: %+v", text, m)
			}
		}
		// Still not a credential; redaction of an already-fake placeholder is
		// harmless either way, so this only pins the finding side.
	}

	const real = "Bearer aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0123456"
	if FindCredential(real) == "" {
		t.Fatal("a real-looking bearer token must still be detected")
	}
	if got := Redact(real); got == real {
		t.Fatalf("Redact left a real-looking bearer token in place: %q", got)
	}
	// A real-looking bearer token still blocks even in a test/fixture path
	// (only the placeholder/template gate is new -- test-path downgrade to
	// advisory is also new for this kind, but only for a match that passes
	// the gate in the first place... this asserts it's found and, since it's
	// realistic (not a placeholder), still visible).
	matches := FindCredentialsInFile("handler_test.go", real)
	found := false
	for _, m := range matches {
		if m.Kind == "bearer token" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a bearer token match for a realistic value in a test file")
	}
}

// TestBearerTokenAdvisoryInTestPath is review H11's test-path extension: a
// realistic-looking (non-placeholder) bearer token in a *_test.go file is
// downgraded to advisory, the same treatment the generic assignment rule
// already gets, since a hardcoded fixture header is common there.
func TestBearerTokenAdvisoryInTestPath(t *testing.T) {
	const value = "Bearer aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0123456"
	testMatches := FindCredentialsInFile("handler_test.go", value)
	found := false
	for _, m := range testMatches {
		if m.Kind != "bearer token" {
			continue
		}
		found = true
		if !m.Advisory {
			t.Errorf("bearer token match in a _test.go file: Advisory = false, want true: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected a bearer token match in the test-path fixture")
	}

	nonTestMatches := FindCredentialsInFile("internal/api/handler.go", value)
	found = false
	for _, m := range nonTestMatches {
		if m.Kind != "bearer token" {
			continue
		}
		found = true
		if m.Advisory {
			t.Errorf("bearer token match outside a test path: Advisory = true, want false: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected a bearer token match outside a test path too")
	}
}

// TestURLUserinfoCredentialPlaceholderNeverBlocks is review H11: URL
// userinfo credential had no validator at all, so
// "mysql://root:examplepass@127.0.0.1/db" raised a critical BLOCKING
// finding even in a *_test.go file. It now gets the same placeholder/
// template gate as the generic assignment rule, while a real-looking DSN
// password still blocks (and is still redacted) everywhere.
func TestURLUserinfoCredentialPlaceholderNeverBlocks(t *testing.T) {
	placeholders := []string{
		"mysql://root:example_pass@127.0.0.1/db",
		"postgres://admin:changeme123@localhost/app",
		"redis://user:dummy_value@cache.local:6379",
	}
	for _, text := range placeholders {
		if FindCredential(text) != "" {
			t.Errorf("placeholder DSN %q was matched as a credential", text)
		}
		matches := FindCredentialsInFile("config.go", text)
		for _, m := range matches {
			if m.Kind == "URL userinfo credential" {
				t.Errorf("placeholder DSN %q raised a URL userinfo credential finding: %+v", text, m)
			}
		}
	}

	const real = "postgres://admin:hunter23456789@db.example.com/prod"
	if FindCredential(real) == "" {
		t.Fatal("a real-looking DSN password must still be detected")
	}
	if got := Redact(real); got == real {
		t.Fatalf("Redact left a real-looking DSN password in place: %q", got)
	}
}

// TestURLUserinfoCredentialAdvisoryInTestPath mirrors
// TestBearerTokenAdvisoryInTestPath for the URL userinfo credential kind.
func TestURLUserinfoCredentialAdvisoryInTestPath(t *testing.T) {
	const value = "postgres://admin:hunter23456789@db.example.com/prod"
	testMatches := FindCredentialsInFile("db_test.go", value)
	found := false
	for _, m := range testMatches {
		if m.Kind != "URL userinfo credential" {
			continue
		}
		found = true
		if !m.Advisory {
			t.Errorf("URL userinfo credential match in a _test.go file: Advisory = false, want true: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected a URL userinfo credential match in the test-path fixture")
	}

	nonTestMatches := FindCredentialsInFile("internal/db/conn.go", value)
	found = false
	for _, m := range nonTestMatches {
		if m.Kind != "URL userinfo credential" {
			continue
		}
		found = true
		if m.Advisory {
			t.Errorf("URL userinfo credential match outside a test path: Advisory = true, want false: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected a URL userinfo credential match outside a test path too")
	}
}
