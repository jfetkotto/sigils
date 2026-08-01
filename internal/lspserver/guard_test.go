package lspserver

import (
	"strings"
	"testing"

	"github.com/tliron/commonlog"
	"github.com/tliron/glsp"
)

func TestGuardConvertsPanicToError(t *testing.T) {
	f := Guard(commonlog.GetLogger("test"), func(ctx *glsp.Context, p int) (string, error) {
		panic("boom")
	})
	result, err := f(nil, 1)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected an error mentioning the panic, got %v", err)
	}
	if result != "" {
		t.Fatalf("expected the zero-value result, got %q", result)
	}
}

func TestGuardPassesThroughNormalResults(t *testing.T) {
	f := Guard(commonlog.GetLogger("test"), func(ctx *glsp.Context, p int) (string, error) {
		return "ok", nil
	})
	result, err := f(nil, 1)
	if err != nil || result != "ok" {
		t.Fatalf("expected (ok, nil), got (%q, %v)", result, err)
	}
}

func TestGuardNotifyConvertsPanicToError(t *testing.T) {
	f := GuardNotify(commonlog.GetLogger("test"), func(ctx *glsp.Context, p int) error {
		panic("boom")
	})
	if err := f(nil, 1); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected an error mentioning the panic, got %v", err)
	}
}

func TestGuardShutdownConvertsPanicToError(t *testing.T) {
	f := GuardShutdown(commonlog.GetLogger("test"), func(ctx *glsp.Context) error {
		panic("boom")
	})
	if err := f(nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected an error mentioning the panic, got %v", err)
	}
}
