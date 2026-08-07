// Package sv indexes SystemVerilog/Verilog source: declarations, their
// nesting, and the word under a cursor. Declarations come from
// svparse (github.com/jfetkotto/svparse) -- a real lexer, preprocessor
// (macro expansion, conditional compilation, `include resolution), and
// declaration-grade parser -- via Scan, which translates svparse's AST
// into this package's own flat Declaration/Occurrence model. This package
// itself still does no parsing of its own: it's an adapter over svparse's
// output plus the workspace-scoped index/resolution logic (Index) built on
// top of it, the same separation svparse itself uses between its
// token/lexer, ast/parser package pairs.
//
// svparse's parser is declaration-grade, not a full grammar (statement/
// expression bodies inside a function or task are recognized and skipped,
// never parsed) -- see Index's own doc comment for what "scope-aware"
// means built on top of that.
package sv

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/jfetkotto/svparse/ast"
	"github.com/jfetkotto/svparse/lexer"
	"github.com/jfetkotto/svparse/parser"
	"github.com/jfetkotto/svparse/preprocessor"
	svtoken "github.com/jfetkotto/svparse/token"
)

type Kind string

const (
	KindModule     Kind = "module"
	KindInterface  Kind = "interface"
	KindProgram    Kind = "program"
	KindClass      Kind = "class"
	KindPackage    Kind = "package"
	KindFunction   Kind = "function"
	KindTask       Kind = "task"
	KindTypedef    Kind = "typedef"
	KindEnumMember Kind = "enum_member"

	// KindPort, KindVariable, and KindParameter are leaf declarations --
	// never containers, never globally referenceable by bare name (see
	// containerKinds/GloballyReferenceableKinds below). Each is parented to
	// its enclosing container like any other declaration, which is what
	// makes it resolvable via the existing scope-chain walk in index.go
	// with no changes to that resolution logic itself.
	KindPort      Kind = "port"
	KindVariable  Kind = "variable"
	KindParameter Kind = "parameter"
)

// ContainerKinds are the declaration kinds that can hold other
// declarations and so participate in the container stack / scope chain.
var containerKinds = map[Kind]bool{
	KindModule:    true,
	KindInterface: true,
	KindProgram:   true,
	KindClass:     true,
	KindPackage:   true,
	KindFunction:  true,
	KindTask:      true,
}

// GloballyReferenceableKinds are kinds realistically referenceable by bare
// name from anywhere in the workspace without a package/class qualifier.
// Bare functions/tasks/typedefs need a qualifier or module-local
// visibility, so they're deliberately excluded from unqualified,
// out-of-scope global fallback -- see Index.FindDefinition.
var GloballyReferenceableKinds = map[Kind]bool{
	KindModule:    true,
	KindInterface: true,
	KindProgram:   true,
	KindClass:     true,
	KindPackage:   true,
}

