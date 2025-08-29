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
	db *badger.DB
}

type DataValue struct {
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data,omitempty"`
}

func main() {
	// Initialize BadgerDB
	opts := badger.DefaultOptions("./data")
	opts.Logger = nil // Disable logging for cleaner output

	db, err := badger.Open(opts)
	if err != nil {
		log.Fatal("Failed to open BadgerDB:", err)
	}
	defer db.Close()

	server := &Server{db: db}

	// Initialize with some sample data
	server.initializeSampleData()

	// Create router
	r := mux.NewRouter()

	// Handle all paths with wildcard
	r.HandleFunc("/{path:.*}", server.handlePath)

	// Start server
	fmt.Println("Server starting on :8080")
	fmt.Println("API Usage:")
	fmt.Println("  GET /{path} - Retrieve data")
	fmt.Println("  POST /{path} - Store data with JSON body")
	fmt.Println("  PUT /{path} - Store raw data with Content-Type header")
	fmt.Println("  DELETE /{path} - Delete data")

	log.Fatal(http.ListenAndServe(":8080", r))
}

func (s *Server) initializeSampleData() {
	// Store some sample data
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
		err := s.storeValue(key, value)
		if err != nil {
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

	// Set appropriate content type
	w.Header().Set("Content-Type", value.ContentType)

	// For binary data, write the raw data
	if len(value.Data) > 0 {
		w.Write(value.Data)
		return
	}

	// For text content, write the content
	w.Write([]byte(value.Content))
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request, path string) {
	var dataValue DataValue

	// Parse JSON from request body
	if err := json.NewDecoder(r.Body).Decode(&dataValue); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if dataValue.ContentType == "" {
		http.Error(w, "content_type is required", http.StatusBadRequest)
		return
	}

	// Store the value
	if err := s.storeValue(path, dataValue); err != nil {
		http.Error(w, "Failed to store data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Data stored at path: %s", path),
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, path string) {
	// Read the raw body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	// Get content type from header
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Store the raw data
	dataValue := DataValue{
		Content:     "",
		ContentType: contentType,
		Data:        body,
	}

	if err := s.storeValue(path, dataValue); err != nil {
		http.Error(w, "Failed to store data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Data stored at path: %s", path),
		"size":    fmt.Sprintf("%d bytes", len(body)),
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, path string) {
	// Check if the key exists first
	_, err := s.getValue(path)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			http.Error(w, fmt.Sprintf("Path '%s' not found", path), http.StatusNotFound)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Delete the key
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
