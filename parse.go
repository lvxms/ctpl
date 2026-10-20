package ctpl

import (
	"fmt"
	"strings"
)

// parser converts the lexer's segment stream into a statement list plus any
// named template definitions found via define/block.
type parser struct {
	name            string
	funcs           map[string]bool // registered function names (for validation)
	segs            []segment
	i               int
	defs            map[string]*templateDef
	breakDepth      int  // >0 while parsing inside a for body
	leftLen         int  // length of the action left delimiter (for column offsets)
	caseInsensitive bool // when true, function names match case-insensitively
}

func parseText(name string, segs []segment, funcs map[string]bool, leftDelim string, caseInsensitive bool) ([]node, map[string]*templateDef, error) {
	p := &parser{name: name, funcs: funcs, segs: segs, defs: map[string]*templateDef{}, leftLen: len(leftDelim), caseInsensitive: caseInsensitive}
	body, stop, err := p.parseBlock(nil)
	if err != nil {
		return nil, nil, err
	}
	if stop != "" {
		return nil, nil, p.errorf(segs[p.i-1].line, segs[p.i-1].col, "unexpected %s", stop)
	}
	return body, p.defs, nil
}

// isBuiltinPseudo reports whether name is a pseudo-function that the engine
// always recognises at parse time, regardless of whether it was registered
// via Funcs or auto-added to the function-name map. This lets templates
// reference built-in pseudo-functions like env("KEY") even when the
// template's function table has not been augmented for them.
func isBuiltinPseudo(name string) bool {
	switch name {
	case "env":
		return true
	}
	return false
}

func (p *parser) errorf(line, col int, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if line > 0 {
		return fmt.Errorf("template: %s:%d:%d: %s", p.name, line, col, msg)
	}
	return fmt.Errorf("template: %s: %s", p.name, msg)
}

func (p *parser) errAt(pos Pos, format string, args ...any) error {
	return p.errorf(pos.Line, pos.Col, format, args...)
}

// stopWords: "else"/"end" at which the block stops; caller consumes them.
func (p *parser) parseBlock(stops map[string]bool) ([]node, string, error) {
	var list []node
	for p.i < len(p.segs) {
		s := p.segs[p.i]
		if s.kind == segText {
			if s.text != "" {
				list = append(list, textNode{pos: Pos{s.line, s.col}, text: s.text})
			}
			p.i++
			continue
		}
		toks, err := lexSub(s.text, Pos{s.line, s.col + p.leftLen})
		if err != nil {
			return nil, "", p.errorf(s.line, s.col, "%v", err)
		}
		if len(toks) == 0 {
			return nil, "", p.errorf(s.line, s.col, "empty action")
		}
		t0 := toks[0]
		if t0.kind == tkKeyword {
			if stops != nil && stops[t0.text] {
				return list, t0.text, nil
			}
			p.i++ // consume this action segment
			n, err := p.parseKeyword(toks)
			if err != nil {
				return nil, "", err
			}
			if n != nil {
				list = append(list, n)
			}
			continue
		}
		// Ordinary expression / assignment statement.
		p.i++
		n, err := p.parseStatement(toks)
		if err != nil {
			return nil, "", err
		}
		list = append(list, n)
	}
	return list, "", nil
}

func (p *parser) parseKeyword(toks []token) (node, error) {
	kw := toks[0]
	// Position of this action.
	line, col := kw.line, kw.col
	switch kw.text {
	case "if", "for", "with":
		return p.parseCtl(kw.text, toks)
	case "else", "end":
		return nil, p.errorf(line, col, "unexpected %s", kw.text)
	case "define":
		if len(toks) < 2 || toks[1].kind != tkString {
			return nil, p.errorf(line, col, "define requires a quoted template name")
		}
		if len(toks) > 2 {
			return nil, p.errorf(line, col, "unexpected tokens after define name")
		}
		name := toks[1].val.(string)
		if err := p.beginDef(name, Pos{toks[1].line, toks[1].col}); err != nil {
			return nil, err
		}
		return nil, nil
	case "block":
		return p.parseBlockCmd(toks)
	case "template":
		return p.parseTemplateCmd(toks)
	case "break":
		if len(toks) > 1 {
			return nil, p.errorf(line, col, "unexpected tokens after break")
		}
		if p.breakDepth == 0 {
			return nil, p.errorf(line, col, "unexpected break outside for")
		}
		return nodeBreak{pos: Pos{line, col}}, nil
	case "continue":
		if len(toks) > 1 {
			return nil, p.errorf(line, col, "unexpected tokens after continue")
		}
		if p.breakDepth == 0 {
			return nil, p.errorf(line, col, "unexpected continue outside for")
		}
		return nodeContinue{pos: Pos{line, col}}, nil
	default:
		return nil, p.errorf(line, col, "unexpected keyword %q", kw.text)
	}
}

