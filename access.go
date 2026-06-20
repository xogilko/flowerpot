package main

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

const (
	accessSecretHeader      = "X-Flowerpot-Access-Secret"
	frameAccessSecretHeader = "X-Flowerpot-Frame-Access-Secret"
	accessSecretQueryParam  = "access_secret"
)

func accessSecretFromRequest(r *http.Request) string {
	if s := r.Header.Get(accessSecretHeader); s != "" {
		return s
	}
	return r.URL.Query().Get(accessSecretQueryParam)
}

func gateSecretFromRequest(r *http.Request, bodySecret string) string {
	if s := accessSecretFromRequest(r); s != "" {
		return s
	}
	return bodySecret
}

func putFrameSecret(r *http.Request, protectedOverwrite bool) string {
	if s := r.Header.Get(frameAccessSecretHeader); s != "" {
		return s
	}
	if !protectedOverwrite {
		return r.Header.Get(accessSecretHeader)
	}
	return ""
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

func (s *Server) requireAccessWithSecret(w http.ResponseWriter, secret string, value *DataValue) bool {
	if len(value.AccessSecretHash) == 0 {
		return true
	}
	if !accessSecretMatches(value.AccessSecretHash, secret) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"access_secret required (header X-Flowerpot-Access-Secret or query access_secret)"}`))
		return false
	}
	return true
}

func (s *Server) requireAccess(w http.ResponseWriter, r *http.Request, value *DataValue) bool {
	return s.requireAccessWithSecret(w, accessSecretFromRequest(r), value)
}

func applyAccessSecret(value *DataValue, secret string) error {
	if secret == "" {
		return nil
	}
	hash, err := hashAccessSecret(secret)
	if err != nil {
		return err
	}
	value.AccessSecretHash = hash
	return nil
}
