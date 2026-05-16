package gitproxy

import (
	"testing"
	"time"
)

func TestTokenMinter_MintAndValidate(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-here!")
	minter := NewTokenMinter(secret)

	claims := TokenClaims{
		RunID:   "run-123",
		StageID: "stage-456",
		Resources: map[string]Access{
			"package-repo": {
				Type:  "git",
				Level: "read-write",
				URL:   "github.com/org/repo",
			},
		},
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}

	token, err := minter.Mint(claims)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	if token == "" {
		t.Error("expected non-empty token")
	}

	validated, err := minter.Validate(token)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}

	if validated.RunID != claims.RunID {
		t.Errorf("run_id mismatch: %s vs %s", validated.RunID, claims.RunID)
	}
	if validated.StageID != claims.StageID {
		t.Errorf("stage_id mismatch: %s vs %s", validated.StageID, claims.StageID)
	}
}

func TestTokenMinter_ExpiredToken(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-here!")
	minter := NewTokenMinter(secret)

	claims := TokenClaims{
		RunID:     "run-123",
		StageID:   "stage-456",
		Resources: map[string]Access{},
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(), // expired
	}

	token, err := minter.Mint(claims)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	_, err = minter.Validate(token)
	if err == nil {
		t.Error("expected error for expired token")
	}
	if err != nil && err.Error() != "token expired" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTokenMinter_InvalidSignature(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-here!")
	minter := NewTokenMinter(secret)

	claims := TokenClaims{
		RunID:     "run-123",
		StageID:   "stage-456",
		Resources: map[string]Access{},
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}

	token, err := minter.Mint(claims)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	// Tamper with token
	tampered := token + "x"

	_, err = minter.Validate(tampered)
	if err == nil {
		t.Error("expected error for tampered token")
	}
}

func TestTokenMinter_DifferentSecret(t *testing.T) {
	secret1 := []byte("secret-1-32-bytes-long-here!!!!")
	secret2 := []byte("secret-2-32-bytes-long-here!!!!")

	minter1 := NewTokenMinter(secret1)
	minter2 := NewTokenMinter(secret2)

	claims := TokenClaims{
		RunID:     "run-123",
		StageID:   "stage-456",
		Resources: map[string]Access{},
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}

	token, err := minter1.Mint(claims)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	_, err = minter2.Validate(token)
	if err == nil {
		t.Error("expected error when validating with different secret")
	}
}