// Declaration is a named construct found while scanning a source file.
// Parent is an index into the same []Declaration slice within the same
// file's bucket (see Scan), or -1 if the declaration is at file (top)
// scope -- including the first declaration on the far side of an
// `include boundary, whose lexical parent lives in a different file (see
// declarationsFromAST).
type Declaration struct {
	Kind         Kind
	Name         string
	Line         int // zero-based, matching LSP Position
	Character    int // zero-based, start of Name
	EndLine      int
	EndCharacter int
	Parent       int

	// Prototype is true for a function/task declared with no body ("extern
	// function ...;", "virtual"/"pure virtual function ...;", or a DPI
	// import). It's what distinguishes goto-declaration from
	// goto-definition for those; everything else has no such split.
	Prototype bool

	// Ports holds the ANSI port list for a module/interface/program
	// declaration (nil for everything else, and for a container with no
	// port list at all). Used for named-port-connection completion at an
	// instantiation site. Each port is also separately recorded as its own
	// KindPort Declaration parented to this one, so referencing a port by
	// name inside the module body resolves too.
	Ports []Port

	// Params holds the OVERRIDABLE entries of a module/interface/program's
	// "#( ... )" parameter port list (nil for everything else) -- used for
	// parameter-override completion at an instantiation site, the "#(...)"
	// counterpart to Ports/named-port-connection completion. A "localparam"
	// entry is deliberately excluded here (per the LRM it can never be
	// overridden via ".name(value)", so suggesting one would be actively
	// misleading), even though it -- like every entry -- still gets its
	// own individual KindParameter Declaration below, parented to this
	// one, the same way every port does.
	Params []Port

	// ReturnType holds a function's return type, pre-formatted via
	// formatType (e.g. "int", "void", "logic [7:0]") -- empty for a
	// function with an implicit (unspecified) return type, and for every
	// other Kind, including KindTask (tasks have no return type in SV, so
	// there's nothing for the *ast.Task case to source this from).
	ReturnType string

	// Args holds the argument list for a function/task declaration (nil
	// for everything else). Reuses Port's Name/Detail shape rather than a
	// near-duplicate type -- a function argument and a port entry have the
	// identical shape (a name plus a direction/type detail string).
	Args []Port

	// TypedefKind distinguishes what a KindTypedef declaration's
	// Underlying actually is, so hover can render each shape correctly.
	// "" means no recognized underlying type (a forward declaration, e.g.
	// "typedef class Foo;" or a bare "typedef Foo;").
	TypedefKind string // "alias" | "enum" | "struct" | "union" | ""

	// AliasType holds the rendered aliased type for a plain alias typedef
	// ("typedef logic [7:0] byte_t;" -> "logic [7:0]"). Set only when
	// TypedefKind == "alias".
	AliasType string

	// BaseType holds an enum typedef's optional base type ("typedef enum
	// int {...} t;" -> "int"), "" if unwritten. EnumMembers holds each
	// member rendered as "NAME" or "NAME = value" (the value either as
	// written, or -- per LRM 6.19 -- computed when none was written and
	// the auto-increment chain is still known, see enumMemberTexts). Both
	// set only when TypedefKind == "enum".
	BaseType    string
	EnumMembers []string

	// Packed and Fields describe a struct/union typedef's body. Fields
	// reuses Port's Name/Detail shape -- a struct/union field and a port
	// share the identical name+type shape. Set only when TypedefKind ==
	// "struct" or "union".
	Packed bool
	Fields []Port

	// Value holds an enum member's resolved value (Kind == KindEnumMember
	// only): the literal expression as written, or -- when none was
	// written and the auto-increment chain since the last known integer
	// value is still intact -- the computed LRM 6.19 default. "" when it
	// can't be safely computed (a non-integer-literal explicit value
	// breaks the chain for subsequent unlabeled members).
	Value string

	// EnumTypedef holds the enclosing enum typedef's name (Kind ==
	// KindEnumMember only), for hover context.
	EnumTypedef string

	// TypeName holds a port's or variable's declared type's bare name
	// (Kind == KindPort or KindVariable only; "" otherwise) -- e.g.
	// "logic" for a plain net, or "ty_bundle" for a struct/union typedef
	// reference. It's what backs struct-member completion after
	// "receiver.": Index.StructFields looks TypeName up against the
	// workspace's typedefs, and a name that turns out to be a builtin
	// keyword (like "logic") or a non-struct/union typedef is expected to
	// simply fail to match there -- no special-casing of builtins is
	// needed here, this is populated unconditionally from ast.Type.Name.
	// KindParameter isn't covered even though a struct-typed parameter is
	// possible in principle ("parameter my_struct_t P = ...") --
	// convertParams never fed this, following Detail's own precedent of
	// treating parameters separately; left as follow-on work if that gap
	// turns out to matter in practice.
	TypeName string

	// Detail holds a human-readable rendering of this declaration's type
	// (Kind == KindPort: direction+type, e.g. "input logic [7:0]", the
	// same rendering already used for the enclosing module's own Ports
	// list entry, see Port.Detail/portDetail; Kind == KindParameter: just
	// the type, e.g. "int", "" if untyped -- see Default below for the
	// piece portEntry-style rendering can't fold in) -- attached to the
	// declaration's own individual entry (not just its container's list)
	// so hovering it, wherever it's referenced, has something to show.
	Detail string

	// Default holds a parameter's default value as written (Kind ==
	// KindParameter only), "" if none. Kept separate from Detail (rather
	// than combined the way Port.Detail/portDetail combines direction and
	// type) because a parameter's default comes AFTER its name in real SV
	// declaration order ("parameter int WIDTH = 8"), unlike a port's
	// direction+type prefix -- see hover's dedicated parameterText,
	// which is why portEntry can't be reused here.
	Default string
}

