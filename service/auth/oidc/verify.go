package oidc

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// verifyRS256 checks an RS256 signature over signingInput.
func verifyRS256(signingInput, signature string, key *rsa.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}
