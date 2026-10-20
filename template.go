package ctpl

import (
	"fmt"
	"io"
	"reflect"
	"strings"
)

// Default action delimiters, used unless Template.Delims overrides them.
const (
	defaultLeftDelim  = "{{"
	defaultRightDelim = "}}"
)

// Template is a parsed text template. It is created with New and parsed with
// Parse. Names defined by {{define}} and {{block}} are associated so they can
// be referenced with {{template "name"}} or rendered directly through
// ExecuteTemplate.
type Template struct {
	name  string
	nodes []node
	assoc map[string]*Template // shared by all associated templates
	owner *Template            // the root template of the association group
	funcs map[string]reflect.Value

	// escapeQuote is a template switch: when true, any substituted string
	// value written to the output has every single quote (') doubled (''),
	// e.g. for building SQL literals. When false (default) values are
	// written verbatim.
	escapeQuote bool

	// leftDelim/rightDelim hold the action delimiters configured by Delims.
	// An empty value means the corresponding default is used.
	leftDelim  string
	rightDelim string

	// caseInsensitiveFuncs, when true, makes function-name lookup ignore case
	// (built-ins and user-registered functions alike). Variable names and
	// keyword actions are unaffected. The default is false.
	caseInsensitiveFuncs bool

	// env is the shared environment exposed to templates. When non-nil, the
	// pseudo-function env("KEY") reads a value from it (see execContext).
	env map[string]any
}

// New creates a new root template with the given name.
func New(name string) *Template {
	t := &Template{name: name}
	t.assoc = map[string]*Template{name: t}
	t.owner = t
	return t
}

// New returns a new template associated with t but sharing the same
// association set and function map. See also text/template.Template.New.
func (t *Template) New(name string) *Template {
	root := t.ownerOf()
	child := &Template{name: name, assoc: root.assoc, owner: root}
	root.assoc[name] = child
	return child
}

// Lookup returns the template with the given name, or nil if it does not
// exist within the association set.
func (t *Template) Lookup(name string) *Template {
	return t.ownerOf().assoc[name]
}

func (t *Template) ownerOf() *Template {
	if t.owner == nil {
		return t
	}
	return t.owner
}

// Funcs adds the elements of the argument map to the template's function map.
// It panics if a map value is not a function.
func (t *Template) Funcs(funcMap FuncMap) *Template {
	root := t.ownerOf()
	if root.funcs == nil {
		root.funcs = map[string]reflect.Value{}
	}
	for name, fn := range funcMap {
		rv := reflect.ValueOf(fn)
		if rv.Kind() != reflect.Func {
			panic(fmt.Sprintf("template.Funcs: %q is not a function", name))
		}
		root.funcs[name] = rv
	}
	return t
}

// SetEscapeQuote toggles the single-quote doubling switch on the whole
// association group (root template). When on is true, every substituted
// string value written while executing contains each single quote replaced
// by two single quotes (''), which is useful when embedding values into
// SQL literals. When on is false, values are replaced verbatim.
//
// The escaping applies to data substitutions only: fields, variables, index
// expressions, method calls such as {{ .Value.String() }}, and so on. The
// value returned by any function call — built-in (printf, upper, toString,
// replace ...), registered through Funcs, or the env pseudo-function — is
// written verbatim, so a helper that assembles a ready-made SQL fragment
// keeps its single quotes intact:
//
//	tm.Funcs(FuncMap{"in": func(v []string) string { return "'a','b'" }})
//	// {{ in(.xs) }}        =>  'a','b'      (not ''a'',''b'')
//	// {{ printf("'%s'", .xs) }} =>  'x'     (not ''x'')
//
// The rule depends on the shape of the printed expression, not on where the
// value came from. Storing a function result in a variable first and printing
// the variable therefore still escapes:
//
//	// {{ $x := in(.xs) }}{{ $x }}  =>  ''a'',''b''
//
// Use that form when a function result is data-derived and does need the
// doubling.
func (t *Template) SetEscapeQuote(on bool) *Template {
	t.ownerOf().escapeQuote = on
	return t
}

// EscapeQuote reports whether the single-quote doubling switch is enabled.
func (t *Template) EscapeQuote() bool {
	return t.ownerOf().escapeQuote
}

// SetCaseInsensitiveFuncs toggles case-insensitive function-name lookup for
// the whole association group. When on is true, identifiers in function-call
// positions (e.g. {{ MyFunc(.x) }}) are matched against the function table in
// a case-insensitive manner, covering both built-ins and user functions.
// Variable names ($x) and keyword actions (if/else/end/...) are NOT affected
// regardless of this setting. The default is false (case-sensitive).
func (t *Template) SetCaseInsensitiveFuncs(on bool) *Template {
	t.ownerOf().caseInsensitiveFuncs = on
	return t
}

// CaseInsensitiveFuncs reports whether function-name lookup is case-insensitive.
func (t *Template) CaseInsensitiveFuncs() bool {
	return t.ownerOf().caseInsensitiveFuncs
}

// SetEnv installs (or replaces) the environment map shared by every template
// in the association group. When env is non-nil, the special pseudo-function
// env("KEY") can be used inside templates — either directly in expressions,
// assigned to a custom variable, or passed to user-defined functions — to
// read values from this map. Pass nil to clear it. The default is nil.
func (t *Template) SetEnv(env map[string]any) *Template {
	t.ownerOf().env = env
	return t
}

