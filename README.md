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
- **Single-use upload tokens**: POST and PUT require a one-time usage token; tokens are minted with the usage password

## First launch

On first run, Flowerpot creates `flowerpot.json` next to the executable with a randomly generated **usage password**. The password is printed to the log once — save it. That password is required to mint new upload tokens.

Example `flowerpot.json`:

```json
{
  "usage_password": "abc123...",
  "tokens": {
    "f8a2...": "",
    "c91b...": "uploads/photo.png"
  }
}
```

- Token keys with an **empty string** value are unused and valid for one POST or PUT.
- After a successful upload, the token's value is set to the path that was written.

## API Usage

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/_flowerpot/tokens` | Mint usage tokens (requires usage password) |
| `GET` | `/{path}` | Retrieve data stored at path |
| `POST` | `/{path}` | Store JSON data (requires usage token) |
| `PUT` | `/{path}` | Store raw data (requires usage token) |
| `DELETE` | `/{path}` | Delete data at path |

### Usage tokens (upload gate)

| Step | How |
|------|-----|
| **Mint tokens** | `POST /_flowerpot/tokens` with header `X-Flowerpot-Usage-Password: <usage password>`. Optional JSON body `{ "count": 5 }` (default 1, max 100). |
| **Upload** | `POST` or `PUT` to `/{path}` with header `X-Flowerpot-Usage-Token: <token>`. Each token works once; it is then bound to that path in `flowerpot.json`. |

Wrong or missing usage token → **401**. Already-used token → **403**.

### Optional per-path access secret

When storing, you can require a secret to read (or delete) that path later.

| When storing | How to set secret |
|--------------|-------------------|
| **POST** | JSON field `access_secret` (optional) |
| **PUT** | Header `X-Flowerpot-Access-Secret: your-secret` (optional). Omit on PUT to keep an existing secret when overwriting body. |

| When reading/deleting | How to provide secret |
|-----------------------|------------------------|
| **GET**, **DELETE** | Same value via header `X-Flowerpot-Access-Secret` or query `?access_secret=` |

Secrets are stored as bcrypt hashes only (`access_secret_hash` in the database). Wrong or missing secret → **401 Unauthorized**. Paths without a secret behave as before (public GET).

## Data Structure

```go
type DataValue struct {
    Content          string `json:"content"`       // Text content
    ContentType      string `json:"content_type"`  // MIME type
    Data             []byte `json:"data,omitempty"` // Binary data (PUT)
    AccessSecretHash []byte `json:"access_secret_hash,omitempty"` // Set by server; never send on POST body
}
```

POST body:

```json
{
  "content": "{ \"theme\": \"dark\" }",
  "content_type": "application/json",
  "access_secret": "my-gate-password"
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

Server starts on port 8083.

## Storage

Data is persisted in the `./data` directory using BadgerDB. The database automatically handles compression and optimization.
