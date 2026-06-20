package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const (
	configFileName     = "flowerpot.json"
	usageTokenHeader   = "X-Flowerpot-Usage-Token"
	usagePasswordHeader = "X-Flowerpot-Usage-Password"
	adminTokensPath    = "/_flowerpot/tokens"
)

var (
	errTokenNotFound = errors.New("usage token not found")
	errTokenUsed     = errors.New("usage token already consumed")
)

// TokenConfig is persisted as flowerpot.json next to the executable.
// Token keys with an empty value are available; after POST/PUT the value is the created ib^gib addr.
type TokenConfig struct {
	UsagePassword string            `json:"usage_password"`
	Tokens        map[string]string `json:"tokens"`

	path string
	mu   sync.Mutex
}

func configPath() (string, error) {
	exec, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exec), configFileName), nil
}

func loadOrCreateTokenConfig() (*TokenConfig, bool, error) {
	path, err := configPath()
	if err != nil {
		return nil, false, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		password, err := randomHex(32)
		if err != nil {
			return nil, false, err
		}
		cfg := &TokenConfig{
			UsagePassword: password,
			Tokens:        make(map[string]string),
			path:          path,
		}
		if err := cfg.save(); err != nil {
			return nil, false, err
		}
		return cfg, true, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}

	cfg := &TokenConfig{path: path}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, false, err
	}
	if cfg.Tokens == nil {
		cfg.Tokens = make(map[string]string)
	}
	return cfg, false, nil
}

func (tc *TokenConfig) save() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.saveLocked()
}

func (tc *TokenConfig) saveLocked() error {
	data, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tc.path, data, 0600)
}

func (tc *TokenConfig) verifyUsagePassword(password string) bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return password != "" && password == tc.UsagePassword
}

func (tc *TokenConfig) generate(count int) ([]string, error) {
	if count < 1 {
		count = 1
	}
	if count > 100 {
		count = 100
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()

	created := make([]string, 0, count)
	for i := 0; i < count; i++ {
		token, err := randomHex(24)
		if err != nil {
			return nil, err
		}
		for {
			if _, exists := tc.Tokens[token]; exists || contains(created, token) {
				token, err = randomHex(24)
				if err != nil {
					return nil, err
				}
				continue
			}
			break
		}
		tc.Tokens[token] = ""
		created = append(created, token)
	}

	if err := tc.saveLocked(); err != nil {
		return nil, err
	}
	return created, nil
}

func (tc *TokenConfig) useForUpload(token string, upload func() (*WriteResult, error)) (*WriteResult, error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	addrUsed, ok := tc.Tokens[token]
	if !ok {
		return nil, errTokenNotFound
	}
	if addrUsed != "" {
		return nil, errTokenUsed
	}

	result, err := upload()
	if err != nil {
		return nil, err
	}

	tc.Tokens[token] = result.Addr
	if err := tc.saveLocked(); err != nil {
		return nil, err
	}
	return result, nil
}

func usageTokenFromRequest(r *http.Request) string {
	return r.Header.Get(usageTokenHeader)
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (s *Server) handleGenerateTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	password := r.Header.Get(usagePasswordHeader)
	if !s.tokens.verifyUsagePassword(password) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid usage password (header X-Flowerpot-Usage-Password)"}`))
		return
	}

	count := 1
	var req struct {
		Count int `json:"count"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Count > 0 {
		count = req.Count
	}

	created, err := s.tokens.generate(count)
	if err != nil {
		http.Error(w, "Failed to generate tokens", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"tokens": created,
	})
}

func (s *Server) requireUsageToken(w http.ResponseWriter, r *http.Request, upload func() (*WriteResult, error)) (*WriteResult, bool) {
	token := usageTokenFromRequest(r)
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"usage token required (header %s)"}`, usageTokenHeader)))
		return nil, false
	}

	result, err := s.tokens.useForUpload(token, upload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case errors.Is(err, errTokenNotFound):
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid usage token"}`))
		case errors.Is(err, errTokenUsed):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"usage token already consumed"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upload failed"}`))
		}
		return nil, false
	}

	return result, true
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Query().Get("route")
	ib := r.URL.Query().Get("ib")
	if route == "" && ib == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"route or ib query parameter required"}`))
		return
	}

	accessSecret := accessSecretFromRequest(r)
	usagePassword := r.Header.Get(usagePasswordHeader)

	data, err := s.store.ListVersions(route, ib, accessSecret, usagePassword, s.tokens)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case errors.Is(err, errRouteNotFound):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"route not found"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to list versions"}`))
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"data":   data,
	})
}
