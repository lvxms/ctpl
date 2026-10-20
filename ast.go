package ctpl

// ---------------------------------------------------------------------------
// Statement nodes (the bodies of templates).
// ---------------------------------------------------------------------------

type node interface{ npos() Pos }

type textNode struct {
	pos Pos
	text string
}

func (n textNode) npos() Pos { return n.pos }

// outputNode prints the result of an expression.
type outputNode struct {
	pos Pos
	x   expr
}

func (n outputNode) npos() Pos { return n.pos }

type nodeIf struct {
	pos  Pos
	cond expr
	then []node
	els  []node // else body; may start with a nested nodeIf for else-if
}

func (n nodeIf) npos() Pos { return n.pos }

type nodeFor struct {
	pos  Pos
	x    expr
	vars []string // length 0, 1 (element) or 2 (index, element)
	body []node
	els  []node
}

func (n nodeFor) npos() Pos { return n.pos }

type nodeWith struct {
	pos     Pos
	x       expr
	varName string // optional variable bound to the value ("" if none)
	body    []node
	els     []node
}

func (n nodeWith) npos() Pos { return n.pos }

// nodeTemplate renders an associated template by name.
type nodeTemplate struct {
	pos  Pos
	name string
	arg  expr // optional data argument (nil means current dot)
}

func (n nodeTemplate) npos() Pos { return n.pos }

// nodeAssign declares ($x := ...) or sets ($x = ...) a variable.
type nodeAssign struct {
	pos    Pos
	name   string
	isDecl bool
	val    expr
}

func (n nodeAssign) npos() Pos { return n.pos }

type nodeBreak struct{ pos Pos }

func (n nodeBreak) npos() Pos { return n.pos }

type nodeContinue struct{ pos Pos }

func (n nodeContinue) npos() Pos { return n.pos }

// templateDef is a parsed named template body (from define/block).
type templateDef struct {
	pos   Pos
	nodes []node
}

// ---------------------------------------------------------------------------
// Expression nodes.
// ---------------------------------------------------------------------------

type expr interface{ epos() Pos }

type literalExpr struct {
	pos Pos
	val any
}

func (e literalExpr) epos() Pos { return e.pos }

type dotExpr struct{ pos Pos }

func (e dotExpr) epos() Pos { return e.pos }

// varRefExpr references the root ($), the current value (see dotExpr) or a
// named variable ($x). name is "$" or "$x".
type varRefExpr struct {
	pos  Pos
	name string
}

func (e varRefExpr) epos() Pos { return e.pos }

// fnCallExpr is a call to a registered function: name(args...).
type fnCallExpr struct {
	pos  Pos
	name string
	args []expr
}

func (e fnCallExpr) epos() Pos { return e.pos }

type opKind int

const (
	opField opKind = iota // .name
	opIndex               // [e]
	opCall                // (args) applied to the preceding value
)

type chainOp struct {
	pos   Pos
	kind  opKind
	name  string // opField / opCall (method or function-valued field)
	idx   expr   // opIndex
	args  []expr // opCall
}

// chainExpr is a base expression followed by selectors such as .a.b[0].m(1).
type chainExpr struct {
	pos  Pos
	base expr
	ops  []chainOp
}

func (e chainExpr) epos() Pos { return e.pos }

type unaryExpr struct {
	pos Pos
	op  string // ! ~ - +
	x   expr
}

func (e unaryExpr) epos() Pos { return e.pos }

type binaryExpr struct {
	pos Pos
	op  string
	x   expr
	y   expr
}

func (e binaryExpr) epos() Pos { return e.pos }

type ternaryExpr struct {
	pos Pos
	c   expr
	t   expr
	f   expr
}

func (e ternaryExpr) epos() Pos { return e.pos }
