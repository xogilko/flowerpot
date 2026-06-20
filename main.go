package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/dgraph-io/badger/v4"
	"github.com/gorilla/mux"
)

type Server struct {
	db     *badger.DB
	store  *IbGibStore
	tokens *TokenConfig
}

type DataValue struct {
	Content          string `json:"content"`
	ContentType      string `json:"content_type"`
	Data             []byte `json:"data,omitempty"`
	AccessSecretHash []byte `json:"access_secret_hash,omitempty"`
}

type postRequest struct {
	Content      string `json:"content"`
	ContentType  string `json:"content_type"`
	AccessSecret string `json:"access_secret,omitempty"`
}

func main() {
	tokens, created, err := loadOrCreateTokenConfig()
	if err != nil {
		log.Fatal("Failed to load token config:", err)
	}
	if created {
		log.Printf("Created %s", tokens.path)
		log.Printf("Usage password (save this): %s", tokens.UsagePassword)
	}

	opts := badger.DefaultOptions("./data")
	opts.Logger = nil

	db, err := badger.Open(opts)
	if err != nil {
		log.Fatal("Failed to open BadgerDB:", err)
	}
	defer db.Close()

	server := &Server{
		db:     db,
		store:  NewIbGibStore(db),
		tokens: tokens,
	}

	r := mux.NewRouter()
	r.HandleFunc(adminTokensPath, server.handleGenerateTokens).Methods(http.MethodPost)
	r.HandleFunc(versionsPath, server.handleListVersions).Methods(http.MethodGet)
	r.HandleFunc("/{path:.*}", server.handlePath)

	fmt.Println("Server starting on :8083")
	fmt.Println("API Usage:")
	fmt.Println("  POST", adminTokensPath, "- Generate usage tokens (header", usagePasswordHeader+")")
	fmt.Println("  GET", versionsPath, "- List version metadata (?route= or ?ib=)")
	fmt.Println("  GET /{path} - Retrieve latest (?addr=ib^gib for specific version)")
	fmt.Println("  POST /{path} - Store JSON; requires header", usageTokenHeader)
	fmt.Println("  PUT /{path} - Store raw body; requires header", usageTokenHeader)
	fmt.Println("  DELETE /{path} - Tombstone latest version")
	fmt.Println("  Responses include header", ibGibHeader)

	log.Fatal(http.ListenAndServe(":8083", r))
}

