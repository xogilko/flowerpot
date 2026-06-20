package main

import (
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"
)

const (
	accessSecretHeader      = "X-Flowerpot-Access-Secret"
	frameAccessSecretHeader = "X-Flowerpot-Frame-Access-Secret"
	publicReadHeader        = "X-Flowerpot-Public-Read"
	accessSecretQueryParam  = "access_secret"
	publicReadQueryParam    = "public_read"
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

func publicReadFromRequest(r *http.Request) bool {
	if v := r.Header.Get(publicReadHeader); v != "" {
		b, err := strconv.ParseBool(v)
		return err == nil && b
	}
	if v := r.URL.Query().Get(publicReadQueryParam); v != "" {
		b, err := strconv.ParseBool(v)
		return err == nil && b
	}
	return false
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

func (v *DataValue) writeProtected() bool {
	return len(v.AccessSecretHash) > 0
}

func (v *DataValue) readProtected() bool {
	return v.writeProtected() && !v.PublicRead
}

func (s *Server) requireReadAccess(w http.ResponseWriter, secret string, value *DataValue) bool {
	if value == nil || !value.readProtected() {
		return true
	}
	if !accessSecretMatches(value.AccessSecretHash, secret) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"access_secret required to read (header X-Flowerpot-Access-Secret or query access_secret)"}`))
		return false
	}
	return true
}

func (s *Server) requireWriteAccess(w http.ResponseWriter, secret string, value *DataValue) bool {
	if value == nil || !value.writeProtected() {
		return true
	}
	if !accessSecretMatches(value.AccessSecretHash, secret) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"access_secret required to modify (header X-Flowerpot-Access-Secret or query access_secret)"}`))
		return false
	}
	return true
}

func (s *Server) requireReadAccessFromRequest(w http.ResponseWriter, r *http.Request, value *DataValue) bool {
	return s.requireReadAccess(w, accessSecretFromRequest(r), value)
}

func applyFrameAccess(value *DataValue, secret string, publicRead bool) error {
	if secret == "" {
		value.AccessSecretHash = nil
		value.PublicRead = false
		return nil
	}
	hash, err := hashAccessSecret(secret)
	if err != nil {
		return err
	}
	value.AccessSecretHash = hash
	value.PublicRead = publicRead
	return nil
}
