package ctpl

import (
	"fmt"
	"html"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// FuncMap maps function names to Go functions usable in templates. This
// mirrors the standard text/template FuncMap type.
type FuncMap map[string]any

// Env is the type of the template environment map (see Template.SetEnv). A
// user function that declares a parameter of this exact type receives the
// current environment automatically whenever it is invoked by name directly
// from a template (e.g. {{ myfunc(...) }}). Such a parameter is filled by the
// engine and does NOT consume an argument from the template call, so the
// function body can read environment values directly:
//
//	tm.SetEnv(map[string]any{"Prefix": "Hello, "})
//	tm.Funcs(FuncMap{"greet": func(env ctpl.Env, name string) string {
//		return env["Prefix"].(string) + name
//	}})
//	// {{ greet("world") }} => "Hello, world"
//
// Only the distinct named type Env opts in to injection; a parameter typed
// map[string]any is treated as an ordinary argument, so passing maps by value
// keeps working as before. Injection applies to direct function calls only;
// method calls on data values ({{ .Method(...) }}) never inject.
type Env map[string]any

var reflectTypeEnv = reflect.TypeOf(Env(nil))

// callFunction invokes a reflect function with converted arguments and applies
// the (value, error) convention. When injectEnv is true, any parameter whose
// static type is exactly Env is filled from e's environment instead of
// consuming a template argument.
func callFunction(e *execContext, name string, fn reflect.Value, args []reflect.Value, injectEnv bool) (reflect.Value, error) {
	ft := fn.Type()
	if fn.Kind() != reflect.Func {
		return reflect.Value{}, fmt.Errorf("value of type %s is not callable", ft)
	}
	variadic := ft.IsVariadic()
	nIn := ft.NumIn()
	fixed := nIn
	if variadic {
		fixed = nIn - 1
	}

	injected := make([]bool, fixed)
	required := fixed
	if injectEnv {
		for i := 0; i < fixed; i++ {
			if ft.In(i) == reflectTypeEnv {
				injected[i] = true
				required--
			}
		}
	}

	if !variadic && len(args) != required {
		return reflect.Value{}, fmt.Errorf("wrong number of arguments for %s: want %d got %d", name, required, len(args))
	}
	if variadic && len(args) < required {
		return reflect.Value{}, fmt.Errorf("wrong number of arguments for %s: want at least %d got %d", name, required, len(args))
	}

	converted := make([]reflect.Value, 0, fixed+len(args))
	ai := 0
	for i := 0; i < fixed; i++ {
		if injected[i] {
			converted = append(converted, envArg(e))
			continue
		}
		cv, err := convertTo(ft.In(i), args[ai])
		if err != nil {
			return reflect.Value{}, fmt.Errorf("error calling %s: %v", name, err)
		}
		converted = append(converted, cv)
		ai++
	}
	if variadic {
		elem := ft.In(fixed).Elem()
		for ; ai < len(args); ai++ {
			cv, err := convertTo(elem, args[ai])
			if err != nil {
				return reflect.Value{}, fmt.Errorf("error calling %s: %v", name, err)
			}
			converted = append(converted, cv)
		}
	}

	var results []reflect.Value
	func() {
		defer func() {
			if r := recover(); r != nil {
				results = []reflect.Value{reflect.ValueOf(fmt.Errorf("call of %s panicked: %v", name, r))}
			}
		}()
		results = fn.Call(converted)
	}()

	if len(results) == 0 {
		return reflect.Value{}, nil
	}
	if len(results) == 1 {
		res := results[0]
		if res.Type().Implements(reflectTypeError) {
			if res.IsNil() {
				return reflect.Value{}, nil
			}
			return reflect.Value{}, res.Interface().(error)
		}
		return res, nil
	}
	if len(results) == 2 && results[1].Type().Implements(reflectTypeError) {
		if !results[1].IsNil() {
			return reflect.Value{}, results[1].Interface().(error)
		}
		return results[0], nil
	}
	return reflect.Value{}, fmt.Errorf("error calling %s: too many return values", name)
}

var reflectTypeError = reflect.TypeOf((*error)(nil)).Elem()

// envArg returns the environment map to inject into a function parameter of
// type Env. A nil context (e.g. the built-in call helper) yields an empty map.
func envArg(e *execContext) reflect.Value {
	if e == nil || e.env == nil {
		return reflect.ValueOf(Env(nil))
	}
	return reflect.ValueOf(Env(e.env))
}

func rvOf(a any) reflect.Value {
	if a == nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(a)
}

// ---------------------------------------------------------------------------
// Default (built-in) functions.
// ---------------------------------------------------------------------------

var builtins map[string]reflect.Value

func init() {
	builtins = map[string]reflect.Value{
		"len":        reflect.ValueOf(builtinLen),
		"printf":     reflect.ValueOf(builtinPrintf),
		"print":      reflect.ValueOf(builtinPrint),
		"println":    reflect.ValueOf(builtinPrintln),
		"eq":         reflect.ValueOf(builtinEq),
		"ne":         reflect.ValueOf(builtinNe),
		"lt":         reflect.ValueOf(builtinLt),
		"le":         reflect.ValueOf(builtinLe),
		"gt":         reflect.ValueOf(builtinGt),
		"ge":         reflect.ValueOf(builtinGe),
		"and":        reflect.ValueOf(builtinAnd),
		"or":         reflect.ValueOf(builtinOr),
		"not":        reflect.ValueOf(builtinNot),
		"index":      reflect.ValueOf(builtinIndex),
		"slice":      reflect.ValueOf(builtinSlice),
		"call":       reflect.ValueOf(builtinCall),
		"html":       reflect.ValueOf(builtinHTML),
		"js":         reflect.ValueOf(builtinJS),
		"urlquery":   reflect.ValueOf(builtinURLQuery),
		"add":        reflect.ValueOf(arithFn("+")),
		"sub":        reflect.ValueOf(arithFn("-")),
		"mul":        reflect.ValueOf(arithFn("*")),
		"div":        reflect.ValueOf(arithFn("/")),
		"mod":        reflect.ValueOf(arithFn("%")),
		"min":        reflect.ValueOf(minmaxFn(-1)),
		"max":        reflect.ValueOf(minmaxFn(1)),
		"abs":        reflect.ValueOf(builtinAbs),
		"upper":      reflect.ValueOf(builtinUpper),
		"lower":      reflect.ValueOf(builtinLower),
		"trim":       reflect.ValueOf(strings.TrimSpace),
		"trimPrefix": reflect.ValueOf(builtinTrimPrefix),
		"trimSuffix": reflect.ValueOf(builtinTrimSuffix),
		"contains":   reflect.ValueOf(strings.Contains),
		"hasPrefix":  reflect.ValueOf(strings.HasPrefix),
		"hasSuffix":  reflect.ValueOf(strings.HasSuffix),
		"repeat":     reflect.ValueOf(strings.Repeat),
		"split":      reflect.ValueOf(strings.Split),
		"join":       reflect.ValueOf(strings.Join),
		"fields":     reflect.ValueOf(strings.Fields),
		"replace":    reflect.ValueOf(builtinReplace),
		"atoi":       reflect.ValueOf(builtinAtoi),
		"itoa":       reflect.ValueOf(strconv.Itoa),
		"int":        reflect.ValueOf(builtinToInt),
		"float":      reflect.ValueOf(builtinToFloat),
		"toString":   reflect.ValueOf(builtinToString),
	}
}

func builtinLen(v any) (int, error) {
	rv := unwrapInterface(rvOf(v))
	if !rv.IsValid() {
		return 0, fmt.Errorf("len of nil value")
	}
	switch rv.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String, reflect.Chan:
		return rv.Len(), nil
	}
	return 0, fmt.Errorf("len of type %s", rv.Type())
}

func builtinPrintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
func builtinPrint(v ...any) string                    { return fmt.Sprint(v...) }
func builtinPrintln(v ...any) string                  { return fmt.Sprintln(v...) }

func builtinEq(a any, rest ...any) (bool, error) {
	for _, b := range rest {
		if !equalValues(rvOf(a), rvOf(b)) {
			return false, nil
		}
	}
	return true, nil
}

func builtinNe(a any, b any) (bool, error) {
	return !equalValues(rvOf(a), rvOf(b)), nil
}

func cmpBuiltin(op string, a, b any) (bool, error) {
	c, ok := compare(rvOf(a), rvOf(b))
	if !ok {
		return false, fmt.Errorf("incompatible types for %s: %T and %T", op, a, b)
	}
	switch op {
	case "lt":
		return c < 0, nil
	case "le":
		return c <= 0, nil
	case "gt":
		return c > 0, nil
	default:
		return c >= 0, nil
	}
}

func builtinLt(a, b any) (bool, error) { return cmpBuiltin("lt", a, b) }
func builtinLe(a, b any) (bool, error) { return cmpBuiltin("le", a, b) }
func builtinGt(a, b any) (bool, error) { return cmpBuiltin("gt", a, b) }
func builtinGe(a, b any) (bool, error) { return cmpBuiltin("ge", a, b) }

func builtinAnd(vals ...any) (any, error) {
	var last any = true
	for _, v := range vals {
		last = v
		b, err := isTrue(rvOf(v))
		if err != nil {
			return nil, err
		}
		if !b {
			return v, nil
		}
	}
	return last, nil
}

