package ctpl

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

const maxExecDepth = 10000

// frame is a lexical scope of variables.
type frame struct {
	vars   map[string]reflect.Value
	parent *frame
}

func newFrame(parent *frame) *frame {
	return &frame{vars: map[string]reflect.Value{}, parent: parent}
}

func (f *frame) lookup(name string) (reflect.Value, bool) {
	for cur := f; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v, true
		}
	}
	return reflect.Value{}, false
}

// assign writes name; decl controls whether a new binding is created.
func (f *frame) assign(name string, decl bool, v reflect.Value) error {
	if decl {
		f.vars[name] = v
		return nil
	}
	for cur := f; cur != nil; cur = cur.parent {
		if _, ok := cur.vars[name]; ok {
			cur.vars[name] = v
			return nil
		}
	}
	return fmt.Errorf("undefined variable %s", name)
}

// sentinel error values carry break/continue through the node walker.
var errBreak = fmt.Errorf("break")
var errContinue = fmt.Errorf("continue")

type execContext struct {
	out         io.Writer
	funcs       map[string]reflect.Value
	root        reflect.Value
	vars        *frame
	tplName     string
	depth       int
	tmpl        *Template
	escapeQuote bool // doubles every ' in substituted string values when true

	// caseInsensitive mirrors the template group's case-insensitive function
	// lookup flag; when true, fnCallExpr lookup folds names to lower-case.
	caseInsensitive bool

	// env is the shared environment map; when non-nil, the pseudo-function
	// env("KEY") reads from it.
	env map[string]any
}

func (e *execContext) errf(pos Pos, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if pos.Line > 0 {
		return fmt.Errorf("template: %s:%s: %s", e.tplName, pos, msg)
	}
	return fmt.Errorf("template: %s: %s", e.tplName, msg)
}

func (e *execContext) pushFrame() *frame {
	e.vars = newFrame(e.vars)
	return e.vars
}

func (e *execContext) popFrame() {
	e.vars = e.vars.parent
}

// run executes a statement list with dot set to dot.
func (e *execContext) run(list []node, dot reflect.Value) error {
	for _, n := range list {
		if err := e.runNode(n, dot); err != nil {
			return err
		}
	}
	return nil
}

func (e *execContext) runNode(n node, dot reflect.Value) error {
	switch n := n.(type) {
	case textNode:
		_, err := io.WriteString(e.out, n.text)
		return err
	case outputNode:
		v, err := e.eval(n.x, dot)
		if err != nil {
			return e.errf(n.x.epos(), "%v", err)
		}
		return e.writeValue(v, !e.isFuncCall(n.x))
	case nodeAssign:
		v, err := e.eval(n.val, dot)
		if err != nil {
			return e.errf(n.val.epos(), "%v", err)
		}
		if err := e.vars.assign(n.name, n.isDecl, v); err != nil {
			return e.errf(n.pos, "%v", err)
		}
		return nil
	case nodeBreak:
		return errBreak
	case nodeContinue:
		return errContinue
	case *nodeIf:
		return e.runIf(n, dot)
	case *nodeWith:
		return e.runWith(n, dot)
	case *nodeFor:
		return e.runFor(n, dot)
	case nodeTemplate:
		return e.runTemplate(n, dot)
	default:
		return e.errf(n.npos(), "cannot execute %T", n)
	}
}

func (e *execContext) runIf(n *nodeIf, dot reflect.Value) error {
	e.pushFrame()
	defer e.popFrame()
	cond := n.cond
	condVal, err := e.eval(cond, dot)
	if err != nil {
		return e.errf(cond.epos(), "%v", err)
	}
	// If $x := ... declares a variable before the test.
	if aw, ok := cond.(*assignWrap); ok {
		if err := e.vars.assign(aw.name, true, condVal); err != nil {
			return e.errf(aw.pos, "%v", err)
		}
	}
	ok, err := isTrue(condVal)
	if err != nil {
		return e.errf(cond.epos(), "%v", err)
	}
	if ok {
		return e.run(n.then, dot)
	}
	return e.run(n.els, dot)
}

func (e *execContext) runWith(n *nodeWith, dot reflect.Value) error {
	v, err := e.eval(n.x, dot)
	if err != nil {
		return e.errf(n.x.epos(), "%v", err)
	}
	ok, err := isTrue(v)
	if err != nil {
		return e.errf(n.x.epos(), "%v", err)
	}
	e.pushFrame()
	defer e.popFrame()
	if n.varName != "" {
		if err := e.vars.assign(n.varName, true, v); err != nil {
			return e.errf(n.pos, "%v", err)
		}
	}
	if ok {
		return e.run(n.body, v)
	}
	return e.run(n.els, dot)
}

