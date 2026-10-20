package ctpl

import (
	"fmt"
	"reflect"
	"strings"
)

// ---------------------------------------------------------------------------
// Small reflection helpers used by the executor.
// ---------------------------------------------------------------------------

// unwrapInterface peels interface layers off v.
func unwrapInterface(v reflect.Value) reflect.Value {
	for v.IsValid() && v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

// isTrue mirrors text/template's truthiness rules.
func isTrue(v reflect.Value) (bool, error) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		return false, nil
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() != 0, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() != 0, nil
	case reflect.Float32, reflect.Float64:
		return v.Float() != 0, nil
	case reflect.String:
		return v.Len() > 0, nil
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return !v.IsNil(), nil
	case reflect.Struct, reflect.Array, reflect.Complex64, reflect.Complex128:
		return true, nil
	}
	return false, fmt.Errorf("cannot use value of type %s as a boolean", typeName(v))
}

// typeName returns a readable type name for a reflect value.
func typeName(v reflect.Value) string {
	if !v.IsValid() {
		return "nil"
	}
	return v.Type().String()
}

// methodByName looks up an exported method, trying the addressable value too.
func methodByName(v reflect.Value, name string) (reflect.Value, bool) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		return reflect.Value{}, false
	}
	if m := v.MethodByName(name); m.IsValid() {
		return m, true
	}
	if v.Kind() != reflect.Ptr && v.CanAddr() {
		if m := v.Addr().MethodByName(name); m.IsValid() {
			return m, true
		}
	}
	return reflect.Value{}, false
}

// evalField resolves ".name" against a value: first a method, then (through
// pointers) a struct field, and finally a map key indexed by the field name
// (matching text/template, which addresses map values with dot syntax).
func evalField(v reflect.Value, name string) (reflect.Value, error) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		return reflect.Value{}, fmt.Errorf("nil data; cannot evaluate field %s", name)
	}
	if m, ok := methodByName(v, name); ok {
		return m, nil
	}
	cur := v
	for {
		if m, ok := methodByName(cur, name); ok {
			return m, nil
		}
		switch cur.Kind() {
		case reflect.Ptr, reflect.Interface:
			if cur.IsNil() {
				return reflect.Value{}, fmt.Errorf("nil pointer evaluating %s", name)
			}
			cur = cur.Elem()
		default:
			goto fields
		}
	}
fields:
	cur = unwrapInterface(cur)
	switch cur.Kind() {
	case reflect.Struct:
		st := cur.Type()
		if sf, ok := st.FieldByName(name); ok {
			return structFieldValue(cur, sf)
		}
		// Case-insensitive fallback, like text/template.
		lower := strings.ToLower(name)
		for i := 0; i < st.NumField(); i++ {
			sf := st.Field(i)
			if sf.PkgPath == "" && strings.ToLower(sf.Name) == lower {
				return structFieldValue(cur, sf)
			}
		}
		return reflect.Value{}, fmt.Errorf("can't evaluate field %s in type %s", name, st)
	case reflect.Map:
		// Dot syntax addresses a map value by key name, exactly like
		// text/template: the field name is converted to the map's key type.
		kt := cur.Type().Key()
		key, err := convertTo(kt, reflect.ValueOf(name))
		if err != nil {
			return reflect.Value{}, fmt.Errorf("can't evaluate field %s in type %s", name, typeName(cur))
		}
		ev := cur.MapIndex(key)
		if !ev.IsValid() {
			return reflect.Value{}, nil // missing key behaves like <no value>
		}
		return ev, nil
	}
	return reflect.Value{}, fmt.Errorf("can't evaluate field %s in type %s", name, typeName(cur))
}

func structFieldValue(v reflect.Value, sf reflect.StructField) (reflect.Value, error) {
	if sf.PkgPath != "" {
		return reflect.Value{}, fmt.Errorf("cannot access unexported field %s", sf.Name)
	}
	return v.FieldByIndex(sf.Index), nil
}

// evalIndex applies "[index]" (array/slice/string/map).
func evalIndex(v reflect.Value, key reflect.Value) (reflect.Value, error) {
	v = unwrapInterface(v)
	key = unwrapInterface(key)
	if !v.IsValid() {
		return reflect.Value{}, fmt.Errorf("index of nil value")
	}
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}, fmt.Errorf("index of nil pointer")
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.String:
		idx, ok := indexInt(key)
		if !ok {
			return reflect.Value{}, fmt.Errorf("index must be an integer, got %s", typeName(key))
		}
		n := int64(v.Len())
		if idx < 0 || idx >= n {
			return reflect.Value{}, fmt.Errorf("index out of range: %d", idx)
		}
		if v.Kind() == reflect.String {
			return stringAt(v, int(idx))
		}
		return v.Index(int(idx)), nil
	case reflect.Map:
		kt := v.Type().Key()
		ck, err := convertTo(kt, key)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("map index: %v", err)
		}
		ev := v.MapIndex(ck)
		if !ev.IsValid() {
			return reflect.Value{}, nil // missing key: like <no value>
		}
		return ev, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot index value of type %s", typeName(v))
}

