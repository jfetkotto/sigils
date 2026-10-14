package lspserver

import (
	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/sv"
)

// cachedTokens is one open document's lexed form, along with the exact
// text it was lexed from.
type cachedTokens struct {
	text string
	toks sv.Tokens
}

// tokensFor returns text lexed, reusing the previous result for an open
// document whose text hasn't changed since.
//
// Every cursor request (hover, highlight, definition, references, rename,
// completion) needs the whole document's tokens, and highlight alone fires
// on every cursor move, so lexing per request made lexing nearly all of
// each request's allocation. The entry is validated against the text
// itself rather than the document version: a string compare against the
// same backing array the store hands out is a pointer check, and it stays
// correct however a client numbers its versions. Only open documents are
// cached, and each entry is dropped when its document closes, so the cache
// never outgrows the set of buffers the editor holds.
func (s *Server) tokensFor(uri, text string) sv.Tokens {
	if _, open := s.docs.Get(document.URI(uri)); !open {
		return sv.Lex(text)
	}
	s.tokMu.Lock()
	c, ok := s.tokens[uri]
	s.tokMu.Unlock()
	if ok && c.text == text {
		return c.toks
	}
	toks := sv.Lex(text)
	s.tokMu.Lock()
	s.tokens[uri] = cachedTokens{text: text, toks: toks}
	s.tokMu.Unlock()
	return toks
}

// forgetTokens drops uri's cached tokens, if any.
func (s *Server) forgetTokens(uri string) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	delete(s.tokens, uri)
}
