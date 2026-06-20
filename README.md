# Flowerpot

A lightweight HTTP file storage and retrieval server built with Go and BadgerDB, with ibgib-style append-only versioning.

## Overview

Flowerpot stores uploads as versioned **ibgib frames** in BadgerDB. Each route maps to a stable **ib** identity (`docs/readme` → `flowerpot:docs/readme`). POST and PUT append new versions; DELETE writes a tombstone frame. GET serves the latest non-tombstone version, or a specific version via `?addr=ib^gib`.

## Features

- **ibgib versioning**: Immutable frames with `ib^gib` addresses; latest pointer per route
- **Hierarchical routes**: Paths like `/docs/readme`, `/config/settings.json`
- **Content-type preservation**: MIME types preserved per version
- **Binary support**: POST for JSON text; PUT for raw bytes
- **Single-use upload tokens**: POST and PUT require a one-time usage token
- **Per-version access secrets**: Optional bcrypt-gated GET/DELETE per frame
- **Version listing**: `GET /_flowerpot/versions` with public or secret-filtered metadata

## First launch

On first run, Flowerpot creates `flowerpot.json` next to the executable with a randomly generated **usage password**. Save it — required to mint upload tokens and to list all versions (admin view).

Example `flowerpot.json`:

```json
{
  "usage_password": "abc123...",
  "tokens": {
    "f8a2...": "",
    "c91b...": "flowerpot:uploads/photo.png^deadbeef..."
  }
}
```

- Empty token values are unused.
- After upload, the token value is the created **`ib^gib` addr** for that version.

## API Usage

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/_flowerpot/tokens` | Mint usage tokens (usage password header) |
| `GET` | `/_flowerpot/versions?route=` | List version metadata for a route |
| `GET` | `/{path}` | Latest version (or `?addr=ib^gib`) |
| `POST` | `/{path}` | New JSON version (usage token) |
| `PUT` | `/{path}` | New binary version (usage token) |
| `DELETE` | `/{path}` | Tombstone latest version |

Successful reads and writes include header **`X-Flowerpot-ib^gib`**.

### Usage tokens

| Step | How |
|------|-----|
| **Mint** | `POST /_flowerpot/tokens` with `X-Flowerpot-Usage-Password` |
| **Upload** | `POST` or `PUT` with `X-Flowerpot-Usage-Token` |

### Version listing (`GET /_flowerpot/versions`)

| Caller | Visibility |
|--------|------------|
| No auth | Public versions only (no `access_secret` on frame) |
| `X-Flowerpot-Access-Secret` | Also versions whose secret matches |
| `X-Flowerpot-Usage-Password` | All version metadata (admin) |

Secret-gated versions are omitted from listings without a matching secret (no existence leak).

Query: `?route=docs/readme` or `?ib=flowerpot:docs/readme`

### Access secrets (per version)

| When storing | How |
|--------------|-----|
| **POST** | JSON field `access_secret` |
| **PUT** | Header `X-Flowerpot-Access-Secret` |

| When reading/deleting | How |
|-----------------------|-----|
| **GET**, **DELETE** | Same header or `?access_secret=` |

Each version stores its own bcrypt hash. Wrong or missing secret → **401**.

### DELETE behavior

DELETE appends a **tombstone** frame. GET latest on a tombstoned route → **410 Gone**. A new upload after tombstone starts a **fresh chain** (firstGen, no `past` link).

## POST body

```json
{
  "content": "{ \"theme\": \"dark\" }",
  "content_type": "application/json",
  "access_secret": "optional-read-password"
}
```

## Upload response

```json
{
  "status": "success",
  "route": "docs/readme",
  "ib": "flowerpot:docs/readme",
  "addr": "flowerpot:docs/readme^abc123...",
  "gib": "abc123...",
  "protected": false
}
```

## Installation

```bash
go mod download
go build -o flowerpot .
```

## Usage

```bash
./flowerpot
```

Server starts on port **8083**. Data persists in `./data` (BadgerDB).

## Storage layout (Badger keys)

| Key prefix | Purpose |
|------------|---------|
| `latest:{ib}` | Current `ib^gib` addr for a route |
| `frame:{addr}` | Immutable ibgib frame JSON |
| `bin:{hash}` | Binary payload for PUT versions |
| `versions:{ib}` | Version metadata index (newest first) |