func builtinOr(vals ...any) (any, error) {
	var last any
	for _, v := range vals {
		b, err := isTrue(rvOf(v))
		if err != nil {
			return nil, err
		}
		if b {
			return v, nil
		}
		last = v
	}
	return last, nil
}

func builtinNot(v any) (bool, error) {
	b, err := isTrue(rvOf(v))
	return !b, err
}

func builtinIndex(v any, idx ...any) (any, error) {
	cur := rvOf(v)
	for _, k := range idx {
		var err error
		cur, err = evalIndex(cur, rvOf(k))
		if err != nil {
			return nil, err
		}
	}
	return valueToAny(cur), nil
}

func builtinSlice(v any, bounds ...any) (any, error) {
	rv := unwrapInterface(rvOf(v))
	if !rv.IsValid() {
		return nil, fmt.Errorf("slice of nil value")
	}
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, fmt.Errorf("slice of nil pointer")
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Array, reflect.Slice, reflect.String:
	default:
		return nil, fmt.Errorf("can't slice %s", rv.Type())
	}
	lo, hi := 0, rv.Len()
	if len(bounds) > 0 {
		n, ok := indexInt(rvOf(bounds[0]))
		if !ok {
			return nil, fmt.Errorf("slice bounds must be integers")
		}
		lo = int(n)
	}
	if len(bounds) > 1 {
		n, ok := indexInt(rvOf(bounds[1]))
		if !ok {
			return nil, fmt.Errorf("slice bounds must be integers")
		}
		hi = int(n)
	}
	if len(bounds) > 2 {
		return nil, fmt.Errorf("too many slice bounds")
	}
	if lo < 0 || hi < lo || hi > rv.Len() {
		return nil, fmt.Errorf("slice bounds out of range")
	}
	if rv.Kind() == reflect.String {
		return rv.String()[lo:hi], nil
	}
	return rv.Slice(lo, hi).Interface(), nil
}

func builtinCall(v any, args ...any) (any, error) {
	fn := unwrapInterface(rvOf(v))
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return nil, fmt.Errorf("value of type %T is not callable", v)
	}
	rargs := make([]reflect.Value, len(args))
	for i, a := range args {
		rargs[i] = rvOf(a)
	}
	res, err := callFunction(nil, "call", fn, rargs, false)
	if err != nil {
		return nil, err
	}
	return valueToAny(res), nil
}

func builtinHTML(s string) string { return html.EscapeString(s) }
func builtinURLQuery(s string) string {
	return url.QueryEscape(s)
}

func builtinJS(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x2028 || r == 0x2029:
			fmt.Fprintf(&sb, "\\u%04x", r)
		case r == '\\' || r == '\'' || r == '"' || r == '<' || r == '>' || r == '&':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func arithFn(op string) func(any, any) (any, error) {
	return func(a, b any) (any, error) {
		v, err := evalArith(op, rvOf(a), rvOf(b))
		if err != nil {
			return nil, err
		}
		return valueToAny(v), nil
	}
}

func minmaxFn(dir int) func(...any) (any, error) {
	return func(vals ...any) (any, error) {
		if len(vals) == 0 {
			return nil, fmt.Errorf("min/max requires at least one argument")
		}
		best := vals[0]
		for _, v := range vals[1:] {
			c, ok := compare(rvOf(best), rvOf(v))
			if !ok {
				return nil, fmt.Errorf("incomparable arguments")
			}
			if (dir < 0 && c > 0) || (dir > 0 && c < 0) {
				best = v
			}
		}
		return best, nil
	}
}

func builtinAbs(v any) (any, error) {
	rv := rvOf(v)
	if _, _, f, k, ok := loadNum(rv); ok && k == nkFloat {
		if f < 0 {
			f = -f
		}
		return f, nil
	}
	n, err := numAsInt64(rv)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		n = -n
	}
	return n, nil
}

func builtinUpper(s string) string { return strings.ToUpper(s) }
func builtinLower(s string) string { return strings.ToLower(s) }

func builtinTrimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }
func builtinTrimSuffix(s, suffix string) string { return strings.TrimSuffix(s, suffix) }

func builtinReplace(s, old, new string, n int) string {
	if n < 0 {
		n = -1
	}
	return strings.Replace(s, old, new, n)
}

func builtinAtoi(s string) (int, error) { return strconv.Atoi(s) }

func builtinToInt(v any) (int64, error) { return numAsInt64(rvOf(v)) }

func builtinToFloat(v any) (float64, error) {
	_, _, f, _, ok := loadNum(rvOf(v))
	if !ok {
		return 0, fmt.Errorf("cannot convert %T to float", v)
	}
	return f, nil
}

func builtinToString(v any) string { return fmt.Sprint(v) }
