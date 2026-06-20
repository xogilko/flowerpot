package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

const (
	ibPrefix       = "flowerpot"
	ibRouteSep     = ":"
	ibGibHeader    = "X-Flowerpot-ib^gib"
	versionsPath   = "/_flowerpot/versions"
	keyLatest      = "latest:"
	keyFrame       = "frame:"
	keyBin         = "bin:"
	keyVersions    = "versions:"
)

var (
	errRouteNotFound = errors.New("route not found")
	errRouteGone     = errors.New("route gone")
	errFrameNotFound = errors.New("frame not found")
	errInvalidAddr   = errors.New("invalid addr")
)

// FrameData is the ibgib data payload for a flowerpot version.
type FrameData struct {
	ContentType      string `json:"content_type"`
	Content          string `json:"content,omitempty"`
	DataRef          string `json:"data_ref,omitempty"`
	AccessSecretHash []byte `json:"access_secret_hash,omitempty"`
	Tombstone        bool   `json:"tombstone,omitempty"`
}

// IbGibFrame is an immutable version record stored in Badger.
type IbGibFrame struct {
	Ib     string              `json:"ib"`
	Gib    string              `json:"gib"`
	Data   FrameData           `json:"data"`
	Rel8ns map[string][]string `json:"rel8ns,omitempty"`
}

// VersionEntry is metadata stored in the versions index.
type VersionEntry struct {
	Addr        string    `json:"addr"`
	Gib         string    `json:"gib"`
	Tombstone   bool      `json:"tombstone"`
	ContentType string    `json:"content_type,omitempty"`
	Protected   bool      `json:"protected"`
	StoredAt    time.Time `json:"stored_at"`
}

// WriteResult is returned after a successful version write.
type WriteResult struct {
	Route string `json:"route"`
	Ib    string `json:"ib"`
	Addr  string `json:"addr"`
	Gib   string `json:"gib"`
}

// ResolvedFrame is a frame with optional binary payload loaded.
type ResolvedFrame struct {
	Route            string
	Ib               string
	Addr             string
	Gib              string
	ContentType      string
	Content          string
	Data             []byte
	AccessSecretHash []byte
	Tombstone        bool
}

// IbGibStore persists ibgib frames in Badger.
type IbGibStore struct {
	db *badger.DB
}

func NewIbGibStore(db *badger.DB) *IbGibStore {
	return &IbGibStore{db: db}
}

func routeToIb(route string) string {
	route = strings.Trim(route, "/")
	if route == "" {
		return ibPrefix
	}
	return ibPrefix + ibRouteSep + route
}

func ibToRoute(ib string) string {
	ib = strings.TrimSpace(ib)
	if ib == ibPrefix {
		return ""
	}
	if after, ok := strings.CutPrefix(ib, ibPrefix+ibRouteSep); ok {
		return after
	}
	// Legacy: "flowerpot docs readme" → docs/readme
	if after, ok := strings.CutPrefix(ib, ibPrefix+" "); ok {
		return strings.ReplaceAll(after, " ", "/")
	}
	return strings.ReplaceAll(ib, " ", "/")
}

func parseAddr(addr string) (ib, gib string, err error) {
	parts := strings.SplitN(addr, "^", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errInvalidAddr
	}
	return parts[0], parts[1], nil
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func computeGib(ib string, data FrameData, past []string) (string, error) {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	h.Write([]byte(ib))
	h.Write(dataBytes)
	if len(past) > 0 {
		rel8nsBytes, err := json.Marshal(map[string][]string{"past": past})
		if err != nil {
			return "", err
		}
		h.Write(rel8nsBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *IbGibStore) readLatestAddr(ib string) (string, error) {
	var addr string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyLatest + ib))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			addr = string(val)
			return nil
		})
	})
	return addr, err
}

func (s *IbGibStore) readFrame(addr string) (*IbGibFrame, error) {
	var frame IbGibFrame
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyFrame + addr))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &frame)
		})
	})
	if err != nil {
		return nil, err
	}
	return &frame, nil
}

func (s *IbGibStore) readBin(dataRef string) ([]byte, error) {
	var content []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyBin + dataRef))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			content = append(content[:0], val...)
			return nil
		})
	})
	return content, err
}

func (s *IbGibStore) resolveFrame(addr string) (*ResolvedFrame, error) {
	frame, err := s.readFrame(addr)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, errFrameNotFound
		}
		return nil, err
	}

	resolved := &ResolvedFrame{
		Route:            ibToRoute(frame.Ib),
		Ib:               frame.Ib,
		Addr:             addr,
		Gib:              frame.Gib,
		ContentType:      frame.Data.ContentType,
		Content:          frame.Data.Content,
		AccessSecretHash: frame.Data.AccessSecretHash,
		Tombstone:        frame.Data.Tombstone,
	}

	if frame.Data.DataRef != "" {
		content, err := s.readBin(frame.Data.DataRef)
		if err != nil {
			return nil, err
		}
		resolved.Data = content
	}

	return resolved, nil
}

func (s *IbGibStore) ReadLatest(route string) (*ResolvedFrame, error) {
	ib := routeToIb(route)
	addr, err := s.readLatestAddr(ib)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, errRouteNotFound
		}
		return nil, err
	}

	resolved, err := s.resolveFrame(addr)
	if err != nil {
		return nil, err
	}
	if resolved.Tombstone {
		return nil, errRouteGone
	}
	return resolved, nil
}

