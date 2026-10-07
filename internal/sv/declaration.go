package sv

// The kind-specific parts of a Declaration, each allocated only for the
// declarations that have one -- see Declaration.ext.

type containerExt struct {
	ports, params []Port
}

type subprogramExt struct {
	returnType string
	args       []Port
}

type typedefExt struct {
	kind        TypedefKind
	aliasType   string
	baseType    string
	enumMembers []string
	packed      bool
	fields      []Port
}

type enumMemberExt struct {
	value, enumTypedef string
}

type parameterExt struct {
	defaultValue string
}

// The constructors return nil rather than an empty struct, so a container
// with no ports, a task with no arguments or a parameter with no default
// costs nothing beyond the Declaration itself.

func newContainerExt(ports, params []Port) any {
	if len(ports) == 0 && len(params) == 0 {
		return nil
	}
	return &containerExt{ports: ports, params: params}
}

func newSubprogramExt(returnType string, args []Port) any {
	if returnType == "" && len(args) == 0 {
		return nil
	}
	return &subprogramExt{returnType: returnType, args: args}
}

func newParameterExt(defaultValue string) any {
	if defaultValue == "" {
		return nil
	}
	return &parameterExt{defaultValue: defaultValue}
}

// Ports returns the ANSI port list of a module/interface/program
// declaration (nil for everything else, and for a container with no port
// list at all). Used for named-port-connection completion at an
// instantiation site. Each port is also separately recorded as its own
// KindPort Declaration parented to this one, so referencing a port by name
// inside the module body resolves too.
func (d *Declaration) Ports() []Port {
	if e, ok := d.ext.(*containerExt); ok {
		return e.ports
	}
	return nil
}

// Params returns the OVERRIDABLE entries of a module/interface/program's
// "#( ... )" parameter port list (nil for everything else) -- used for
// parameter-override completion at an instantiation site, the "#(...)"
// counterpart to Ports/named-port-connection completion. A "localparam"
// entry is deliberately excluded here (per the LRM it can never be
// overridden via ".name(value)", so suggesting one would be actively
// misleading), even though it -- like every entry -- still gets its own
// individual KindParameter Declaration, parented to this one, the same way
// every port does.
func (d *Declaration) Params() []Port {
	if e, ok := d.ext.(*containerExt); ok {
		return e.params
	}
	return nil
}

// ReturnType returns a function's return type, pre-formatted via
// formatType (e.g. "int", "void", "logic [7:0]") -- empty for a function
// with an implicit (unspecified) return type, and for every other Kind,
// including KindTask (tasks have no return type in SV).
func (d *Declaration) ReturnType() string {
	if e, ok := d.ext.(*subprogramExt); ok {
		return e.returnType
	}
	return ""
}

// Args returns the argument list of a function/task declaration (nil for
// everything else). Reuses Port's Name/Detail shape rather than a
// near-duplicate type -- a function argument and a port entry have the
// identical shape (a name plus a direction/type detail string).
func (d *Declaration) Args() []Port {
	if e, ok := d.ext.(*subprogramExt); ok {
		return e.args
	}
	return nil
}

// TypedefKind distinguishes what a KindTypedef declaration's underlying
// type actually is, so hover can render each shape correctly. "" means no
// recognized underlying type (a forward declaration, e.g. "typedef class
// Foo;" or a bare "typedef Foo;"), and for every other Kind.
func (d *Declaration) TypedefKind() TypedefKind {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.kind
	}
	return ""
}

// AliasType returns the rendered aliased type of a plain alias typedef
// ("typedef logic [7:0] byte_t;" -> "logic [7:0]"). Set only when
// TypedefKind is TypedefAlias.
func (d *Declaration) AliasType() string {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.aliasType
	}
	return ""
}

// BaseType returns an enum typedef's optional base type ("typedef enum int
// {...} t;" -> "int"), "" if unwritten. Set only when TypedefKind is
// TypedefEnum.
func (d *Declaration) BaseType() string {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.baseType
	}
	return ""
}

// EnumMembers returns an enum typedef's members, each rendered as "NAME"
// or "NAME = value" (the value either as written, or -- per LRM 6.19 --
// computed when none was written and the auto-increment chain is still
// known, see enumMemberTexts). Set only when TypedefKind is TypedefEnum.
func (d *Declaration) EnumMembers() []string {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.enumMembers
	}
	return nil
}

// Packed reports whether a struct/union typedef is packed. Set only when
// TypedefKind is TypedefStruct or TypedefUnion.
func (d *Declaration) Packed() bool {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.packed
	}
	return false
}

// Fields returns a struct/union typedef's members, reusing Port's
// Name/Detail shape -- a struct/union field and a port share the identical
// name+type shape. Set only when TypedefKind is TypedefStruct or
// TypedefUnion.
func (d *Declaration) Fields() []Port {
	if e, ok := d.ext.(*typedefExt); ok {
		return e.fields
	}
	return nil
}

// Value returns an enum member's resolved value (Kind == KindEnumMember
// only): the literal expression as written, or -- when none was written
// and the auto-increment chain since the last known integer value is
// still intact -- the computed LRM 6.19 default. "" when it can't be
// safely computed (a non-integer-literal explicit value breaks the chain
// for subsequent unlabeled members).
func (d *Declaration) Value() string {
	if e, ok := d.ext.(*enumMemberExt); ok {
		return e.value
	}
	return ""
}

// EnumTypedef returns the enclosing enum typedef's name (Kind ==
// KindEnumMember only), for hover context.
func (d *Declaration) EnumTypedef() string {
	if e, ok := d.ext.(*enumMemberExt); ok {
		return e.enumTypedef
	}
	return ""
}

// Default returns a parameter's default value as written (Kind ==
// KindParameter only), "" if none. Kept separate from Detail (rather than
// combined the way Port.Detail/portDetail combines direction and type)
// because a parameter's default comes AFTER its name in real SV
// declaration order ("parameter int WIDTH = 8"), unlike a port's
// direction+type prefix -- see hover's dedicated parameterText, which is
// why portEntry can't be reused here.
func (d *Declaration) Default() string {
	if e, ok := d.ext.(*parameterExt); ok {
		return e.defaultValue
	}
	return ""
}