// stringAt returns the rune at byte index i as a one-character string.
func stringAt(v reflect.Value, i int) (reflect.Value, error) {
	s := v.String()
	if i < 0 || i >= len(s) {
		return reflect.Value{}, fmt.Errorf("index out of range")
	}
	for _, r := range s[i:] {
		return reflect.ValueOf(string(r)), nil
	}
	return reflect.Value{}, fmt.Errorf("index out of range")
}

func indexInt(v reflect.Value) (int64, bool) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		return 0, false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(v.Uint()), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Numeric arithmetic.
// ---------------------------------------------------------------------------

type numKind int

const (
	nkInt numKind = iota
	nkUint
	nkFloat
)

// loadNum extracts a numeric triple from v.
func loadNum(v reflect.Value) (i int64, u uint64, f float64, k numKind, ok bool) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i = v.Int()
		return i, 0, float64(i), nkInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u = v.Uint()
		return 0, u, float64(u), nkUint, true
	case reflect.Float32, reflect.Float64:
		f = v.Float()
		return 0, 0, f, nkFloat, true
	}
	return 0, 0, 0, nkInt, false
}

// arithPrecision merges two number kinds into the widest result kind.
func arithPrecision(a, b numKind) numKind {
	if a == nkFloat || b == nkFloat {
		return nkFloat
	}
	if a == nkUint || b == nkUint {
		return nkUint
	}
	return nkInt
}

// evalArith evaluates a binary arithmetic operator over numeric operands. For
// '+' two strings are concatenated as well.
func evalArith(op string, x, y reflect.Value) (reflect.Value, error) {
	// string concatenation
	if op == "+" {
		xs := unwrapInterface(x)
		ys := unwrapInterface(y)
		if xs.IsValid() && ys.IsValid() &&
			(xs.Kind() == reflect.String || (xs.Kind() == reflect.Interface && xs.Type().NumMethod() == 0)) {
			if xs.Kind() == reflect.String && ys.Kind() == reflect.String {
				return reflect.ValueOf(xs.String() + ys.String()), nil
			}
		}
	}
	_, _, xf, xk, xok := loadNum(x)
	_, _, yf, yk, yok := loadNum(y)
	if !xok || !yok {
		return reflect.Value{}, fmt.Errorf("invalid operand types for %s: %s and %s", op, typeName(x), typeName(y))
	}
	switch arithPrecision(xk, yk) {
	case nkFloat:
		a, b := xf, yf
		var r float64
		switch op {
		case "+":
			r = a + b
		case "-":
			r = a - b
		case "*":
			r = a * b
		case "/":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = a / b
		case "%":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = modFloat(a, b)
		default:
			return reflect.Value{}, fmt.Errorf("unknown operator %q", op)
		}
		return reflect.ValueOf(r), nil
	case nkUint:
		// Convert signed operands; they fit because we only get here when no
		// negative int64 is combined with a uint64-class operand below.
		a, err := numAsUint64(x)
		if err != nil {
			return reflect.Value{}, err
		}
		b, err := numAsUint64(y)
		if err != nil {
			return reflect.Value{}, err
		}
		var r uint64
		switch op {
		case "+":
			r = a + b
		case "-":
			r = a - b
		case "*":
			r = a * b
		case "/":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = a / b
		case "%":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = a % b
		case "&":
			r = a & b
		case "|":
			r = a | b
		case "^":
			r = a ^ b
		case "<<":
			r = a << b
		case ">>":
			r = a >> b
		default:
			return reflect.Value{}, fmt.Errorf("unknown operator %q", op)
		}
		return reflect.ValueOf(r), nil
	default: // int
		a, err := numAsInt64(x)
		if err != nil {
			return reflect.Value{}, err
		}
		b, err := numAsInt64(y)
		if err != nil {
			return reflect.Value{}, err
		}
		var r int64
		switch op {
		case "+":
			r = a + b
		case "-":
			r = a - b
		case "*":
			r = a * b
		case "/":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = a / b
		case "%":
			if b == 0 {
				return reflect.Value{}, fmt.Errorf("division by zero")
			}
			r = a % b
		case "&":
			r = a & b
		case "|":
			r = a | b
		case "^":
			r = a ^ b
		case "<<":
			r = a << b
		case ">>":
			r = a >> b
		default:
			return reflect.Value{}, fmt.Errorf("unknown operator %q", op)
		}
		return reflect.ValueOf(r), nil
	}
}

