// Package document tracks the text of files the client has open, as
// reported via textDocument/didOpen, didChange, and didClose.
package document

import "sync"

// URI is an LSP document URI, e.g. "file:///path/to/file.sv".
type URI string

// Document is one open document as the client last reported it.
type Document struct {
	URI        URI
	LanguageID string
	Version    int32
	Text       string
}

// Store is a concurrency-safe, in-memory table of open documents keyed by
// URI. It only supports full-document sync: ApplyFullChange replaces the
// entire text rather than applying incremental edits.
type Store struct {
	mu   sync.RWMutex
	docs map[URI]*Document
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{docs: make(map[URI]*Document)}
}

// Open records uri as open with the given text, replacing any document
// already stored under it.
func (s *Store) Open(uri URI, languageID string, version int32, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &Document{URI: uri, LanguageID: languageID, Version: version, Text: text}
}

// Close forgets uri. Closing a document that isn't open is a no-op.
func (s *Store) Close(uri URI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}

// ApplyFullChange replaces the document's text and version. It reports
// whether the document was known; callers should log a warning rather than
// fail hard on a miss, since it indicates a client/server desync rather than
// a condition we can usefully recover from here.
func (s *Store) ApplyFullChange(uri URI, version int32, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[uri]
	if !ok {
		return false
	}
	doc.Version = version
	doc.Text = text
	return true
}

// Get returns a copy of the document so callers can't mutate stored state.
func (s *Store) Get(uri URI) (Document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.docs[uri]
	if !ok {
		return Document{}, false
	}
	return *doc, true
}

// Len reports how many documents are open.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}