// Port is one entry from a module/interface/program's ANSI port list, or a
// function/task's argument list (see Declaration.Args). Detail is a
// human-readable direction+type rendering (e.g. "input logic [7:0]") for
// use as completion-item documentation -- reconstructed from svparse's
// parsed ast.Type, not a token-for-token echo of the source.
type Port struct {
	Name   string
	Detail string
}

// importDecl records one "import pkg::name;" / "import pkg::*;" as a
// side-channel entry, not a Declaration -- see addDecl's doc comment for
// why an import isn't given its own Kind. Parent uses the same
// same-bucket-index convention as Declaration.Parent (-1 = file scope),
// which is what lets an import's visibility compose with the ordinary
// container-ancestor walk resolveRefsLocked already does for name
// shadowing, instead of needing a separate scope model.
type importDecl struct {
	Package string
	Member  string // "*" for wildcard, or one specific imported name
	Parent  int
}

// connectionSite records one named port connection or parameter override
// at an instantiation site (".clk(sig)", ".clk" implicit shorthand, or
// "#(.WIDTH(8))") -- a side-channel entry, not a Declaration, for the
// same reason importDecl is one: a connection doesn't declare a new
// name, it references an existing one on ModuleType (recorded as
// written, not yet resolved against the index). Line/Character are the
// connection's own name token (not the preceding "."), matching where
// WordAt/rename expect a name to start -- see svparse's
// PortConnection/ParamOverride Position convention.
type connectionSite struct {
	ModuleType      string
	Name            string
	Line, Character int
}

// Scan returns text's declarations -- bucketed by the file each one
// actually belongs to, see declarationsFromAST -- every identifier
// occurrence in it, and every preprocessing/parsing error recorded along
// the way, also bucketed by file. Index.SetFile uses this to populate all
// three.
//
// Declarations and occurrences deliberately come from different token
// streams. Occurrences (references/rename/documentHighlight) come from
// svparse's raw, unexpanded lexer output -- every occurrence position is
// real, editable text in this exact file, which matters for rename
// correctness. Declarations come from the full preprocess+parse pipeline,
// so they reflect real macro/conditional-compilation state and carry real
// types -- diagnostics are this pipeline's own by-product, not a separate
// pass.
//
// resolver, if non-nil, resolves `include directives during preprocessing
// (see IncludeResolver) -- pass nil for standalone/test use where
// cross-file includes aren't relevant; a bare `include then becomes a
// recorded (non-fatal) preprocessing error instead of pulling in content.
//
// initialMacros seeds object-like macros as if `defined before text's
// first token -- the workspace's captured `+define+` entries (see
// workspace.FilelistDiscoverer.Defines), so a company-wide flag can gate
// an `ifdef the same way an in-source `define would. Pass nil where none
// apply.
func Scan(uri, text string, resolver IncludeResolver, initialMacros map[string]string) (decls map[string][]Declaration, occurrences []Occurrence, diags map[string][]Diagnostic, imports map[string][]importDecl, connections map[string][]connectionSite) {
	lexToks, _ := lexer.Lex(text) // lex errors don't block occurrence collection -- best-effort on malformed/mid-edit text
	occurrences = occurrencesFromSVParseTokens(lexToks)

	ppToks, ppErrs := preprocessor.PreprocessWithOptions(uri, text, resolver, preprocessor.Options{InitialMacros: initialMacros})
	f, parseErrs := parser.Parse(uri, ppToks)

	diags = diagnosticsByFile(uri, ppErrs, parseErrs)
	decls, imports, connections = declarationsFromAST(f)
	return decls, occurrences, diags, imports, connections
}

// Diagnostic is a single preprocessing or parsing problem svparse
// recorded, deliberately minimal and LSP-agnostic (no Severity/Source --
// those are protocol.Diagnostic formatting decisions, made in
// internal/lspserver) matching how this package avoids depending on glsp
// elsewhere too.
type Diagnostic struct {
	Line      int
	Character int
	Message   string
}

// diagnosticsByFile merges svparse's preprocessor and parser error lists
// (both already File-attributed, correctly, across `include boundaries --
// preprocessor.Error includes re-attributed lexer errors too, via
// lexToTokens's own error reporting) into one map keyed by file, seeding
// uri itself with a nil (not absent) entry so a file that used to have
// errors and now has none still gets an entry -- the same reason
// declarationsFromAST seeds buckets[f.Path] up front.
func diagnosticsByFile(uri string, ppErrs []preprocessor.Error, parseErrs []parser.Error) map[string][]Diagnostic {
	diags := map[string][]Diagnostic{uri: nil}
	for _, e := range ppErrs {
		diags[e.File] = append(diags[e.File], Diagnostic{Line: e.Line, Character: e.Character, Message: e.Message})
	}
	for _, e := range parseErrs {
		diags[e.File] = append(diags[e.File], Diagnostic{Line: e.Line, Character: e.Character, Message: e.Message})
	}
	return diags
}

