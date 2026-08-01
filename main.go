package main

import (
	"flag"
	"os"

	"github.com/tliron/commonlog"
	commonlogslog "github.com/tliron/commonlog/slog"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"

	"github.com/jfetkotto/sigils/internal/lspserver"
)

const serverName = "sigils"

func main() {
	logFile := flag.String("log-file", "", "write logs to this file instead of stderr")
	verbosity := flag.Int("verbosity", 1, "commonlog verbosity: -4 (none) through 2 (debug)")
	flag.Parse()

	commonlog.SetBackend(commonlogslog.NewBackend())
	if *logFile != "" {
		commonlog.Configure(*verbosity, logFile)
	} else {
		commonlog.Configure(*verbosity, nil)
	}
	log := commonlog.GetLogger(serverName)

	srv := lspserver.NewServer(log)
	// Every handler is wrapped so a panic becomes a logged error response
	// for that one request instead of killing the server -- neither glsp
	// nor its jsonrpc2 transport recovers on its own; see lspserver.Guard.
	handler := &protocol.Handler{
		Initialize:                    lspserver.Guard(log, srv.Initialize),
		Initialized:                   lspserver.GuardNotify(log, srv.Initialized),
		Shutdown:                      lspserver.GuardShutdown(log, srv.Shutdown),
		SetTrace:                      lspserver.GuardNotify(log, srv.SetTrace),
		TextDocumentDidOpen:           lspserver.GuardNotify(log, srv.TextDocumentDidOpen),
		TextDocumentDidChange:         lspserver.GuardNotify(log, srv.TextDocumentDidChange),
		TextDocumentDidClose:          lspserver.GuardNotify(log, srv.TextDocumentDidClose),
		TextDocumentDefinition:        lspserver.Guard(log, srv.TextDocumentDefinition),
		TextDocumentDeclaration:       lspserver.Guard(log, srv.TextDocumentDeclaration),
		TextDocumentCompletion:        lspserver.Guard(log, srv.TextDocumentCompletion),
		TextDocumentDocumentSymbol:    lspserver.Guard(log, srv.TextDocumentDocumentSymbol),
		TextDocumentFoldingRange:      lspserver.Guard(log, srv.TextDocumentFoldingRange),
		WorkspaceSymbol:               lspserver.Guard(log, srv.WorkspaceSymbol),
		TextDocumentHover:             lspserver.Guard(log, srv.TextDocumentHover),
		TextDocumentDocumentHighlight: lspserver.Guard(log, srv.TextDocumentDocumentHighlight),
		TextDocumentReferences:        lspserver.Guard(log, srv.TextDocumentReferences),
		TextDocumentRename:            lspserver.Guard(log, srv.TextDocumentRename),
	}

	glspServer := server.NewServer(handler, serverName, false)

	// stdout is exclusively the JSON-RPC channel from here on; all logging
	// goes through commonlog to stderr (or -log-file) instead.
	if err := glspServer.RunStdio(); err != nil {
		log.Errorf("server error: %s", err.Error())
		os.Exit(1)
	}

	// glsp doesn't manage the exit-notification process-exit-code
	// convention itself: the LSP spec says exit should be 0 if shutdown
	// preceded it, 1 otherwise.
	if srv.ShutdownReceived() {
		os.Exit(0)
	}
	os.Exit(1)
}
