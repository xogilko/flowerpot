package main

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

// Header and query used to set (PUT) or present (GET/DELETE) the per-path access secret.
const accessSecretHeader = "X-Flowerpot-Access-Secret"

func accessSecretFromRequest(r *http.Request) string {
	if s := r.Header.Get(accessSecretHeader); s != "" {
		return s
	}
	return r.URL.Query().Get("access_secret")
}

func hashAccessSecret(secret string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
}

func accessSecretMatches(hash []byte, secret string) bool {
	if len(hash) == 0 {
		return true
	}
	if secret == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(secret)) == nil
}

func (s *Server) requireAccess(w http.ResponseWriter, r *http.Request, value *DataValue) bool {
	if len(value.AccessSecretHash) == 0 {
		return true
	}
	if !accessSecretMatches(value.AccessSecretHash, accessSecretFromRequest(r)) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"access_secret required (header X-Flowerpot-Access-Secret or query access_secret)"}`))
		return false
	}
	return true
}

func applyAccessSecret(value *DataValue, secret string, previous *DataValue) error {
	if secret != "" {
		hash, err := hashAccessSecret(secret)
		if err != nil {
			return err
		}
		value.AccessSecretHash = hash
		return nil
	}
	if previous != nil {
		value.AccessSecretHash = previous.AccessSecretHash
	}
	return nil
}