// ScanDeclarations is a convenience wrapper around Scan for callers that
// only want one file's own declarations and don't need cross-file include
// resolution or initial macros -- mainly tests.
func ScanDeclarations(uri, text string) []Declaration {
	decls, _, _, _, _ := Scan(uri, text, nil, nil)
	return decls[uri]
}

// declarationsFromAST walks f's declarations and buckets them by the URI
// each one actually belongs to (ast.Position.File), not just f.Path -- a
// declaration reached via `include is attributed to the included file's
// own bucket, matching how the rest of this package keys everything by
// file (goto-def on a type declared in a shared header should point at the
// header, regardless of which file's scan happened to discover it). Import
// statements (see importDecl) are bucketed the same way, in a parallel map.
//
// Parent indices are always scoped to their own bucket (Declaration.Parent
// is a same-slice index, inherited from the flat scanner this replaced).
// A declaration whose File differs from its lexical parent's -- the first
// declaration on the far side of an `include boundary -- starts a fresh,
// file-scoped (Parent -1) entry in its own bucket instead of attempting a
// cross-file Parent link, which that flat int-index model has no way to
// express. Nesting *within* the included file's own content is still
// tracked normally from that point on.
func declarationsFromAST(f *ast.File) (map[string][]Declaration, map[string][]importDecl, map[string][]connectionSite) {
	buckets := map[string][]Declaration{f.Path: nil}
	impBuckets := map[string][]importDecl{f.Path: nil}
	connBuckets := map[string][]connectionSite{f.Path: nil}
	walkDecls(f.Decls, f.Path, -1, buckets, impBuckets, connBuckets)
	return buckets, impBuckets, connBuckets
}

func walkDecls(decls []ast.Decl, uri string, parent int, buckets map[string][]Declaration, impBuckets map[string][]importDecl, connBuckets map[string][]connectionSite) {
	for _, d := range decls {
		declURI := d.Pos().File
		p := parent
		if declURI != uri {
			p = -1 // crossing an `include boundary -- see declarationsFromAST
		}
		addDecl(d, declURI, p, buckets, impBuckets, connBuckets)
	}
}

