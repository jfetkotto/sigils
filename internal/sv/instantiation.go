package sv

import (
	"github.com/jfetkotto/svparse/lexer"
	svtoken "github.com/jfetkotto/svparse/token"
)

// InstantiationContextAt inspects text up to (line, character) and reports
// whether the cursor sits inside an instantiation's port-connection parens
// -- "ModuleName [#(overrides)] instanceName [dims] ( ... <cursor> ... )"
// -- for named-port completion. It walks the token stream up to the
// cursor tracking paren (and brace) balance to find the innermost still-
// open "(", then walks backward from there (see
// instantiationModuleTokenBefore) past an optional instance-array suffix
// and an optional "#( param overrides )" list to find the module type
// identifier.
//
// This is a lexical heuristic, not a parse: it doesn't verify that
// moduleName is actually a known module, or that the two-identifiers-then-
// "(" shape it found is really an instantiation and not some other
// construct that happens to look like one. A caller that fails to resolve
// moduleName in the index just gets no completions, which is a safe
// failure mode. Deliberately left as a lexical heuristic over svparse's
// raw lexer output rather than replaced by ast.Instantiation's structural
// data -- it already works, and completion needs to run on text the
// parser may not even accept yet (an instantiation still being typed).
//
// connected reports the port names already given an explicit ".name("
// connection earlier in the same (still-open) parens, so the caller can
// exclude them from suggestions.
func InstantiationContextAt(text string, line, character int) (moduleName string, connected map[string]bool, ok bool) {
	return InstantiationContextIn(Lex(text), line, character)
}

// Tokens is a whole document already lexed, so a caller that makes several
// instantiation-context probes against the same text pays for lexing once
// rather than once per probe. Every "...At(text, ...)" function in this
// file is a thin wrapper that lexes and delegates to its "...In(toks, ...)"
// counterpart -- convenient for a single probe or a test, but a request
// handler that probes for both a port connection and a parameter override
// (as goto-definition, hover, and find-references all do) should Lex once
// and use the "In" form. Lexing is a whole-file operation, so on a large
// file the difference is per-keystroke visible.
type Tokens []svtoken.Token

// Lex tokenizes text for the "...In" functions below. Lex errors are
// ignored deliberately: these are lexical heuristics run against text the
// user is still typing, and a partial token stream is exactly what they
// need to work on.
func Lex(text string) Tokens {
	toks, _ := lexer.Lex(text)
	return toks
}

// InstantiationContextIn is InstantiationContextAt against an
// already-lexed document -- see Tokens.
func InstantiationContextIn(toks Tokens, line, character int) (moduleName string, connected map[string]bool, ok bool) {
	return instantiationContextAt(toks, line, character, instantiationModuleTokenBefore)
}

// InstantiationParamContextAt is InstantiationContextAt's counterpart for
// a "#( ... )" parameter-override list instead of the port-connection
// "( ... )" -- "ModuleName #( .WIDTH(8), <cursor> )", not "ModuleName
// u0 ( <cursor> )". Same lexical-heuristic caveats and connected/ok
// semantics as InstantiationContextAt; see
// instantiationModuleTokenBeforeParamList for the one part that differs.
func InstantiationParamContextAt(text string, line, character int) (moduleName string, connected map[string]bool, ok bool) {
	return InstantiationParamContextIn(Lex(text), line, character)
}

// InstantiationParamContextIn is InstantiationParamContextAt against an
// already-lexed document -- see Tokens.
func InstantiationParamContextIn(toks Tokens, line, character int) (moduleName string, connected map[string]bool, ok bool) {
	return instantiationContextAt(toks, line, character, instantiationModuleTokenBeforeParamList)
}

