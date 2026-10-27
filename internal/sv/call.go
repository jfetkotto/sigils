package sv

import (
	svtoken "github.com/jfetkotto/svparse/v2/token"
)

// CallContext describes a function/task call whose argument list the
// cursor sits inside -- "name(a, b<cursor>" -- for signature help.
type CallContext struct {
	// Name is the callee identifier, and NameLine/NameCharacter its start
	// (zero-based), for scope resolution the same way hover does it.
	Name          string
	NameLine      int
	NameCharacter int

	// ActiveIndex is the zero-based position of the argument the cursor is
	// in, counting top-level commas only. ActiveName is set instead when
	// that argument is a named one (".x(...)"), since a named argument's
	// position says nothing about which formal it binds.
	ActiveIndex int
	ActiveName  string
}

// CallContextAt inspects text up to (line, character) and reports whether
// the cursor sits inside the parenthesized argument list of a call to a
// plain identifier, returning the callee and which argument is active.
//
// Like InstantiationContextAt this is a lexical heuristic over the raw
// token stream, not a parse, since it has to work on a call still being
// typed. It finds the innermost still-open "(" and requires an identifier
// immediately before it. It doesn't decide whether that identifier is
// really a function or task: the caller resolves it against the index, and
// a miss (an instantiation, a declaration, a method call on an unresolved
// receiver) is a safe failure mode that just yields no signature.
func CallContextAt(text string, line, character int) (CallContext, bool) {
	return CallContextIn(Lex(text), line, character)
}

// CallContextIn is CallContextAt against an already-lexed document -- see
// Tokens.
func CallContextIn(toks Tokens, line, character int) (CallContext, bool) {
	prefix := toks[:tokensBefore(toks, line, character)]

	openIdx, ok := innermostOpenParen(prefix)
	// A cursor inside a named argument's own parens ("foo(.a(<here>") is
	// still in foo's argument list, so step out to the enclosing "(".
	for ok && openIdx >= 3 &&
		prefix[openIdx-1].Kind == svtoken.KindIdent && prefix[openIdx-2].Kind == svtoken.KindDot &&
		(prefix[openIdx-3].Kind == svtoken.KindLParen || prefix[openIdx-3].Kind == svtoken.KindComma) {
		openIdx, ok = innermostOpenParen(prefix[:openIdx])
	}
	if !ok || openIdx == 0 {
		return CallContext{}, false
	}
	nameTok := prefix[openIdx-1]
	if nameTok.Kind != svtoken.KindIdent {
		return CallContext{}, false
	}
	// "obj.method(" can't be resolved by name alone (see HoverInfo's
	// caveat about field-access references), and offering the signature of
	// whatever unrelated function shares the name would be worse than none.
	if openIdx >= 2 && prefix[openIdx-2].Kind == svtoken.KindDot {
		return CallContext{}, false
	}

	ctx := CallContext{Name: nameTok.Text, NameLine: nameTok.Line, NameCharacter: nameTok.Character}

	// Walk forward from the "(" to the cursor, counting commas that sit at
	// this call's own nesting level (not inside a nested call, a
	// concatenation or an index) and remembering where the current
	// argument starts.
	depth := 0
	segStart := openIdx + 1
	for i := openIdx + 1; i < len(prefix); i++ {
		switch prefix[i].Kind {
		case svtoken.KindLParen, svtoken.KindLBrace, svtoken.KindLBrack:
			depth++
		case svtoken.KindRParen, svtoken.KindRBrace, svtoken.KindRBrack:
			depth--
		case svtoken.KindComma:
			if depth == 0 {
				ctx.ActiveIndex++
				segStart = i + 1
			}
		}
	}
	if segStart+1 < len(prefix) && prefix[segStart].Kind == svtoken.KindDot && prefix[segStart+1].Kind == svtoken.KindIdent {
		ctx.ActiveName = prefix[segStart+1].Text
	}
	return ctx, true
}