func (e *execContext) runFor(n *nodeFor, dot reflect.Value) error {
	v, err := e.eval(n.x, dot)
	if err != nil {
		return e.errf(n.x.epos(), "%v", err)
	}
	v = unwrapInterface(v)
	if v.IsValid() && v.Kind() == reflect.Ptr && !v.IsNil() {
		v = v.Elem()
	}

	// Not iterable: only the else branch may run.
	if !isIterable(v) {
		if len(n.els) > 0 {
			e.pushFrame()
			defer e.popFrame()
			return e.run(n.els, dot)
		}
		return e.errf(n.pos, "for can't iterate over %s", typeName(v))
	}

	e.pushFrame()
	defer e.popFrame()

	runBody := func(key, elem reflect.Value) error {
		switch len(n.vars) {
		case 2:
			if err := e.vars.assign(n.vars[0], true, key); err != nil {
				return e.errf(n.pos, "%v", err)
			}
			if err := e.vars.assign(n.vars[1], true, elem); err != nil {
				return e.errf(n.pos, "%v", err)
			}
		case 1:
			if err := e.vars.assign(n.vars[0], true, elem); err != nil {
				return e.errf(n.pos, "%v", err)
			}
		}
		return e.run(n.body, elem)
	}

	var count int
	var fail error
	// step runs one iteration; it returns false when the loop must stop.
	step := func(key, elem reflect.Value) bool {
		count++
		err := runBody(key, elem)
		if err == errBreak {
			return false
		}
		if err == errContinue {
			return true
		}
		if err != nil {
			fail = err
			return false
		}
		return true
	}

	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if !step(reflect.ValueOf(int64(i)), v.Index(i)) {
				break
			}
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(a, b int) bool {
			av, bv := valueToAny(keys[a]), valueToAny(keys[b])
			return fmt.Sprint(av) < fmt.Sprint(bv)
		})
		for _, k := range keys {
			if !step(k, v.MapIndex(k)) {
				break
			}
		}
	case reflect.Chan:
		idx := 0
		for {
			item, ok := v.Recv()
			if !ok {
				break
			}
			if !step(reflect.ValueOf(int64(idx)), item) {
				break
			}
			idx++
		}
	}
	if fail != nil {
		return fail
	}
	if count == 0 && len(n.els) > 0 {
		return e.run(n.els, dot)
	}
	return nil
}

func isIterable(v reflect.Value) bool {
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.Chan:
		return true
	}
	return false
}