// addDecl converts one AST node into its Declaration(s), appended to
// buckets[uri], recursing into container-shaped nodes' bodies. An
// *ast.Import gets no Declaration of its own -- it doesn't declare a new
// name (the package it names already has its own Declaration elsewhere,
// and a specific-member import names an existing declaration, not a new
// one) -- instead it's appended to impBuckets[uri] as an importDecl side
// channel, consulted only by Index's import-based resolution step. An
// *ast.Instantiation is handled the same way: its named connections/
// overrides are appended to connBuckets[uri] as connectionSite side
// channels, consulted only by Index's instantiation-connection scoping
// (find-references/rename). Remaining node kinds with no useful
// representation in any model (constraints) are still silently skipped.
func addDecl(d ast.Decl, uri string, parent int, buckets map[string][]Declaration, impBuckets map[string][]importDecl, connBuckets map[string][]connectionSite) {
	switch n := d.(type) {
	case *ast.Container:
		idx := appendDecl(buckets, uri, Declaration{
			Kind: containerKind(n.ContainerKind), Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.EndLine, EndCharacter: n.EndCharacter,
			Parent: parent,
			Ports:  convertPorts(n.Ports),
			Params: convertParams(n.Params),
		})
		for _, port := range n.Ports {
			appendDecl(buckets, uri, Declaration{
				Kind: KindPort, Name: port.Name,
				Line: port.Line, Character: port.Character,
				EndLine: port.Line, EndCharacter: port.Character + UTF16Len(port.Name),
				Parent:   idx,
				Detail:   portDetail(port.Direction, port.Type),
				TypeName: port.Type.Name,
			})
		}
		for _, param := range n.Params {
			appendDecl(buckets, uri, Declaration{
				Kind: KindParameter, Name: param.Name,
				Line: param.Line, Character: param.Character,
				EndLine: param.Line, EndCharacter: param.Character + UTF16Len(param.Name),
				Parent:  idx,
				Detail:  formatType(param.Type),
				Default: joinTokenText(param.Default),
			})
		}
		walkDecls(n.Body, uri, idx, buckets, impBuckets, connBuckets)

	case *ast.Class:
		idx := appendDecl(buckets, uri, Declaration{
			Kind: KindClass, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.EndLine, EndCharacter: n.EndCharacter,
			Parent: parent,
		})
		walkDecls(n.Body, uri, idx, buckets, impBuckets, connBuckets)

	case *ast.Package:
		idx := appendDecl(buckets, uri, Declaration{
			Kind: KindPackage, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.EndLine, EndCharacter: n.EndCharacter,
			Parent: parent,
		})
		walkDecls(n.Body, uri, idx, buckets, impBuckets, connBuckets)

	case *ast.Function:
		appendDecl(buckets, uri, Declaration{
			Kind: KindFunction, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.EndLine, EndCharacter: n.EndCharacter,
			Parent: parent, Prototype: n.Prototype,
			ReturnType: formatType(n.ReturnType),
			Args:       convertArgs(n.Args),
		})

	case *ast.Task:
		appendDecl(buckets, uri, Declaration{
			Kind: KindTask, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.EndLine, EndCharacter: n.EndCharacter,
			Parent: parent, Prototype: n.Prototype,
			Args: convertArgs(n.Args),
		})

	case *ast.Typedef:
		// Enum members are recorded parented to the typedef's OWN parent,
		// not the typedef itself -- SV enum members are referenced
		// unqualified in the enclosing scope, not through the enum type's
		// name the way a class's members would be. Struct/union members
		// get no such treatment: they're only reachable through the
		// struct/union type itself (recorded on the Declaration below, as
		// Fields, for hover display) -- this package still doesn't model
		// field-access reference resolution ("instance.field"). Completion
		// after "instance." is a lighter-weight problem than resolution,
		// though, and IS handled -- see Declaration.TypeName and
		// Index.StructFields -- since it only needs the receiver's own
		// declared type, not the field reference itself.
		var enumLabels, enumValues []string
		if e, ok := n.Underlying.(*ast.Enum); ok {
			enumLabels, enumValues = enumMemberTexts(e.Members)
			for i, m := range e.Members {
				appendDecl(buckets, uri, Declaration{
					Kind: KindEnumMember, Name: m.Name,
					Line: m.Line, Character: m.Character,
					EndLine: m.Line, EndCharacter: m.Character + UTF16Len(m.Name),
					Parent:      parent,
					Value:       enumValues[i],
					EnumTypedef: n.Name,
				})
			}
		}
		td := Declaration{
			Kind: KindTypedef, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.Line, EndCharacter: n.Character + UTF16Len(n.Name),
			Parent: parent,
		}
		switch u := n.Underlying.(type) {
		case *ast.TypeAlias:
			td.TypedefKind, td.AliasType = "alias", formatType(u.Type)
		case *ast.Enum:
			td.TypedefKind, td.BaseType, td.EnumMembers = "enum", formatType(u.BaseType), enumLabels
		case *ast.Struct:
			td.TypedefKind, td.Packed, td.Fields = "struct", u.Packed, structUnionFields(u.Members)
		case *ast.Union:
			td.TypedefKind, td.Packed, td.Fields = "union", u.Packed, structUnionFields(u.Members)
		}
		appendDecl(buckets, uri, td)

	case *ast.Variable:
		appendDecl(buckets, uri, Declaration{
			Kind: KindVariable, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.Line, EndCharacter: n.Character + UTF16Len(n.Name),
			Parent:   parent,
			TypeName: n.Type.Name,
		})

	case *ast.Parameter:
		appendDecl(buckets, uri, Declaration{
			Kind: KindParameter, Name: n.Name,
			Line: n.Line, Character: n.Character,
			EndLine: n.Line, EndCharacter: n.Character + UTF16Len(n.Name),
			Parent:  parent,
			Detail:  formatType(n.Type),
			Default: joinTokenText(n.Default),
		})

	case *ast.Import:
		impBuckets[uri] = append(impBuckets[uri], importDecl{Package: n.Package, Member: n.Member, Parent: parent})

	case *ast.Instantiation:
		for _, inst := range n.Instances {
			for _, conn := range inst.Connections {
				if conn.Name == "" || conn.Wildcard {
					continue // positional or ".*" -- no specific name to attribute
				}
				connBuckets[uri] = append(connBuckets[uri], connectionSite{
					ModuleType: n.ModuleType, Name: conn.Name,
					Line: conn.Line, Character: conn.Character,
				})
			}
		}
		for _, ov := range n.ParamOverrides { // shared by every instance in this statement, recorded once
			if ov.Name == "" {
				continue // positional override -- no name to attribute
			}
			connBuckets[uri] = append(connBuckets[uri], connectionSite{
				ModuleType: n.ModuleType, Name: ov.Name,
				Line: ov.Line, Character: ov.Character,
			})
		}
	}
}