// parseStatement parses an ordinary action body: an expression (printed) or a
// variable declaration/assignment.
func (p *parser) parseStatement(toks []token) (node, error) {
	first := toks[0]
	// $x := ... / $x = ...
	if first.kind == tkDollar && len(toks) >= 3 && toks[1].kind == tkIdent {
		op := toks[2]
		if op.kind == tkOp && (op.text == ":=" || op.text == "=") {
			name := "$" + toks[1].text
			if name == "$" {
				return nil, p.errorf(op.line, op.col, "cannot reassign $")
			}
			x, used, err := p.expr(toks, 3)
			if err != nil {
				return nil, err
			}
			if used != len(toks) {
				return nil, p.errorf(toks[used].line, toks[used].col, "unexpected token %q", toks[used].text)
			}
			return nodeAssign{pos: Pos{first.line, first.col}, name: name, isDecl: op.text == ":=", val: x}, nil
		}
	}
	x, used, err := p.expr(toks, 0)
	if err != nil {
		return nil, err
	}
	if used != len(toks) {
		return nil, p.errorf(toks[used].line, toks[used].col, "unexpected token %q", toks[used].text)
	}
	return outputNode{pos: Pos{first.line, first.col}, x: x}, nil
}

// ctlHead describes the optional variable header of an if/for/with action.
type ctlHead struct {
	vars []string // "$x", "$y", ...
	val  expr
}

// parseCtl handles "if cond", "for $i, $v := xs", "with $v := x".
func (p *parser) parseCtl(kw string, toks []token) (node, error) {
	head, used, err := p.parseCtlHead(toks) // used is relative to toks[1:]
	if err != nil {
		return nil, err
	}
	if used != len(toks)-1 {
		pos := Pos{}
		if len(toks) > 0 {
			pos = Pos{toks[len(toks)-1].line, toks[len(toks)-1].col}
		}
		return nil, p.errorf(pos.Line, pos.Col, "unexpected token %q", toks[used].text)
	}
	pos := Pos{toks[0].line, toks[0].col}
	switch kw {
	case "if":
		if len(head.vars) > 1 {
			return nil, p.errorf(toks[0].line, toks[0].col, "if accepts at most one variable")
		}
		n := &nodeIf{pos: pos, cond: head.val}
		if len(head.vars) == 1 {
			n.cond = &assignWrap{name: head.vars[0], val: head.val, pos: pos}
		}
		if err := p.fillCtl(n); err != nil {
			return nil, err
		}
		return n, nil
	case "with":
		if len(head.vars) > 1 {
			return nil, p.errorf(toks[0].line, toks[0].col, "with accepts at most one variable")
		}
		n := &nodeWith{pos: pos, x: head.val}
		if len(head.vars) == 1 {
			n.varName = head.vars[0]
		}
		if err := p.fillCtl(n); err != nil {
			return nil, err
		}
		return n, nil
	default: // for
		if len(head.vars) > 2 {
			return nil, p.errorf(toks[0].line, toks[0].col, "for accepts at most two variables")
		}
		n := &nodeFor{pos: pos, x: head.val, vars: head.vars}
		p.breakDepth++
		err := p.fillCtl(n)
		p.breakDepth--
		if err != nil {
			return nil, err
		}
		return n, nil
	}
}

