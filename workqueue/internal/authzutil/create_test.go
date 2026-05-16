package authzutil

import "testing"

func TestCreateFromEnvNoop(t *testing.T) {
	t.Setenv("AUTHZ_BACKEND", "noop")
	auth, err := CreateFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorizer")
	}
}

func TestCreateFromEnvDefault(t *testing.T) {
	t.Setenv("AUTHZ_BACKEND", "")
	auth, err := CreateFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorizer for default")
	}
}

func TestCreateFromEnvUnsupported(t *testing.T) {
	t.Setenv("AUTHZ_BACKEND", "invalid")
	_, err := CreateFromEnv()
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestCreateFromEnvCedarMissingPath(t *testing.T) {
	t.Setenv("AUTHZ_BACKEND", "cedar")
	t.Setenv("AUTHZ_CEDAR_POLICY_PATH", "")
	_, err := CreateFromEnv()
	if err == nil {
		t.Fatal("expected error when AUTHZ_CEDAR_POLICY_PATH is not set")
	}
}

func TestCreateFromEnvOPAMissingEndpoint(t *testing.T) {
	t.Setenv("AUTHZ_BACKEND", "opa")
	t.Setenv("AUTHZ_OPA_ENDPOINT", "")
	_, err := CreateFromEnv()
	if err == nil {
		t.Fatal("expected error when AUTHZ_OPA_ENDPOINT is not set")
	}
}
