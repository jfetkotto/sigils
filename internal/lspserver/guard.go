package lspserver

import (
	"fmt"
	"runtime/debug"

	"github.com/tliron/commonlog"
	"github.com/tliron/glsp"
)

// Neither glsp nor its jsonrpc2 transport recovers from a panicking
// handler (verified against the glsp v0.2.2 and sourcegraph/jsonrpc2
// v0.2.0 sources), so an unguarded panic -- a scanner edge case on
// unusual input, say -- kills the server for the whole editor session.
// These wrappers convert a panic into a logged error response for that
// one request instead; main.go applies them to every handler it wires
// into the protocol.Handler.

// Guard wraps a request handler, turning a panic into an error return.
func Guard[P, R any](log commonlog.Logger, f func(*glsp.Context, P) (R, error)) func(*glsp.Context, P) (R, error) {
	return func(ctx *glsp.Context, params P) (result R, err error) {
		defer func() {
			if v := recover(); v != nil {
				err = logPanic(log, v)
			}
		}()
		return f(ctx, params)
	}
}

// GuardNotify is Guard for notification handlers, which return only an
// error.
func GuardNotify[P any](log commonlog.Logger, f func(*glsp.Context, P) error) func(*glsp.Context, P) error {
	return func(ctx *glsp.Context, params P) (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = logPanic(log, v)
			}
		}()
		return f(ctx, params)
	}
}

// GuardShutdown is Guard for the parameterless Shutdown handler.
func GuardShutdown(log commonlog.Logger, f func(*glsp.Context) error) func(*glsp.Context) error {
	return func(ctx *glsp.Context) (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = logPanic(log, v)
			}
		}()
		return f(ctx)
	}
}

func logPanic(log commonlog.Logger, v any) error {
	log.Errorf("handler panic: %v\n%s", v, debug.Stack())
	return fmt.Errorf("internal error: %v", v)
}
