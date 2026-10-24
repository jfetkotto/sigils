package semantic

import (
	"github.com/jfetkotto/svparse/v2/sema"
)

// Token types and modifiers, in the order of the legend the server declares.
// The names are LSP's standard ones, so a client's theme colours them without
// configuration.
const (
	TokNamespace = iota
	TokType
	TokClass
	TokInterface
	TokTypeParameter
	TokParameter
	TokVariable
	TokProperty
	TokEnumMember
	TokFunction
	TokMethod
)

// TokenTypes is the legend's token type list.
var TokenTypes = []string{
	"namespace", "type", "class", "interface", "typeParameter",
	"parameter", "variable", "property", "enumMember", "function", "method",
}

// Token modifier bits.
const (
	ModDeclaration = 1 << iota
	ModReadonly
)

// TokenModifiers is the legend's modifier list; bit i is TokenModifiers[i].
var TokenModifiers = []string{"declaration", "readonly"}

// SemToken is one coloured name: single-line, UTF-16 units.
type SemToken struct {
	Line, Character, Length int
	Type                    int
	Modifiers               uint32
}

// SemanticTokens classifies every name in uri that the semantic model
// resolves: a package is a namespace, a typedef a type, a port a property, a
// parameter a read-only variable, a function called inside a class a method,
// and so on. The result is in source order. It is what a grammar cannot tell
// apart: that `x` is a parameter and `y` a signal.
func (s *Service) SemanticTokens(uri string) (tokens []SemToken) {
	defer func() {
		if recover() != nil {
			tokens = nil
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	nav := s.navLocked()
	var last Location
	for _, r := range nav.byFile[uri] {
		l := r.loc
		if l.EndLine != l.Line || l.EndCharacter <= l.Character {
			continue
		}
		if l.Line == last.Line && l.Character < last.EndCharacter && last.URI != "" {
			continue // overlapping names cannot both be tokens
		}
		typ, mods, ok := classify(r.sym)
		if !ok {
			continue
		}
		if r.decl {
			mods |= ModDeclaration
		}
		last = l
		tokens = append(tokens, SemToken{Line: l.Line, Character: l.Character, Length: l.EndCharacter - l.Character, Type: typ, Modifiers: mods})
	}
	return tokens
}

// classify maps a resolved name to a token type and modifiers.
func classify(target any) (typ int, mods uint32, ok bool) {
	switch t := target.(type) {
	case *sema.Definition:
		switch t.Kind {
		case sema.DefPackage:
			return TokNamespace, 0, true
		case sema.DefInterface:
			return TokInterface, 0, true
		}
		return TokClass, 0, true
	case *sema.Symbol:
		switch t.Kind {
		case sema.SymTypedef, sema.SymNettype:
			return TokType, 0, true
		case sema.SymClass, sema.SymCovergroup:
			return TokClass, 0, true
		case sema.SymInterface:
			return TokInterface, 0, true
		case sema.SymModule, sema.SymProgram, sema.SymUDP:
			return TokClass, 0, true
		case sema.SymTypeParam:
			return TokTypeParameter, 0, true
		case sema.SymParameter:
			return TokVariable, ModReadonly, true
		case sema.SymEnumMember:
			return TokEnumMember, ModReadonly, true
		case sema.SymArg:
			return TokParameter, 0, true
		case sema.SymPort, sema.SymClockingSignal:
			return TokProperty, 0, true
		case sema.SymVariable, sema.SymNet, sema.SymGenvar, sema.SymLoopVar, sema.SymInstance:
			if t.Scope != nil && t.Scope.Kind == sema.ScopeClass {
				return TokProperty, 0, true
			}
			return TokVariable, 0, true
		case sema.SymFunction, sema.SymTask:
			if t.Scope != nil && t.Scope.Kind == sema.ScopeClass {
				return TokMethod, 0, true
			}
			return TokFunction, 0, true
		}
	}
	return 0, 0, false
}