// appendDecl appends d to buckets[uri] and returns its index within that
// bucket, for use as a child's Parent.
func appendDecl(buckets map[string][]Declaration, uri string, d Declaration) int {
	buckets[uri] = append(buckets[uri], d)
	return len(buckets[uri]) - 1
}

func containerKind(k ast.ContainerKind) Kind {
	switch k {
	case ast.KindInterface:
		return KindInterface
	case ast.KindProgram:
		return KindProgram
	default:
		return KindModule
	}
}

func convertPorts(ports []ast.Port) []Port {
	if len(ports) == 0 {
		return nil
	}
	out := make([]Port, len(ports))
	for i, p := range ports {
		out[i] = Port{Name: p.Name, Detail: portDetail(p.Direction, p.Type)}
	}
	return out
}

func convertArgs(args []ast.Arg) []Port {
	if len(args) == 0 {
		return nil
	}
	out := make([]Port, len(args))
	for i, a := range args {
		out[i] = Port{Name: a.Name, Detail: portDetail(a.Direction, a.Type)}
	}
	return out
}

// convertParams builds Declaration.Params from a container's "#( ... )"
// parameter port list, mirroring convertPorts -- but, unlike convertPorts,
// skips IsLocal ("localparam") entries: this list feeds parameter-override
// completion at an instantiation site, and a localparam entry can never
// legally appear on the right of ".name(value)" there. Detail combines
// type and default ("int = 8") since it's only ever shown as a flat
// completion-item string, unlike the individual per-entry Declaration's
// separate Detail/Default (see addDecl's *ast.Container case), which
// hover needs apart to render in real SV declaration order.
func convertParams(params []ast.Parameter) []Port {
	var out []Port
	for _, p := range params {
		if p.IsLocal {
			continue
		}
		out = append(out, Port{Name: p.Name, Detail: paramDetail(p.Type, p.Default)})
	}
	return out
}

func paramDetail(t ast.Type, def []preprocessor.Token) string {
	parts := make([]string, 0, 2)
	if typeText := formatType(t); typeText != "" {
		parts = append(parts, typeText)
	}
	if len(def) > 0 {
		parts = append(parts, "= "+joinTokenText(def))
	}
	return strings.Join(parts, " ")
}

func portDetail(dir ast.Direction, t ast.Type) string {
	parts := make([]string, 0, 2)
	if dir != ast.DirUnspecified {
		parts = append(parts, string(dir))
	}
	if typeText := formatType(t); typeText != "" {
		parts = append(parts, typeText)
	}
	return strings.Join(parts, " ")
}

// formatType renders t as a human-readable string (e.g. "logic [7:0]",
// "unsigned int", "my_pkg::my_type_t") for use in Port.Detail -- a
// presentation reconstruction, not a token-for-token echo of the source
// (dimension bounds are re-joined from their raw token spans with no
// original whitespace preserved).
func formatType(t ast.Type) string {
	var b strings.Builder
	if t.Signed {
		b.WriteString("signed ")
	} else if t.Unsigned {
		b.WriteString("unsigned ")
	}
	if t.PackageQualifier != "" {
		b.WriteString(t.PackageQualifier)
		b.WriteString("::")
	}
	b.WriteString(t.Name)
	for _, d := range t.PackedDims {
		b.WriteString(" [")
		b.WriteString(joinTokenText(d.Left))
		if d.Right != nil {
			b.WriteString(":")
			b.WriteString(joinTokenText(d.Right))
		}
		b.WriteString("]")
	}
	return b.String()
}

func joinTokenText(toks []preprocessor.Token) string {
	var b strings.Builder
	for _, t := range toks {
		b.WriteString(t.Text)
	}
	return b.String()
}