func modFloat(a, b float64) float64 {
	r := a / b
	tr := float64(int64(r))
	return a - tr*b
}

// numAsInt64 converts an already-checked numeric operand to int64.
func numAsInt64(v reflect.Value) (int64, error) {
	v = unwrapInterface(v)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > 1<<63-1 {
			return 0, fmt.Errorf("value %d overflows int64", u)
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		return int64(v.Float()), nil
	}
	return 0, fmt.Errorf("not a number: %s", typeName(v))
}

func numAsUint64(v reflect.Value) (uint64, error) {
	v = unwrapInterface(v)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := v.Int()
		if i < 0 {
			return 0, fmt.Errorf("negative value %d cannot be unsigned", i)
		}
		return uint64(i), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if f < 0 {
			return 0, fmt.Errorf("negative value cannot be unsigned")
		}
		return uint64(f), nil
	}
	return 0, fmt.Errorf("not a number: %s", typeName(v))
}

// ---------------------------------------------------------------------------
// Comparisons.
// ---------------------------------------------------------------------------

// compare returns -1, 0 or +1, or false when the operands cannot be ordered.
func compare(x, y reflect.Value) (int, bool) {
	x = unwrapInterface(x)
	y = unwrapInterface(y)
	// identical strings compare as strings.
	if x.IsValid() && y.IsValid() &&
		x.Kind() == reflect.String && y.Kind() == reflect.String {
		return strings.Compare(x.String(), y.String()), true
	}
	_, _, xf, xk, xok := loadNum(x)
	_, _, yf, yk, yok := loadNum(y)
	if xok && yok {
		switch arithPrecision(xk, yk) {
		case nkFloat:
			return floatCmp(xf, yf), true
		case nkUint:
			a, err1 := numAsUint64(x)
			b, err2 := numAsUint64(y)
			if err1 != nil || err2 != nil {
				// fall back to float when an int64 is negative.
				return floatCmp(xf, yf), true
			}
			return uintCmp(a, b), true
		default:
			xi, err1 := numAsInt64(x)
			yi, err2 := numAsInt64(y)
			if err1 != nil || err2 != nil {
				return 0, false
			}
			return intCmp(xi, yi), true
		}
	}
	return 0, false
}

func intCmp(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func uintCmp(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func floatCmp(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// equalValues implements == / != with deep fallback.
func equalValues(x, y reflect.Value) bool {
	x = unwrapInterface(x)
	y = unwrapInterface(y)
	if !x.IsValid() || !y.IsValid() {
		return x.IsValid() == y.IsValid() // nil == nil
	}
	if c, ok := compare(x, y); ok {
		return c == 0
	}
	if x.Kind() == reflect.Bool && y.Kind() == reflect.Bool {
		return x.Bool() == y.Bool()
	}
	if x.Type() == y.Type() {
		switch x.Kind() {
		case reflect.Bool:
			return x.Bool() == y.Bool()
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			return x.Pointer() == y.Pointer()
		}
	}
	if isComparable(x) && isComparable(y) && x.Type() == y.Type() {
		return x.Interface() == y.Interface()
	}
	return reflect.DeepEqual(valueToAny(x), valueToAny(y))
}

func isComparable(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Func:
		return false
	}
	return true
}

// valueToAny converts a reflect.Value into an any for DeepEqual.
func valueToAny(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.CanInterface() {
		return v.Interface()
	}
	return fmt.Sprintf("<value of type %s>", v.Type())
}

// ---------------------------------------------------------------------------
// Argument conversion for calling user/builtin functions.
// ---------------------------------------------------------------------------

func convertTo(t reflect.Type, v reflect.Value) (reflect.Value, error) {
	v = unwrapInterface(v)
	if !v.IsValid() {
		if isNilable(t) {
			return reflect.Zero(t), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot use <no value> as %s", t)
	}
	vt := v.Type()
	if vt.AssignableTo(t) {
		return v, nil
	}
	if t.Kind() == reflect.Interface {
		if vt.Implements(t) {
			return v, nil
		}
		return reflect.Value{}, fmt.Errorf("cannot use %s as %s", vt, t)
	}
	// numeric conversions
	if isNumberKind(v.Kind()) && isNumberKind(t.Kind()) {
		return v.Convert(t), nil
	}
	if t.Kind() == reflect.String && (v.Kind() == reflect.String) {
		return v.Convert(t), nil
	}
	if t.Kind() == reflect.String && v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
		return v.Convert(t), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", vt, t)
}

func isNilable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return true
	}
	return false
}

func isNumberKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}
