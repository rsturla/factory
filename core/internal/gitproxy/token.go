package gitproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenClaims encodes what a sandbox can access via git-proxy.
type TokenClaims struct {
	RunID      string            `json:"run_id"`
	StageID    string            `json:"stage_id"`
	Resources  map[string]Access `json:"resources"` // resource name -> access level
	ExpiresAt  int64             `json:"exp"`
}

// Access defines what operations are allowed on a resource.
type Access struct {
	Type  string `json:"type"`   // "git", "http", "s3"
	Level string `json:"level"`  // "read-only", "read-write"
	URL   string `json:"url"`    // resource URL
	Ref   string `json:"ref"`    // git ref (optional)
}

// TokenMinter creates and validates FACTORY_GIT_TOKEN values.
type TokenMinter struct {
	secret []byte
}

// NewTokenMinter creates a token minter with the given secret.
// Secret should be 32+ bytes from secure random source.
func NewTokenMinter(secret []byte) *TokenMinter {
	return &TokenMinter{secret: secret}
}

// Mint creates a signed token encoding the given claims.
// Token format: base64(json(claims)).base64(hmac-sha256(json(claims)))
func (m *TokenMinter) Mint(claims TokenClaims) (string, error) {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	mac := hmac.New(sha256.New, m.secret)
	mac.Write(claimsJSON)
	signature := mac.Sum(nil)

	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	return claimsB64 + "." + sigB64, nil
}

// Validate checks token signature and expiry, returns claims if valid.
func (m *TokenMinter) Validate(token string) (*TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid token format")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	expectedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	mac := hmac.New(sha256.New, m.secret)
	mac.Write(claimsJSON)
	actualSig := mac.Sum(nil)

	if !hmac.Equal(expectedSig, actualSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	var claims TokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}