// ctlBody is the interface shared by nodeIf/nodeWith/nodeFor so they can be
// filled with then/else bodies uniformly.
type ctlBody interface {
	thenBody() []node
	setThen([]node)
	elseBody() []node
	setElse([]node)
}

func (n *nodeIf) thenBody() []node   { return n.then }
func (n *nodeIf) setThen(b []node)   { n.then = b }
func (n *nodeIf) elseBody() []node   { return n.els }
func (n *nodeIf) setElse(b []node)   { n.els = b }
func (n *nodeWith) thenBody() []node { return n.body }
func (n *nodeWith) setThen(b []node) { n.body = b }
func (n *nodeWith) elseBody() []node { return n.els }
func (n *nodeWith) setElse(b []node) { n.els = b }
func (n *nodeFor) thenBody() []node  { return n.body }
func (n *nodeFor) setThen(b []node)  { n.body = b }
func (n *nodeFor) elseBody() []node  { return n.els }
func (n *nodeFor) setElse(b []node)  { n.els = b }

// assignWrap turns "if $x := expr" into a conditional whose body also defines
// $x. It is replaced during execution by evaluating and declaring the var.
type assignWrap struct {
	pos  Pos
	name string
	val  expr
}

func (a *assignWrap) epos() Pos { return a.pos }

var _ expr = (*assignWrap)(nil)

// fillCtl reads the then/else bodies that follow the opening action.
func (p *parser) fillCtl(n ctlBody) error {
	then, stop, err := p.parseBlock(map[string]bool{"else": true, "end": true})
	if err != nil {
		return err
	}
	n.setThen(then)
	switch stop {
	case "":
		return p.errorf(0, 0, "unexpected end of template inside %s block", "control")
	case "end":
		p.i++ // consume {{end}}
		return nil
	case "else":
		// consume {{else ...}}
		s := p.segs[p.i]
		p.i++
		toks, err := lexSub(s.text, Pos{s.line, s.col + p.leftLen})
		if err != nil {
			return p.errorf(s.line, s.col, "%v", err)
		}
		if len(toks) == 0 {
			return p.errorf(s.line, s.col, "empty else")
		}
		// {{else if ...}}: shared single {{end}} chain. The re-lexed tokens
		// still start with the "else" keyword (seg.text keeps it), so skip both.
		if len(toks) >= 2 && toks[0].kind == tkKeyword && toks[0].text == "else" &&
			toks[1].kind == tkKeyword && toks[1].text == "if" {
			inner, err := p.parseCtlFromTokens(toks[2:])
			if err != nil {
				return err
			}
			n.setElse([]node{inner})
			return nil
		}
		if len(toks) > 1 {
			return p.errorf(toks[1].line, toks[1].col, "unexpected tokens after else")
		}
		els, stop2, err := p.parseBlock(map[string]bool{"end": true})
		if err != nil {
			return err
		}
		if stop2 != "end" {
			return p.errorf(0, 0, "unexpected end of template inside else block")
		}
		p.i++
		n.setElse(els)
		return nil
	}
	return nil
}

// parseCtlFromTokens parses a control flow construct whose leading keyword has
// already been consumed (toks begin after "if"/"for"/"with"). Used for
// {{else if ...}}.
func (p *parser) parseCtlFromTokens(toks []token) (node, error) {
	head, used, err := p.parseCtlHead2(toks)
	if err != nil {
		return nil, err
	}
	if used != len(toks) {
		return nil, p.errorf(toks[used].line, toks[used].col, "unexpected token %q", toks[used].text)
	}
	pos := Pos{toks[0].line, toks[0].col}
	n := &nodeIf{pos: pos, cond: head.val}
	if len(head.vars) == 1 {
		n.cond = &assignWrap{name: head.vars[0], val: head.val, pos: pos}
	}
	if err := p.fillCtl(n); err != nil {
		return nil, err
	}
	return n, nil
}

// parseCtlHead parses tokens starting with the control keyword and returns the
// header (optional variable list with ":=" plus the controlling expression).
func (p *parser) parseCtlHead(toks []token) (*ctlHead, int, error) {
	return p.parseCtlHead2(toks[1:])
}

