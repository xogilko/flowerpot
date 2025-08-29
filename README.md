# Flowerpot

A lightweight HTTP file storage and retrieval server built with Go and BadgerDB.

## Overview

Flowerpot provides a RESTful API for storing and retrieving files at arbitrary hierarchical paths. It uses BadgerDB as a backend for high-performance key-value storage.

## Features

- **Hierarchical storage**: Store files at paths like `/docs/readme`, `/config/settings.json`
- **Content-type preservation**: Automatically handles MIME types for proper file serving
- **Binary support**: Stores and serves both text and binary files
- **CRUD operations**: Full HTTP method support (GET, POST, PUT, DELETE)
- **No file type restrictions**: Accepts any content type

## API Usage

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/{path}` | Retrieve data stored at path |
| `POST` | `/{path}` | Store JSON data with content and content_type |
| `PUT` | `/{path}` | Store raw data with Content-Type header |
| `DELETE` | `/{path}` | Delete data at path |

## Data Structure

```go
type DataValue struct {
    Content     string `json:"content"`      // Text content
    ContentType string `json:"content_type"` // MIME type
    Data        []byte `json:"data,omitempty"` // Binary data
}
```

## Installation

```bash
go mod download
go build -o flowerpot main.go
```

## Usage

```bash
./flowerpot
```

Server starts on port 8080.

## Storage

Data is persisted in the `./data` directory using BadgerDB. The database automatically handles compression and optimization.
