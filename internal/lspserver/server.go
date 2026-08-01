// Package lspserver implements sigils' LSP request handlers on top of
// github.com/tliron/glsp, which owns the JSON-RPC transport, message
// framing, and protocol-level lifecycle gating (rejecting requests before
// initialize, after shutdown, etc.).
package lspserver

import (
	"context"
	"sync"

	"github.com/tliron/commonlog"
	"github.com/tliron/glsp"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/sv"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// Server holds sigils' own state behind the glsp protocol handler callbacks.
// Beyond what glsp's protocol.Handler already manages (initialize/shutdown
// gating), it tracks: whether shutdown preceded exit (for the process exit
// code), the resolved workspace root/config/discoverer, the declaration
// index that backs goto-definition, and the cancel func for the background
// indexing/file-watching goroutine started at Initialize.
type Server struct {
	Log   commonlog.Logger
	docs  *document.Store
	index *sv.Index

	mu               sync.Mutex
	root             string
	cfg              workspace.Config
	discoverer       workspace.Discoverer
	shutdownReceived bool
	watchCancel      context.CancelFunc
	snippetSupport   bool
	// notify sends a server-initiated notification to the client (e.g.
	// textDocument/publishDiagnostics). Captured once from Initialize's own
	// *glsp.Context, whose Notify closes over the connection-lifetime
	// jsonrpc2.Conn (not a per-request context that dies when the handler
	// returns -- confirmed by reading glsp/sourcegraph-jsonrpc2 directly),
	// so it's safe to reuse from background goroutines (buildIndex's
	// worker pool, watch.go's cascade) well after Initialize itself
	// returns. nil until Initialize runs.
	notify glsp.NotifyFunc
}

func NewServer(log commonlog.Logger) *Server {
	return &Server{
		Log:   log,
		docs:  document.NewStore(),
		index: sv.NewIndex(),
	}
}

func (s *Server) Documents() *document.Store {
	return s.docs
}

func (s *Server) Index() *sv.Index {
	return s.index
}

func (s *Server) Root() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

func (s *Server) Config() workspace.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Server) Discoverer() workspace.Discoverer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoverer
}

func (s *Server) ShutdownReceived() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownReceived
}

func (s *Server) SnippetSupport() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snippetSupport
}

// setNotify configures the func used to send server-initiated
// notifications -- see the Server.notify field's doc comment.
func (s *Server) setNotify(notify glsp.NotifyFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = notify
}

// getNotify returns the current notify func, or nil if Initialize hasn't
// run yet.
func (s *Server) getNotify() glsp.NotifyFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}