func (p *parser) parseCtlHead2(toks []token) (*ctlHead, int, error) {
	i := 0
	var vars []string
	if i+1 < len(toks) && toks[i].kind == tkDollar && toks[i+1].kind == tkIdent {
		// try to read variable list: $a or $a, $b followed by :=
		j := i
		for {
			if j+1 >= len(toks) || toks[j].kind != tkDollar || toks[j+1].kind != tkIdent {
				break
			}
			vars = append(vars, "$"+toks[j+1].text)
			j += 2
			if j < len(toks) && toks[j].kind == tkComma {
				j++
				continue
			}
			break
		}
		if j < len(toks) && toks[j].kind == tkOp && toks[j].text == ":=" {
			// re-collect without the comma between vars? We already appended
			// in order including after comma loop; names come one at a time.
			// (vars) is correct.
			x, used, err := p.expr(toks, j+1)
			if err != nil {
				return nil, 0, err
			}
			return &ctlHead{vars: vars, val: x}, used, nil
		}
		vars = nil
	}
	x, used, err := p.expr(toks, i)
	if err != nil {
		return nil, 0, err
	}
	return &ctlHead{val: x}, used, nil
}

func (p *parser) parseBlockCmd(toks []token) (node, error) {
	if len(toks) < 2 || toks[1].kind != tkString {
		return nil, p.errorf(toks[0].line, toks[0].col, "block requires a quoted template name")
	}
	name := toks[1].val.(string)
	var arg expr
	var used int
	var err error
	if len(toks) > 2 {
		arg, used, err = p.expr(toks, 2)
		if err != nil {
			return nil, err
		}
		if used != len(toks) {
			return nil, p.errorf(toks[used].line, toks[used].col, "unexpected token %q", toks[used].text)
		}
	}
	body, stop, err := p.parseBlock(map[string]bool{"end": true})
	if err != nil {
		return nil, err
	}
	if stop != "end" {
		return nil, p.errorf(0, 0, "unexpected end of template inside block %q", name)
	}
	p.i++
	if err := p.registerDef(name, Pos{toks[1].line, toks[1].col}, body); err != nil {
		return nil, err
	}
	return nodeTemplate{pos: Pos{toks[0].line, toks[0].col}, name: name, arg: arg}, nil
}

func (p *parser) parseTemplateCmd(toks []token) (node, error) {
	if len(toks) < 2 || toks[1].kind != tkString {
		return nil, p.errorf(toks[0].line, toks[0].col, "template requires a quoted template name")
	}
	name := toks[1].val.(string)
	var arg expr
	if len(toks) > 2 {
		x, used, err := p.expr(toks, 2)
		if err != nil {
			return nil, err
		}
		if used != len(toks) {
			return nil, p.errorf(toks[used].line, toks[used].col, "unexpected token %q", toks[used].text)
		}
		arg = x
	}
	return nodeTemplate{pos: Pos{toks[0].line, toks[0].col}, name: name, arg: arg}, nil
}

// beginDef starts collecting a define body (tokens already consumed).
func (p *parser) beginDef(name string, pos Pos) error {
	body, stop, err := p.parseBlock(map[string]bool{"end": true})
	if err != nil {
		return err
	}
	if stop != "end" {
		return p.errorf(0, 0, "unexpected end of template inside define %q", name)
	}
	p.i++
	return p.registerDef(name, pos, body)
}

func (p *parser) registerDef(name string, pos Pos, body []node) error {
	if _, ok := p.defs[name]; ok {
		return p.errorf(pos.Line, pos.Col, "template %q redefined", name)
	}
	p.defs[name] = &templateDef{pos: pos, nodes: body}
	return nil
}

// ---------------------------------------------------------------------------
// Expression parsing (recursive descent, C-style precedence).
// ---------------------------------------------------------------------------

var binPrec = map[string]int{
	"||": 10,
	"&&": 15,
	"|":  20,
	"^":  25,
	"&":  28,
	"==": 30, "!=": 30,
	"<": 35, "<=": 35, ">": 35, ">=": 35,
	"<<": 40, ">>": 40,
	"+": 50, "-": 50,
	"*": 60, "/": 60, "%": 60,
}