func (e *execContext) runTemplate(n nodeTemplate, dot reflect.Value) error {
	if e.depth >= maxExecDepth {
		return e.errf(n.pos, "exceeded maximum template depth (%d)", maxExecDepth)
	}
	target := e.tmpl.assoc[n.name]
	if target == nil {
		return e.errf(n.pos, "template %q not defined", n.name)
	}
	ndot := dot
	if n.arg != nil {
		v, err := e.eval(n.arg, dot)
		if err != nil {
			return e.errf(n.arg.epos(), "%v", err)
		}
		ndot = v
	}
	sub := &execContext{
		out:             e.out,
		funcs:           e.funcs,
		root:            e.root,
		vars:            newFrame(nil),
		tplName:         target.name,
		depth:           e.depth + 1,
		tmpl:            e.tmpl,
		escapeQuote:     e.escapeQuote,
		caseInsensitive: e.caseInsensitive,
		env:             e.env,
	}
	if err := sub.run(target.nodes, ndot); err != nil {
		if err == errBreak || err == errContinue {
			return e.errf(n.pos, "break/continue must be used inside for")
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Expression evaluation.
// ---------------------------------------------------------------------------

func (e *execContext) eval(x expr, dot reflect.Value) (reflect.Value, error) {
	switch x := x.(type) {
	case literalExpr:
		return reflect.ValueOf(x.val), nil
	case dotExpr:
		return dot, nil
	case varRefExpr:
		if x.name == "$" {
			return e.root, nil
		}
		if v, ok := e.vars.lookup(x.name); ok {
			return v, nil
		}
		return reflect.Value{}, fmt.Errorf("undefined variable %s", x.name)
	case fnCallExpr:
		key := x.name
		if e.caseInsensitive {
			key = strings.ToLower(key)
		}
		fn, ok := e.funcs[key]
		if !ok {
			// Pseudo-function env("KEY"): a user-registered function with
			// the same name takes precedence (it lives in e.funcs and is
			// resolved by the lookup above). When none is registered we
			// route to evalEnv, which surfaces a runtime error if no env
			// map was configured on the template association group.
			if key == "env" {
				return e.evalEnv(x.args, dot)
			}
			return reflect.Value{}, fmt.Errorf("function %q not defined", x.name)
		}
		args, err := e.evalArgs(x.args, dot)
		if err != nil {
			return reflect.Value{}, err
		}
		return callFunction(e, x.name, fn, args, true)
	case chainExpr:
		return e.evalChain(x, dot)
	case unaryExpr:
		return e.evalUnary(x, dot)
	case binaryExpr:
		return e.evalBinary(x, dot)
	case ternaryExpr:
		cond, err := e.eval(x.c, dot)
		if err != nil {
			return reflect.Value{}, err
		}
		b, err := isTrue(cond)
		if err != nil {
			return reflect.Value{}, err
		}
		if b {
			return e.eval(x.t, dot)
		}
		return e.eval(x.f, dot)
	case *assignWrap:
		v, err := e.eval(x.val, dot)
		if err != nil {
			return reflect.Value{}, err
		}
		if err := e.vars.assign(x.name, true, v); err != nil {
			return reflect.Value{}, err
		}
		return v, nil
	default:
		return reflect.Value{}, fmt.Errorf("cannot evaluate %T", x)
	}
}

func (e *execContext) evalArgs(args []expr, dot reflect.Value) ([]reflect.Value, error) {
	out := make([]reflect.Value, 0, len(args))
	for _, a := range args {
		v, err := e.eval(a, dot)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// evalEnv implements the pseudo-function env("KEY") that reads a value from
// the shared environment map configured on the template association group.
// The argument must evaluate to a string-like value. When the key is absent
// (or its value is nil) the call returns a typed nil, which renders as
// "<no value>" rather than producing an Invalid reflect.Value that would
// break downstream operations.
func (e *execContext) evalEnv(args []expr, dot reflect.Value) (reflect.Value, error) {
	if e.env == nil {
		return reflect.Value{}, fmt.Errorf("env: no environment configured")
	}
	rargs, err := e.evalArgs(args, dot)
	if err != nil {
		return reflect.Value{}, err
	}
	if len(rargs) != 1 {
		return reflect.Value{}, fmt.Errorf("env: expects 1 argument, got %d", len(rargs))
	}
	cv, err := convertTo(reflect.TypeOf(""), rargs[0])
	if err != nil {
		return reflect.Value{}, fmt.Errorf("env: %v", err)
	}
	name := cv.String()
	v, ok := e.env[name]
	if !ok || v == nil {
		var z any
		return reflect.ValueOf(&z).Elem(), nil
	}
	return reflect.ValueOf(v), nil
}

func (e *execContext) evalChain(x chainExpr, dot reflect.Value) (reflect.Value, error) {
	cur, err := e.eval(x.base, dot)
	if err != nil {
		return reflect.Value{}, err
	}
	for _, op := range x.ops {
		switch op.kind {
		case opField:
			cur, err = evalField(cur, op.name)
			if err != nil {
				return reflect.Value{}, e.ctxError(op.pos, "%v", err)
			}
		case opIndex:
			key, err := e.eval(op.idx, dot)
			if err != nil {
				return reflect.Value{}, err
			}
			cur, err = evalIndex(cur, key)
			if err != nil {
				return reflect.Value{}, e.ctxError(op.pos, "%v", err)
			}
		case opCall:
			callee := cur
			if op.name != "" {
				c, err := evalField(cur, op.name)
				if err != nil {
					return reflect.Value{}, e.ctxError(op.pos, "%v", err)
				}
				callee = c
			}
			if !callee.IsValid() || callee.Kind() != reflect.Func {
				return reflect.Value{}, e.ctxError(op.pos, "value of type %s is not callable", typeName(callee))
			}
			args, err := e.evalArgs(op.args, dot)
			if err != nil {
				return reflect.Value{}, err
			}
			name := op.name
			if name == "" {
				name = "<call>"
			}
			cur, err = callFunction(e, name, callee, args, false)
			if err != nil {
				return reflect.Value{}, e.ctxError(op.pos, "%v", err)
			}
		}
	}
	return cur, nil
}

func (e *execContext) ctxError(pos Pos, format string, args ...any) error {
	return e.errf(pos, format, args...)
}

func (e *execContext) evalUnary(x unaryExpr, dot reflect.Value) (reflect.Value, error) {
	v, err := e.eval(x.x, dot)
	if err != nil {
		return reflect.Value{}, err
	}
	switch x.op {
	case "!":
		b, err := isTrue(v)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(!b), nil
	case "-":
		_, _, f, k, ok := loadNum(v)
		if !ok {
			return reflect.Value{}, fmt.Errorf("invalid operand type for unary -: %s", typeName(v))
		}
		if k == nkFloat {
			return reflect.ValueOf(-f), nil
		}
		if k == nkUint {
			u, err := numAsUint64(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(-int64(u)), nil
		}
		i, err := numAsInt64(v)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(-i), nil
	case "+":
		return v, nil
	case "~":
		if i, ok := indexInt(v); ok {
			return reflect.ValueOf(^i), nil
		}
		return reflect.Value{}, fmt.Errorf("invalid operand type for ~: %s", typeName(v))
	}
	return reflect.Value{}, fmt.Errorf("unknown unary operator %q", x.op)
}

func (e *execContext) evalBinary(x binaryExpr, dot reflect.Value) (reflect.Value, error) {
	lv, err := e.eval(x.x, dot)
	if err != nil {
		return reflect.Value{}, err
	}
	// Short-circuit logical operators.
	if x.op == "&&" {
		b, err := isTrue(lv)
		if err != nil {
			return reflect.Value{}, err
		}
		if !b {
			return reflect.ValueOf(false), nil
		}
		rv, err := e.eval(x.y, dot)
		if err != nil {
			return reflect.Value{}, err
		}
		rb, err := isTrue(rv)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(rb), nil
	}
	if x.op == "||" {
		b, err := isTrue(lv)
		if err != nil {
			return reflect.Value{}, err
		}
		if b {
			return reflect.ValueOf(true), nil
		}
		rv, err := e.eval(x.y, dot)
		if err != nil {
			return reflect.Value{}, err
		}
		rb, err := isTrue(rv)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(rb), nil
	}
	rv, err := e.eval(x.y, dot)
	if err != nil {
		return reflect.Value{}, err
	}
	switch x.op {
	case "==", "!=":
		res := equalValues(lv, rv)
		if x.op == "!=" {
			res = !res
		}
		return reflect.ValueOf(res), nil
	case "<", "<=", ">", ">=":
		c, ok := compare(lv, rv)
		if !ok {
			return reflect.Value{}, fmt.Errorf("incompatible types for %s: %s and %s",
				x.op, typeName(lv), typeName(rv))
		}
		switch x.op {
		case "<":
			return reflect.ValueOf(c < 0), nil
		case "<=":
			return reflect.ValueOf(c <= 0), nil
		case ">":
			return reflect.ValueOf(c > 0), nil
		default:
			return reflect.ValueOf(c >= 0), nil
		}
	default:
		return evalArith(x.op, lv, rv)
	}
}

// ---------------------------------------------------------------------------
// Writing values.
// ---------------------------------------------------------------------------

// isFuncCall reports whether x is a function call expression, i.e. a call by
// name resolved against the template function table: {{ name(args...) }}.
// That covers built-ins, functions registered through Template.Funcs and the
// env pseudo-function. Values they return are written verbatim and therefore
// bypass the escapeQuote doubling; everything else (fields, variables, index
// expressions, method calls such as {{ .M.String() }}, literals ...) stays
// subject to it.
func (e *execContext) isFuncCall(x expr) bool {
	_, ok := x.(fnCallExpr)
	return ok
}

func (e *execContext) writeValue(v reflect.Value, escape bool) error {
	if !v.IsValid() {
		_, err := io.WriteString(e.out, "<no value>")
		return err
	}
	if v.Kind() == reflect.Interface && v.IsNil() {
		_, err := io.WriteString(e.out, "<no value>")
		return err
	}
	v = unwrapInterface(v)
	if !v.IsValid() {
		_, err := io.WriteString(e.out, "<no value>")
		return err
	}
	// When the quote switch is on, double every single quote found in the
	// substituted string so the value stays valid inside a SQL literal.
	// Results of user-registered functions are exempt: they are treated as
	// caller-authored raw text (see Template.SetEscapeQuote).
	if escape && e.escapeQuote && v.Kind() == reflect.String {
		_, err := io.WriteString(e.out, strings.ReplaceAll(v.String(), "'", "''"))
		return err
	}
	_, err := fmt.Fprint(e.out, valueToAny(v))
	return err
}