func (s *IbGibStore) ReadLatestAny(route string) (*ResolvedFrame, error) {
	ib := routeToIb(route)
	addr, err := s.readLatestAddr(ib)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, errRouteNotFound
		}
		return nil, err
	}
	return s.resolveFrame(addr)
}

func (s *IbGibStore) ReadAddr(addr string) (*ResolvedFrame, error) {
	if _, _, err := parseAddr(addr); err != nil {
		return nil, err
	}
	resolved, err := s.resolveFrame(addr)
	if err != nil {
		return nil, err
	}
	if resolved.Tombstone {
		return nil, errRouteGone
	}
	return resolved, nil
}

func (s *IbGibStore) writeVersion(route string, data FrameData, bin []byte) (*WriteResult, error) {
	ib := routeToIb(route)
	var result WriteResult

	err := s.db.Update(func(txn *badger.Txn) error {
		var past []string
		latestItem, err := txn.Get([]byte(keyLatest + ib))
		if err == nil {
			var prevAddr string
			if err := latestItem.Value(func(val []byte) error {
				prevAddr = string(val)
				return nil
			}); err != nil {
				return err
			}
			prevFrame, err := s.loadFrameTxn(txn, prevAddr)
			if err != nil {
				return err
			}
			if !prevFrame.Data.Tombstone {
				past = []string{prevAddr}
			}
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}

		if len(bin) > 0 {
			dataRef := hashContent(bin)
			data.DataRef = dataRef
			if err := txn.Set([]byte(keyBin+dataRef), bin); err != nil {
				return err
			}
		}

		gib, err := computeGib(ib, data, past)
		if err != nil {
			return err
		}

		addr := ib + "^" + gib
		frame := IbGibFrame{
			Ib:   ib,
			Gib:  gib,
			Data: data,
		}
		if len(past) > 0 {
			frame.Rel8ns = map[string][]string{"past": past}
		}

		frameBytes, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte(keyFrame+addr), frameBytes); err != nil {
			return err
		}
		if err := txn.Set([]byte(keyLatest+ib), []byte(addr)); err != nil {
			return err
		}

		entry := VersionEntry{
			Addr:        addr,
			Gib:         gib,
			Tombstone:   data.Tombstone,
			ContentType: data.ContentType,
			Protected:   len(data.AccessSecretHash) > 0,
			StoredAt:    time.Now().UTC(),
		}
		if err := s.prependVersionEntryTxn(txn, ib, entry); err != nil {
			return err
		}

		result = WriteResult{Route: route, Ib: ib, Addr: addr, Gib: gib}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *IbGibStore) loadFrameTxn(txn *badger.Txn, addr string) (*IbGibFrame, error) {
	item, err := txn.Get([]byte(keyFrame + addr))
	if err != nil {
		return nil, err
	}
	var frame IbGibFrame
	err = item.Value(func(val []byte) error {
		return json.Unmarshal(val, &frame)
	})
	if err != nil {
		return nil, err
	}
	return &frame, nil
}

func (s *IbGibStore) prependVersionEntryTxn(txn *badger.Txn, ib string, entry VersionEntry) error {
	var entries []VersionEntry
	key := []byte(keyVersions + ib)
	item, err := txn.Get(key)
	if err == nil {
		_ = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &entries)
		})
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}

	entries = append([]VersionEntry{entry}, entries...)
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return txn.Set(key, data)
}

func (s *IbGibStore) WritePost(route string, value DataValue) (*WriteResult, error) {
	data := FrameData{
		ContentType:      value.ContentType,
		Content:          value.Content,
		AccessSecretHash: value.AccessSecretHash,
	}
	return s.writeVersion(route, data, nil)
}

func (s *IbGibStore) WritePut(route string, value DataValue) (*WriteResult, error) {
	data := FrameData{
		ContentType:      value.ContentType,
		AccessSecretHash: value.AccessSecretHash,
	}
	return s.writeVersion(route, data, value.Data)
}

func (s *IbGibStore) WriteTombstone(route string) (*WriteResult, error) {
	data := FrameData{
		ContentType: "application/octet-stream",
		Tombstone:   true,
	}
	return s.writeVersion(route, data, nil)
}

func (s *IbGibStore) ListVersions(route, ib, accessSecret, usagePassword string, tokens *TokenConfig) (map[string]interface{}, error) {
	if ib == "" {
		ib = routeToIb(route)
	}
	if route == "" {
		route = ibToRoute(ib)
	}

	admin := tokens != nil && usagePassword != "" && tokens.verifyUsagePassword(usagePassword)

	var entries []VersionEntry
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyVersions + ib))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &entries)
		})
	})
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, errRouteNotFound
		}
		return nil, err
	}

	visible := make([]VersionEntry, 0, len(entries))
	for _, entry := range entries {
		if admin || !entry.Protected {
			visible = append(visible, entry)
			continue
		}
		frame, err := s.readFrame(entry.Addr)
		if err != nil {
			continue
		}
		if accessSecretMatches(frame.Data.AccessSecretHash, accessSecret) {
			visible = append(visible, entry)
		}
	}

	latestAddr, _ := s.readLatestAddr(ib)
	latestTombstone := false
	if latestAddr != "" {
		if frame, err := s.readFrame(latestAddr); err == nil {
			latestTombstone = frame.Data.Tombstone
		}
	}

	return map[string]interface{}{
		"ib":               ib,
		"route":            route,
		"latest":           latestAddr,
		"latest_tombstone": latestTombstone,
		"versions":         visible,
	}, nil
}

func setIbGibHeader(w http.ResponseWriter, addr string) {
	if addr != "" {
		w.Header().Set(ibGibHeader, addr)
	}
}