// structUnionFields converts a struct/union typedef's member list for
// Declaration.Fields, mirroring convertPorts -- a struct/union field and
// a port share the identical name+type-detail shape. Unpacked dims on a
// field aren't rendered, the same limitation Port.Detail already has for
// ports/args.
func structUnionFields(members []ast.Variable) []Port {
	if len(members) == 0 {
		return nil
	}
	out := make([]Port, len(members))
	for i, m := range members {
		out[i] = Port{Name: m.Name, Detail: formatType(m.Type)}
	}
	return out
}

// enumMemberTexts computes, for each enum member in order, a typedef-body
// label ("NAME" or "NAME = value") and the member's bare value text alone
// (used for individual enum-member hover) -- one pass so the two can
// never disagree. A written value is echoed as-is; an unwritten value is
// the LRM 6.19 auto-increment default (previous value + 1, or 0 for the
// first member) computed from a running counter that re-anchors on every
// explicit value that's a single plain base-10 integer literal ("5", not
// "8'h2" or "WIDTH-1") -- so "{FOO=0, BAR, BAZ=10, QUX}" yields BAR=1 and
// QUX=11, each continuing from its nearest preceding explicit or computed
// value. A non-integer-literal explicit value breaks the chain (known =
// false): subsequent unlabeled members are left with no computed value
// ("" / bare name) rather than guess, since this package does no real
// constant-expression evaluation.
func enumMemberTexts(members []ast.EnumMember) (labels, values []string) {
	labels = make([]string, len(members))
	values = make([]string, len(members))
	next := 0
	known := true
	for i, m := range members {
		var value string
		if len(m.Value) > 0 {
			value = joinTokenText(m.Value)
			if n, err := strconv.Atoi(value); err == nil {
				next, known = n+1, true
			} else {
				known = false
			}
		} else if known {
			value = strconv.Itoa(next)
			next++
		}
		values[i] = value
		if value != "" {
			labels[i] = m.Name + " = " + value
		} else {
			labels[i] = m.Name
		}
	}
	return labels, values
}

// occurrencesFromSVParseTokens collects every identifier-like token
// (KindIdent, KindKeyword, and KindSystemIdent -- e.g. "$display") as an
// Occurrence. Keywords are included deliberately, matching this package's
// prior tokenizer, which never distinguished them from identifiers at the
// lexical level -- an unrestrained ScopedOccurrences query on a word like
// "module" simply won't resolve to any Declaration and so is harmless.
// Identical identifier text within one file (a signal like "clk" might be
// referenced hundreds of times) is interned to a single string so storing
// every occurrence -- not just declarations -- doesn't multiply memory use
// by the number of references; see Index's doc comment for why occurrence
// data is kept at all.
func occurrencesFromSVParseTokens(toks []svtoken.Token) []Occurrence {
	seen := make(map[string]string)
	out := make([]Occurrence, 0, len(toks))
	for _, t := range toks {
		if t.Kind != svtoken.KindIdent && t.Kind != svtoken.KindKeyword && t.Kind != svtoken.KindSystemIdent {
			continue
		}
		name, ok := seen[t.Text]
		if !ok {
			name = t.Text
			seen[name] = name
		}
		out = append(out, Occurrence{Name: name, Line: t.Line, Character: t.Character})
	}
	return out
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

// IsIdentifier reports whether name is a valid SV identifier by this
// package's own lexical rules -- useful for validating a proposed rename
// target before generating a WorkspaceEdit for it. A reserved word (see
// IsKeyword) is rejected too: it's lexically identifier-shaped, but
// renaming something TO e.g. "logic" would produce source no SV compiler
// accepts.
func IsIdentifier(name string) bool {
	runes := []rune(name)
	if len(runes) == 0 || !isIdentStart(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if !isIdentChar(r) {
			return false
		}
	}
	return !IsKeyword(name)
}

// IsKeyword reports whether name is one of SystemVerilog's reserved words
// (IEEE 1800-2017 Annex B.6). Exposed here (wrapping svparse/token's own
// Keywords map, the same pattern UTF16Len already uses for svparse/token's
// UTF16Len) so lspserver can guard against renaming a keyword occurrence
// without importing svparse directly -- ScopedOccurrences deliberately
// includes keyword tokens (see occurrencesFromSVParseTokens), so a rename
// request positioned on one would otherwise happily rewrite every
// occurrence of, say, "module" in the workspace.
func IsKeyword(name string) bool {
	return svtoken.Keywords[name]
}
