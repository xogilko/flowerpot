package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/dgraph-io/badger/v4"
	"github.com/gorilla/mux"
)

type Server struct {
	db     *badger.DB
	tokens *TokenConfig
}

type DataValue struct {
	Content          string `json:"content"`
	ContentType      string `json:"content_type"`
	Data             []byte `json:"data,omitempty"`
	AccessSecretHash []byte `json:"access_secret_hash,omitempty"`
}

// postRequest is the JSON body for POST (access_secret is not stored verbatim).
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

	server := &Server{db: db, tokens: tokens}
	server.initializeSampleData()

	r := mux.NewRouter()
	r.HandleFunc(adminTokensPath, server.handleGenerateTokens).Methods(http.MethodPost)
	r.HandleFunc("/{path:.*}", server.handlePath)

	fmt.Println("Server starting on :8083")
	fmt.Println("API Usage:")
	fmt.Println("  POST", adminTokensPath, "- Generate usage tokens (header", usagePasswordHeader+")")
	fmt.Println("  GET /{path} - Retrieve data")
	fmt.Println("  POST /{path} - Store JSON; requires header", usageTokenHeader)
	fmt.Println("  PUT /{path} - Store raw body; requires header", usageTokenHeader)
	fmt.Println("  DELETE /{path} - Delete data")
	fmt.Println("  Protected paths require the same access_secret on GET/DELETE")

	log.Fatal(http.ListenAndServe(":8083", r))
}

func (s *Server) initializeSampleData() {
	samples := map[string]DataValue{
		"alphabet/soup": {
			Content:     "Delicious alphabet soup recipe",
			ContentType: "text/plain",
		},
		"config/settings.json": {
			Content:     `{"theme": "dark", "language": "en"}`,
			ContentType: "application/json",
		},
		"docs/readme": {
			Content:     "# Welcome to the API\n\nThis is a sample readme file.",
			ContentType: "text/markdown",
		},
	}

	for key, value := range samples {
		if err := s.storeValue(key, value); err != nil {
			log.Printf("Failed to store sample data for %s: %v", key, err)
		}
	}
}

func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	path := vars["path"]

	if path == "" {
		http.Error(w, "Path is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		s.handleGet(w, r, path)
	case "POST":
		s.handlePost(w, r, path)
	case "PUT":
		s.handlePut(w, r, path)
	case "DELETE":
		s.handleDelete(w, r, path)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, path string) {
	value, err := s.getValue(path)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			http.Error(w, fmt.Sprintf("Path '%s' not found", path), http.StatusNotFound)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if !s.requireAccess(w, r, value) {
		return
	}

	w.Header().Set("Content-Type", value.ContentType)

	if len(value.Data) > 0 {
		w.Write(value.Data)
		return
	}

	w.Write([]byte(value.Content))
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

	dataValue := DataValue{
		Content:     req.Content,
		ContentType: req.ContentType,
	}

	if err := applyAccessSecret(&dataValue, req.AccessSecret, nil); err != nil {
		http.Error(w, "Failed to process access_secret", http.StatusInternalServerError)
		return
	}

	if !s.requireUsageToken(w, r, path, func() error {
		return s.storeValue(path, dataValue)
	}) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":    "success",
		"message":   fmt.Sprintf("Data stored at path: %s", path),
		"protected": len(dataValue.AccessSecretHash) > 0,
	}
	json.NewEncoder(w).Encode(response)
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

	dataValue := DataValue{
		Content:     "",
		ContentType: contentType,
		Data:        body,
	}

	previous, _ := s.getValue(path)
	secret := accessSecretFromRequest(r)
	if err := applyAccessSecret(&dataValue, secret, previous); err != nil {
		http.Error(w, "Failed to process access_secret", http.StatusInternalServerError)
		return
	}

	if !s.requireUsageToken(w, r, path, func() error {
		return s.storeValue(path, dataValue)
	}) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":    "success",
		"message":   fmt.Sprintf("Data stored at path: %s", path),
		"size":      fmt.Sprintf("%d bytes", len(body)),
		"protected": len(dataValue.AccessSecretHash) > 0,
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, path string) {
	value, err := s.getValue(path)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			http.Error(w, fmt.Sprintf("Path '%s' not found", path), http.StatusNotFound)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if !s.requireAccess(w, r, value) {
		return
	}

	if err := s.deleteValue(path); err != nil {
		http.Error(w, "Failed to delete data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Data deleted at path: %s", path),
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) storeValue(key string, value DataValue) error {
	return s.db.Update(func(txn *badger.Txn) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return txn.Set([]byte(key), data)
	})
}

func (s *Server) deleteValue(key string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

func (s *Server) getValue(key string) (*DataValue, error) {
	var value *DataValue
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}

		return item.Value(func(val []byte) error {
			var dataValue DataValue
			if err := json.Unmarshal(val, &dataValue); err != nil {
				return err
			}
			value = &dataValue
			return nil
		})
	})

	return value, err
}