func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	path := vars["path"]

	if path == "" {
		http.Error(w, "Path is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r, path)
	case http.MethodPost:
		s.handlePost(w, r, path)
	case http.MethodPut:
		s.handlePut(w, r, path)
	case http.MethodDelete:
		s.handleDelete(w, r, path)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) resolvedFrame(w http.ResponseWriter, r *http.Request, path string) (*ResolvedFrame, bool) {
	if addr := r.URL.Query().Get("addr"); addr != "" {
		frame, err := s.store.ReadAddr(addr)
		if err != nil {
			s.writeFrameError(w, path, err)
			return nil, false
		}
		return frame, true
	}

	frame, err := s.store.ReadLatest(path)
	if err != nil {
		s.writeFrameError(w, path, err)
		return nil, false
	}
	return frame, true
}

func (s *Server) writeFrameError(w http.ResponseWriter, path string, err error) {
	switch {
	case errors.Is(err, errRouteNotFound), errors.Is(err, errFrameNotFound):
		http.Error(w, fmt.Sprintf("Path '%s' not found", path), http.StatusNotFound)
	case errors.Is(err, errRouteGone):
		http.Error(w, fmt.Sprintf("Path '%s' gone", path), http.StatusGone)
	case errors.Is(err, errInvalidAddr):
		http.Error(w, "Invalid addr", http.StatusBadRequest)
	default:
		http.Error(w, "Database error", http.StatusInternalServerError)
	}
}

func frameToDataValue(frame *ResolvedFrame) *DataValue {
	if frame == nil {
		return nil
	}
	return &DataValue{
		Content:          frame.Content,
		ContentType:      frame.ContentType,
		Data:             frame.Data,
		AccessSecretHash: frame.AccessSecretHash,
	}
}

func (s *Server) requireOverwriteAccess(w http.ResponseWriter, gateSecret string, path string) (*ResolvedFrame, bool) {
	frame, err := s.store.ReadLatest(path)
	if err != nil {
		if errors.Is(err, errRouteNotFound) || errors.Is(err, errRouteGone) {
			return nil, true
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return nil, false
	}
	if !s.requireAccessWithSecret(w, gateSecret, frameToDataValue(frame)) {
		return nil, false
	}
	return frame, true
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, path string) {
	frame, ok := s.resolvedFrame(w, r, path)
	if !ok {
		return
	}

	if !s.requireAccess(w, r, frameToDataValue(frame)) {
		return
	}

	setIbGibHeader(w, frame.Addr)
	w.Header().Set("Content-Type", frame.ContentType)

	if len(frame.Data) > 0 {
		w.Write(frame.Data)
		return
	}
	w.Write([]byte(frame.Content))
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request, path string) {
	var req postRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.ContentType == "" {
		http.Error(w, "content_type is required", http.StatusBadRequest)
		return
	}

	gateSecret := gateSecretFromRequest(r, req.AccessSecret)
	_, ok := s.requireOverwriteAccess(w, gateSecret, path)
	if !ok {
		return
	}

	dataValue := DataValue{
		Content:     req.Content,
		ContentType: req.ContentType,
	}

	if err := applyAccessSecret(&dataValue, req.AccessSecret); err != nil {
		http.Error(w, "Failed to process access_secret", http.StatusInternalServerError)
		return
	}

	result, ok := s.requireUsageToken(w, r, func() (*WriteResult, error) {
		return s.store.WritePost(path, dataValue)
	})
	if !ok {
		return
	}

	setIbGibHeader(w, result.Addr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "success",
		"message":   fmt.Sprintf("Data stored at path: %s", path),
		"route":     result.Route,
		"ib":        result.Ib,
		"addr":      result.Addr,
		"gib":       result.Gib,
		"protected": len(dataValue.AccessSecretHash) > 0,
	})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, path string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	gateSecret := accessSecretFromRequest(r)
	previous, ok := s.requireOverwriteAccess(w, gateSecret, path)
	if !ok {
		return
	}

	protectedOverwrite := previous != nil && len(previous.AccessSecretHash) > 0

	dataValue := DataValue{
		ContentType: contentType,
		Data:        body,
	}

	if err := applyAccessSecret(&dataValue, putFrameSecret(r, protectedOverwrite)); err != nil {
		http.Error(w, "Failed to process access_secret", http.StatusInternalServerError)
		return
	}

	result, ok := s.requireUsageToken(w, r, func() (*WriteResult, error) {
		return s.store.WritePut(path, dataValue)
	})
	if !ok {
		return
	}

	setIbGibHeader(w, result.Addr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "success",
		"message":   fmt.Sprintf("Data stored at path: %s", path),
		"route":     result.Route,
		"ib":        result.Ib,
		"addr":      result.Addr,
		"gib":       result.Gib,
		"size":      fmt.Sprintf("%d bytes", len(body)),
		"protected": len(dataValue.AccessSecretHash) > 0,
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, path string) {
	frame, err := s.store.ReadLatestAny(path)
	if err != nil {
		s.writeFrameError(w, path, err)
		return
	}
	if frame.Tombstone {
		http.Error(w, fmt.Sprintf("Path '%s' gone", path), http.StatusGone)
		return
	}

	if !s.requireAccess(w, r, frameToDataValue(frame)) {
		return
	}

	result, err := s.store.WriteTombstone(path)
	if err != nil {
		http.Error(w, "Failed to delete data", http.StatusInternalServerError)
		return
	}

	setIbGibHeader(w, result.Addr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": fmt.Sprintf("Data deleted at path: %s", path),
		"route":   result.Route,
		"ib":      result.Ib,
		"addr":    result.Addr,
		"gib":     result.Gib,
	})
}