// Env returns the environment map configured for the association group, or
// nil if none was set.
func (t *Template) Env() map[string]any {
	return t.ownerOf().env
}

// Delims sets the action delimiters to the specified strings, to be used in
// subsequent calls to Parse. Each delimiter must be non-empty; an empty
// argument restores the default. The default delimiters are "{{" and "}}",
// so call Delims("{", "}") to switch the whole association group to single
// curly braces. As with text/template, Delims must be called before the
// template text is parsed.
func (t *Template) Delims(left, right string) *Template {
	root := t.ownerOf()
	root.leftDelim, root.rightDelim = left, right
	return t
}

// delims returns the effective action delimiters of the association group.
func (t *Template) delims() (left, right string) {
	root := t.ownerOf()
	left, right = root.leftDelim, root.rightDelim
	if left == "" {
		left = defaultLeftDelim
	}
	if right == "" {
		right = defaultRightDelim
	}
	return left, right
}

// funcNames returns the names of functions available at parse time (built-ins
// plus user-registered). When CaseInsensitiveFuncs is enabled, every name
// is folded to lower-case so the parser accepts any casing at the call site.
// The pseudo-function env is always recognised as a valid call site; if no
// environment map is configured the call is rejected at execution time.
func (t *Template) funcNames() map[string]bool {
	root := t.ownerOf()
	ci := root.caseInsensitiveFuncs
	cap := len(builtins) + len(root.funcs) + 1
	names := make(map[string]bool, cap)
	add := func(n string) {
		if ci {
			n = strings.ToLower(n)
		}
		names[n] = true
	}
	for n := range builtins {
		add(n)
	}
	add("env")
	for n := range root.funcs {
		add(n)
	}
	return names
}

// mergedFuncs returns built-in and user functions for execution. When
// CaseInsensitiveFuncs is enabled, all keys are folded to lower-case so
// the execContext lookup applies the same folding.
func (t *Template) mergedFuncs() map[string]reflect.Value {
	root := t.ownerOf()
	ci := root.caseInsensitiveFuncs
	m := make(map[string]reflect.Value, len(builtins)+len(root.funcs))
	for n, f := range builtins {
		k := n
		if ci {
			k = strings.ToLower(k)
		}
		m[k] = f
	}
	for n, f := range root.funcs {
		k := n
		if ci {
			k = strings.ToLower(k)
		}
		m[k] = f
	}
	return m
}

// Parse parses the template definition string. Repeated Parse calls append to
// the receiver's body.
func (t *Template) Parse(text string) (*Template, error) {
	left, right := t.delims()
	segs, err := lexMain(text, left, right)
	if err != nil {
		return nil, fmt.Errorf("template: %s: %v", t.name, err)
	}
	body, defs, err := parseText(t.name, segs, t.funcNames(), left, t.ownerOf().caseInsensitiveFuncs)
	if err != nil {
		return nil, err
	}
	t.nodes = append(t.nodes, body...)

	owner := t.ownerOf()
	for name, def := range defs {
		if _, exists := owner.assoc[name]; exists {
			return t, fmt.Errorf("template: %s: template %q redefined", t.name, name)
		}
		nt := &Template{name: name, assoc: owner.assoc, owner: owner}
		nt.nodes = def.nodes
		owner.assoc[name] = nt
	}
	return t, nil
}

// Execute applies the parsed template to the given data and writes the result
// to w.
func (t *Template) Execute(w io.Writer, data any) error {
	if t.nodes == nil {
		return fmt.Errorf("template: %s: no template parsed", t.name)
	}
	return t.executeInto(w, t, data)
}

// ExecuteTemplate applies the template associated with t that has the given
// name to data and writes the result to w.
func (t *Template) ExecuteTemplate(w io.Writer, name string, data any) error {
	target := t.Lookup(name)
	if target == nil {
		return fmt.Errorf("template: no template %q associated with template %q", name, t.name)
	}
	if target.nodes == nil {
		return fmt.Errorf("template: %s: no template parsed", target.name)
	}
	return t.executeInto(w, target, data)
}

func (t *Template) executeInto(w io.Writer, target *Template, data any) error {
	owner := t.ownerOf()
	e := &execContext{
		out:             w,
		funcs:           owner.mergedFuncs(),
		root:            reflect.ValueOf(data),
		vars:            newFrame(nil),
		tplName:         target.name,
		tmpl:            owner,
		escapeQuote:     owner.escapeQuote,
		caseInsensitive: owner.caseInsensitiveFuncs,
		env:             owner.env,
	}
	return e.run(target.nodes, reflect.ValueOf(data))
}

// Must is a helper that wraps a call to a function returning (*Template,
// error) and panics if the error is non-nil.
func Must(t *Template, err error) *Template {
	if err != nil {
		panic(err)
	}
	return t
}

// Name returns the template name.
func (t *Template) Name() string { return t.name }

// DefinedTemplates lists the defined associated templates, mirroring the
// standard library for compatibility.
func (t *Template) DefinedTemplates() string {
	owner := t.ownerOf()
	var b []byte
	for name := range owner.assoc {
		if b != nil {
			b = append(b, ", "...)
		}
		b = append(b, name...)
	}
	if len(b) == 0 {
		return ""
	}
	return "; defined templates are: " + string(b)
}
