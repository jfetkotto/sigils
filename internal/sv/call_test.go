package sv

import "testing"

func TestCallContextAt(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		line, col int
		want      CallContext
		wantOK    bool
	}{
		{"just after open paren", "  foo(", 0, 6, CallContext{Name: "foo", NameCharacter: 2}, true},
		{"first arg", "  foo(a", 0, 7, CallContext{Name: "foo", NameCharacter: 2}, true},
		{"second arg", "  foo(a, b", 0, 10, CallContext{Name: "foo", NameCharacter: 2, ActiveIndex: 1}, true},
		{"comma in nested call ignored", "  foo(bar(x, y), ", 0, 18, CallContext{Name: "foo", NameCharacter: 2, ActiveIndex: 1}, true},
		{"comma in concatenation ignored", "  foo({a, b}, ", 0, 14, CallContext{Name: "foo", NameCharacter: 2, ActiveIndex: 1}, true},
		{"inside nested call", "  foo(a, bar(x, ", 0, 16, CallContext{Name: "bar", NameCharacter: 9, ActiveIndex: 1}, true},
		{"named argument", "  foo(.b(1), .a(", 0, 16, CallContext{Name: "foo", NameCharacter: 2, ActiveIndex: 1, ActiveName: "a"}, true},
		{"spans lines", "  foo(a,\n    b", 1, 5, CallContext{Name: "foo", NameCharacter: 2, ActiveIndex: 1}, true},
		{"after closing paren", "  foo(a) ", 0, 9, CallContext{}, false},
		{"not a call", "  x = (a", 0, 8, CallContext{}, false},
		{"method call on receiver", "  obj.run(", 0, 10, CallContext{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CallContextAt(tc.text, tc.line, tc.col)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if tc.line == 0 {
				tc.want.NameLine = 0
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