var boolLits = map[string]any{"true": true, "false": false, "nil": nil}

func (p *parser) expr(toks []token, i int) (expr, int, error) {
	left, i, err := p.binary(toks, i, 1)
	if err != nil {
		return nil, 0, err
	}
	if i < len(toks) && toks[i].kind == tkOp && toks[i].text == "?" {
		q := toks[i]
		i++
		t, i2, err := p.expr(toks, i)
		if err != nil {
			return nil, 0, err
		}
		if i2 >= len(toks) || toks[i2].kind != tkOp || toks[i2].text != ":" {
			return nil, 0, p.errorf(q.line, q.col, "expected ':' in conditional expression")
		}
		i2++
		f, i3, err := p.expr(toks, i2)
		if err != nil {
			return nil, 0, err
		}
		return ternaryExpr{pos: Pos{q.line, q.col}, c: left, t: t, f: f}, i3, nil
	}
	return left, i, nil
}

func (p *parser) binary(toks []token, i, minPrec int) (expr, int, error) {
	left, i, err := p.unary(toks, i)
	if err != nil {
		return nil, 0, err
	}
	for i < len(toks) {
		t := toks[i]
		if t.kind != tkOp {
			break
		}
		prec, ok := binPrec[t.text]
		if !ok || prec < minPrec {
			break
		}
		i++
		right, i2, err := p.binary(toks, i, prec+1)
		if err != nil {
			return nil, 0, err
		}
		left = binaryExpr{pos: Pos{t.line, t.col}, op: t.text, x: left, y: right}
		i = i2
	}
	return left, i, nil
}

func (p *parser) unary(toks []token, i int) (expr, int, error) {
	if i >= len(toks) {
		return nil, 0, p.errorf(0, 0, "unexpected end of expression")
	}
	t := toks[i]
	if t.kind == tkOp {
		switch t.text {
		case "!", "~", "-", "+":
			x, i2, err := p.unary(toks, i+1)
			if err != nil {
				return nil, 0, err
			}
			return unaryExpr{pos: Pos{t.line, t.col}, op: t.text, x: x}, i2, nil
		}
	}
	return p.postfix(toks, i)
}

func (p *parser) postfix(toks []token, i int) (expr, int, error) {
	base, i, err := p.primary(toks, i)
	if err != nil {
		return nil, 0, err
	}
	var ops []chainOp
	for i < len(toks) {
		t := toks[i]
		switch {
		case t.kind == tkDot:
			if i+1 >= len(toks) || toks[i+1].kind != tkIdent {
				return nil, 0, p.errorf(t.line, t.col, "expected field name after '.'")
			}
			name := toks[i+1]
			pos := Pos{t.line, t.col}
			i += 2
			if i < len(toks) && toks[i].kind == tkOp && toks[i].text == "(" {
				args, i2, err := p.parseArgs(toks, i+1)
				if err != nil {
					return nil, 0, err
				}
				ops = append(ops, chainOp{pos: pos, kind: opCall, name: name.text, args: args})
				i = i2
			} else {
				ops = append(ops, chainOp{pos: pos, kind: opField, name: name.text})
			}
		case t.kind == tkOp && t.text == "[":
			idx, i2, err := p.expr(toks, i+1)
			if err != nil {
				return nil, 0, err
			}
			if i2 >= len(toks) || toks[i2].kind != tkOp || toks[i2].text != "]" {
				return nil, 0, p.errorf(t.line, t.col, "expected ']'")
			}
			ops = append(ops, chainOp{pos: Pos{t.line, t.col}, kind: opIndex, idx: idx})
			i = i2 + 1
		case t.kind == tkOp && t.text == "(":
			args, i2, err := p.parseArgs(toks, i+1)
			if err != nil {
				return nil, 0, err
			}
			ops = append(ops, chainOp{pos: Pos{t.line, t.col}, kind: opCall, args: args})
			i = i2
		default:
			goto done
		}
	}
done:
	if len(ops) == 0 {
		return base, i, nil
	}
	return chainExpr{pos: base.epos(), base: base, ops: ops}, i, nil
}

