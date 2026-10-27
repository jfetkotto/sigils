package lspserver

import (
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// TextDocumentSignatureHelp shows the signature of the function or task
// whose call's argument list the cursor is inside, highlighting the
// argument being typed. The callee is found lexically (sv.CallContextIn)
// and then resolved the same way hover resolves a name (sv.Index.HoverInfo,
// with the callee's own position for scope), so a local or package-scoped
// function wins over an unrelated global of the same name. A callee that
// doesn't resolve to a function or task -- an instantiation, a declaration
// header, an unknown name -- yields no signature.
func (s *Server) TextDocumentSignatureHelp(_ *glsp.Context, params *protocol.SignatureHelpParams) (*protocol.SignatureHelp, error) {
	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	call, ok := sv.CallContextIn(s.tokensFor(params.TextDocument.URI, text), line, character)
	if !ok {
		return nil, nil
	}

	qualifier, hasQualifier := sv.QualifierAt(text, call.NameLine, call.NameCharacter)
	decl, ok := s.index.HoverInfo(params.TextDocument.URI, call.NameLine, call.NameCharacter, call.Name, qualifier, hasQualifier)
	if !ok || (decl.Kind != sv.KindFunction && decl.Kind != sv.KindTask) {
		return nil, nil
	}
	// The cursor is in the declaration's own header ("function int f(<here>"),
	// not at a call site.
	if decl.Line == call.NameLine && decl.Character == call.NameCharacter {
		return nil, nil
	}

	args := decl.Args()
	info := protocol.SignatureInformation{
		Label:      subprogramSignature(decl),
		Parameters: make([]protocol.ParameterInformation, len(args)),
	}
	for i, a := range args {
		info.Parameters[i] = protocol.ParameterInformation{Label: portEntry(a)}
	}

	active := call.ActiveIndex
	if call.ActiveName != "" {
		active = len(args) // out of range: highlight nothing unless the name matches below
		for i, a := range args {
			if a.Name == call.ActiveName {
				active = i
				break
			}
		}
	}
	activeParam := protocol.UInteger(active)
	activeSig := protocol.UInteger(0)
	return &protocol.SignatureHelp{
		Signatures:      []protocol.SignatureInformation{info},
		ActiveSignature: &activeSig,
		ActiveParameter: &activeParam,
	}, nil
}
