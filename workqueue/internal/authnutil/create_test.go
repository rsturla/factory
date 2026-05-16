package authnutil

import "testing"

func TestCreateFromEnvNoop(t *testing.T) {
	t.Setenv("AUTHN_BACKEND", "noop")
	auth, err := CreateFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authenticator")
	}
}

func TestCreateFromEnvDefault(t *testing.T) {
	t.Setenv("AUTHN_BACKEND", "")
	auth, err := CreateFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authenticator for default")
	}
}

func TestCreateFromEnvUnsupported(t *testing.T) {
	t.Setenv("AUTHN_BACKEND", "invalid")
	_, err := CreateFromEnv()
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}