func (p *parser) parseArgs(toks []token, i int) ([]expr, int, error) {
	if i < len(toks) && toks[i].kind == tkOp && toks[i].text == ")" {
		return nil, i + 1, nil
	}
	var args []expr
	for {
		a, i2, err := p.expr(toks, i)
		if err != nil {
			return nil, 0, err
		}
		args = append(args, a)
		i = i2
		if i < len(toks) && toks[i].kind == tkComma {
			i++
			continue
		}
		break
	}
	if i >= len(toks) || toks[i].kind != tkOp || toks[i].text != ")" {
		return nil, 0, p.errorf(i, 0, "expected ')'")
	}
	_ = i
	return args, i + 1, nil
}

func (p *parser) primary(toks []token, i int) (expr, int, error) {
	if i >= len(toks) {
		return nil, 0, p.errorf(0, 0, "unexpected end of expression")
	}
	t := toks[i]
	switch t.kind {
	case tkNumber:
		i++
		return literalExpr{pos: Pos{t.line, t.col}, val: t.val}, i, nil
	case tkString, tkChar:
		i++
		return literalExpr{pos: Pos{t.line, t.col}, val: t.val}, i, nil
	case tkKeyword:
		return nil, 0, p.errorf(t.line, t.col, "unexpected keyword %q", t.text)
	case tkDollar:
		pos := Pos{t.line, t.col}
		name := "$"
		i++
		if i < len(toks) && toks[i].kind == tkIdent {
			name = "$" + toks[i].text
			i++
		}
		return varRefExpr{pos: pos, name: name}, i, nil
	case tkDot:
		dpos := Pos{t.line, t.col}
		i++
		// A dot immediately followed by a field or a method (.Name, .a.b[0],
		// .Double(x)) is folded here into a chain rooted at the current dot so
		// that postfix can keep appending further selectors.
		if i < len(toks) && toks[i].kind == tkIdent {
			fname := toks[i]
			i++
			var ops []chainOp
			if i < len(toks) && toks[i].kind == tkOp && toks[i].text == "(" {
				args, i2, err := p.parseArgs(toks, i+1)
				if err != nil {
					return nil, 0, err
				}
				ops = append(ops, chainOp{pos: dpos, kind: opCall, name: fname.text, args: args})
				i = i2
			} else {
				ops = append(ops, chainOp{pos: dpos, kind: opField, name: fname.text})
			}
			return chainExpr{pos: dpos, base: dotExpr{pos: dpos}, ops: ops}, i, nil
		}
		return dotExpr{pos: dpos}, i, nil
	case tkOp:
		if t.text == "(" {
			x, i2, err := p.expr(toks, i+1)
			if err != nil {
				return nil, 0, err
			}
			if i2 >= len(toks) || toks[i2].kind != tkOp || toks[i2].text != ")" {
				return nil, 0, p.errorf(t.line, t.col, "expected ')'")
			}
			return x, i2 + 1, nil
		}
		return nil, 0, p.errorf(t.line, t.col, "unexpected operator %q", t.text)
	case tkIdent:
		if lit, ok := boolLits[t.text]; ok {
			i++
			return literalExpr{pos: Pos{t.line, t.col}, val: lit}, i, nil
		}
		// function call name(...)
		if i+1 < len(toks) && toks[i+1].kind == tkOp && toks[i+1].text == "(" {
			if p.funcs != nil && !p.funcs[t.text] &&
				!(p.caseInsensitive && p.funcs[strings.ToLower(t.text)]) &&
				!isBuiltinPseudo(t.text) {
				return nil, 0, p.errorf(t.line, t.col, "function %q not defined", t.text)
			}
			args, i2, err := p.parseArgs(toks, i+2)
			if err != nil {
				return nil, 0, err
			}
			return fnCallExpr{pos: Pos{t.line, t.col}, name: t.text, args: args}, i2, nil
		}
		return nil, 0, p.errorf(t.line, t.col, "unexpected identifier %q (functions must be called: %s(...))", t.text, t.text)
	default:
		return nil, 0, p.errorf(t.line, t.col, "unexpected token %q", t.text)
	}
}