// instantiationContextAt is InstantiationContextAt/InstantiationParamContextAt's
// shared implementation: find the innermost still-open "(" before
// (line, character), identify what construct it belongs to via
// findModule (the only thing that differs between a port-connection list
// and a parameter-override list), and collect every depth-0 ".name("
// seen since. findModule reports found=false for any other shape (the
// "(" belongs to some unrelated construct), a safe failure mode since
// the caller just gets no completions/no resolution.
func instantiationContextAt(
	toks Tokens, line, character int,
	findModule func(prefix []svtoken.Token, openIdx int) (svtoken.Token, bool),
) (moduleName string, connected map[string]bool, ok bool) {
	cutoff := 0
	for cutoff < len(toks) && before(toks[cutoff].Line, toks[cutoff].Character, line, character) {
		cutoff++
	}
	prefix := toks[:cutoff]

	var open []int
	for i, t := range prefix {
		switch t.Kind {
		case svtoken.KindLParen:
			open = append(open, i)
		case svtoken.KindRParen:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
	}
	if len(open) == 0 {
		return "", nil, false
	}
	openIdx := open[len(open)-1]

	moduleTok, found := findModule(prefix, openIdx)
	if !found {
		return "", nil, false
	}

	connected = map[string]bool{}
	depth := 0
	for i := openIdx + 1; i < len(prefix); i++ {
		switch prefix[i].Kind {
		case svtoken.KindLParen, svtoken.KindLBrace:
			depth++
		case svtoken.KindRParen, svtoken.KindRBrace:
			depth--
		case svtoken.KindDot:
			if depth == 0 && i+1 < len(prefix) && prefix[i+1].Kind == svtoken.KindIdent {
				connected[prefix[i+1].Text] = true
			}
		}
	}

	return moduleTok.Text, connected, true
}

// instantiationModuleTokenBefore walks backward from openIdx (the index
// of an instantiation's own port-connection "(") to find the module type
// token that introduces it. The instance name immediately precedes "(",
// optionally through a balanced "[...]" instance-array suffix
// ("u_leaf[3] ("); the module type immediately precedes the instance
// name, optionally through a balanced "#( ... )" parameter override list
// ("my_mod #(.W(4)) u0 ("). Reports found=false if that exact shape isn't
// present -- e.g. the "(" belongs to some other construct entirely, a
// safe failure mode since the caller just gets no completions.
func instantiationModuleTokenBefore(prefix []svtoken.Token, openIdx int) (moduleTok svtoken.Token, found bool) {
	i := openIdx - 1
	if i < 0 {
		return svtoken.Token{}, false
	}
	if prefix[i].Kind == svtoken.KindRBrack {
		open, ok := skipBalancedBack(prefix, i, svtoken.KindLBrack, svtoken.KindRBrack)
		if !ok {
			return svtoken.Token{}, false
		}
		i = open - 1
	}
	if i < 0 || prefix[i].Kind != svtoken.KindIdent {
		return svtoken.Token{}, false // the instance name itself
	}
	i-- // step past the instance name

	if i >= 0 && prefix[i].Kind == svtoken.KindRParen {
		open, ok := skipBalancedBack(prefix, i, svtoken.KindLParen, svtoken.KindRParen)
		if !ok {
			return svtoken.Token{}, false
		}
		i = open - 1
		if i < 0 || prefix[i].Kind != svtoken.KindHash {
			return svtoken.Token{}, false
		}
		i--
	}

	if i < 0 || prefix[i].Kind != svtoken.KindIdent {
		return svtoken.Token{}, false
	}
	return prefix[i], true
}

// instantiationModuleTokenBeforeParamList walks backward from openIdx
// (the index of a "#(" parameter-override list's own opening paren) to
// find the module type token that introduces it. Unlike a port-
// connection "(" (see instantiationModuleTokenBefore), a "#(" is
// preceded directly by the module type with nothing in between --
// "ModuleName #(...) instanceName (...)" -- since the instance name and
// its own port-connection parens come AFTER the parameter override list,
// not before it.
func instantiationModuleTokenBeforeParamList(prefix []svtoken.Token, hashParenIdx int) (moduleTok svtoken.Token, found bool) {
	i := hashParenIdx - 1
	if i < 0 || prefix[i].Kind != svtoken.KindHash {
		return svtoken.Token{}, false
	}
	i--
	if i < 0 || prefix[i].Kind != svtoken.KindIdent {
		return svtoken.Token{}, false
	}
	return prefix[i], true
}

// skipBalancedBack walks backward from idx (the index of a closing
// bracket of kind close, already counted as depth 1) to the index of its
// matching opener of kind open, or reports ok=false if the input runs
// out first.
func skipBalancedBack(toks []svtoken.Token, idx int, open, close svtoken.Kind) (int, bool) {
	depth := 1
	for i := idx - 1; i >= 0; i-- {
		switch toks[i].Kind {
		case close:
			depth++
		case open:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// InstantiationPortNameAt reports the module a named port connection's
// port-name token belongs to, if (line, word, wordStart) is exactly such
// a token -- the identifier immediately after a still-open
// instantiation's depth-0 "." (see InstantiationContextAt's connected
// map). wordStart must be word's own start column (as WordAt returns),
// not the raw cursor position: InstantiationContextAt's backward scan
// only includes tokens strictly BEFORE the position it's given, so
// probing at wordStart+len(word) (word's END) guarantees word's own
// token is always included regardless of exactly where the cursor
// landed within it.
func InstantiationPortNameAt(text string, line int, word string, wordStart int) (moduleName string, ok bool) {
	return InstantiationPortNameIn(Lex(text), line, word, wordStart)
}

// InstantiationPortNameIn is InstantiationPortNameAt against an
// already-lexed document -- see Tokens.
func InstantiationPortNameIn(toks Tokens, line int, word string, wordStart int) (moduleName string, ok bool) {
	moduleName, connected, ok := InstantiationContextIn(toks, line, wordStart+UTF16Len(word))
	if !ok || !connected[word] {
		return "", false
	}
	return moduleName, true
}

// InstantiationParamNameAt mirrors InstantiationPortNameAt exactly (see
// its doc comment for the wordStart/word-end reasoning), for a parameter
// override's name ("#( .WIDTH(8) )") instead of a port connection's.
func InstantiationParamNameAt(text string, line int, word string, wordStart int) (moduleName string, ok bool) {
	return InstantiationParamNameIn(Lex(text), line, word, wordStart)
}

// InstantiationParamNameIn is InstantiationParamNameAt against an
// already-lexed document -- see Tokens.
func InstantiationParamNameIn(toks Tokens, line int, word string, wordStart int) (moduleName string, ok bool) {
	moduleName, connected, ok := InstantiationParamContextIn(toks, line, wordStart+UTF16Len(word))
	if !ok || !connected[word] {
		return "", false
	}
	return moduleName, true
}

// IncludePathIn returns the path written on the `include directive whose own
// token, or whose following string-literal argument, contains (line,
// character) -- unquoted, exactly as the source spells it.
//
// inDirective reports whether the cursor is on such a directive at all,
// separately from whether a path came back, so the caller can decline to fall
// through to ordinary identifier resolution when it is. That separation
// matters: WordAt is deliberately string- and comment-unaware (see its doc
// comment), so inside `include "pa_cfg.svh" it happily returns "pa_cfg" and
// resolves it to an unrelated module of that name. A cursor there has a real
// answer or none.
//
// ok is false with inDirective true when the directive's argument isn't a
// plain string literal: an `include `PATH_MACRO, which svparse's preprocessor
// doesn't expand (a documented limitation), or the angle-bracket form.
//
// Located by token span, not by line alone, so more than one directive on a
// line stays correct.
func IncludePathIn(toks Tokens, line, character int) (path string, inDirective, ok bool) {
	for i, t := range toks {
		if t.Kind != svtoken.KindDirective || t.Text != "include" || t.Line != line {
			continue
		}
		// The directive token's Character is the '`' column, while its Text
		// excludes the backtick (see svparse's lexer) -- hence the +1.
		if spans(character, t.Character, t.Character+1+UTF16Len(t.Text)) {
			return includeArg(toks, i)
		}
		if i+1 < len(toks) {
			if a := toks[i+1]; a.Line == line && spans(character, a.Character, a.Character+UTF16Len(a.Text)) {
				return includeArg(toks, i)
			}
		}
	}
	return "", false, false
}

// includeArg unquotes the string literal following the `include directive at
// index i, if that's what follows it at all.
func includeArg(toks Tokens, i int) (path string, inDirective, ok bool) {
	if i+1 >= len(toks) || toks[i+1].Kind != svtoken.KindStringLiteral {
		return "", true, false
	}
	text := toks[i+1].Text
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return "", true, false
	}
	return text[1 : len(text)-1], true, true
}

// spans reports whether character falls within [start, end], end inclusive so
// a cursor resting just past a token still counts as being on it -- the same
// courtesy WordAt extends at a word's trailing edge.
func spans(character, start, end int) bool {
	return character >= start && character <= end
}
