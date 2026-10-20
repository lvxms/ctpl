package ctpl

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, src string, data any, funcs FuncMap) string {
	t.Helper()
	tm := New("t")
	if funcs != nil {
		tm.Funcs(funcs)
	}
	if _, err := tm.Parse(src); err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, data); err != nil {
		t.Fatalf("execute %q: %v", src, err)
	}
	return b.String()
}

func execErr(t *testing.T, src string, data any, funcs ...FuncMap) error {
	t.Helper()
	tm := New("t")
	if len(funcs) > 0 && funcs[0] != nil {
		tm.Funcs(funcs[0])
	}
	if _, err := tm.Parse(src); err != nil {
		return err
	}
	return tm.Execute(&bytes.Buffer{}, data)
}

func TestEscapeQuoteSwitch(t *testing.T) {
	src := `SELECT 'fixed' FROM t WHERE name = '{{ .Name }}'`
	data := struct{ Name string }{"It's O'Brien"}
	tm := New("q")
	if _, err := tm.Parse(src); err != nil {
		t.Fatal(err)
	}

	// Default (switch off): values are replaced verbatim.
	var b bytes.Buffer
	if err := tm.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "SELECT 'fixed' FROM t WHERE name = 'It's O'Brien'"; b.String() != want {
		t.Errorf("off => %q, want %q", b.String(), want)
	}

	// Switch on: every ' inside substituted strings becomes ''.
	b.Reset()
	tm.SetEscapeQuote(true)
	if err := tm.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "SELECT 'fixed' FROM t WHERE name = 'It''s O''Brien'"; b.String() != want {
		t.Errorf("on => %q, want %q", b.String(), want)
	}
	if !tm.EscapeQuote() {
		t.Error("EscapeQuote() should report true after SetEscapeQuote(true)")
	}

	// Switch back off: verbatim again.
	b.Reset()
	tm.SetEscapeQuote(false)
	if err := tm.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "SELECT 'fixed' FROM t WHERE name = 'It's O'Brien'"; b.String() != want {
		t.Errorf("off again => %q, want %q", b.String(), want)
	}
}

// quotedVal carries a method that returns a string containing a quote, used to
// check that method calls stay subject to the escapeQuote doubling.
type quotedVal struct{}

func (quotedVal) Quote() string { return "a'b" }

func TestEscapeQuoteExemptsFuncResults(t *testing.T) {
	data := struct{ Name string }{"It's"}

	// raw returns a ready-made SQL fragment; its quotes must survive.
	raw := func(s string) string { return "'" + s + "'" }

	// 1. User function result is written verbatim.
	tm := New("q").SetEscapeQuote(true).Funcs(FuncMap{"raw": raw})
	if _, err := tm.Parse(`{{ raw(.Name) }}`); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "'It's'"; b.String() != want {
		t.Errorf("user func => %q, want %q", b.String(), want)
	}

	// 2. Data substitution in the same template is still escaped.
	b.Reset()
	tm2 := New("q2").SetEscapeQuote(true).Funcs(FuncMap{"raw": raw})
	if _, err := tm2.Parse(`{{ .Name }}|{{ raw(.Name) }}`); err != nil {
		t.Fatal(err)
	}
	if err := tm2.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "It''s|'It's'"; b.String() != want {
		t.Errorf("mixed => %q, want %q", b.String(), want)
	}

	// 3. Built-in functions are exempt too.
	if got := runEscape(t, `{{ upper(.Name) }}`, data, true); got != "IT'S" {
		t.Errorf("builtin upper => %q, want %q", got, "IT'S")
	}
	if got := runEscape(t, `{{ printf("'%s'", .Name) }}`, data, true); got != "'It's'" {
		t.Errorf("builtin printf => %q, want %q", got, "'It's'")
	}

	// 4. The env pseudo-function is exempt as well.
	tmEnv := New("qenv").SetEscapeQuote(true).SetEnv(map[string]any{"G": "'It's'"})
	if _, err := tmEnv.Parse(`{{ env("G") }}`); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := tmEnv.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "'It's'"; b.String() != want {
		t.Errorf("env => %q, want %q", b.String(), want)
	}

	// 5. A variable holding a function result is still escaped, because the
	//    printed expression is a variable, not a call.
	//    ('It's' with every quote doubled => ''It''s'')
	tm3 := New("q3").SetEscapeQuote(true).Funcs(FuncMap{"raw": raw})
	if _, err := tm3.Parse(`{{ $x := raw(.Name) }}{{ $x }}`); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := tm3.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "''It''s''"; b.String() != want {
		t.Errorf("via variable => %q, want %q", b.String(), want)
	}

	// 6. Method calls on data are escaped (chains, not function calls).
	tm4 := New("q4").SetEscapeQuote(true)
	if _, err := tm4.Parse(`{{ .V.Quote() }}`); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := tm4.Execute(&b, struct{ V quotedVal }{quotedVal{}}); err != nil {
		t.Fatal(err)
	}
	if want := "a''b"; b.String() != want {
		t.Errorf("method call => %q, want %q", b.String(), want)
	}

	// 7. Case-insensitive lookup keeps the exemption working.
	tm5 := New("q5").SetEscapeQuote(true).SetCaseInsensitiveFuncs(true).Funcs(FuncMap{"raw": raw})
	if _, err := tm5.Parse(`{{ RAW(.Name) }}`); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := tm5.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if want := "'It's'"; b.String() != want {
		t.Errorf("case-insensitive => %q, want %q", b.String(), want)
	}

	// 8. With the switch off nothing is escaped anyway.
	if got := runEscape(t, `{{ raw(.Name) }}|{{ .Name }}`, data, false); got != "'It's'|It's" {
		t.Errorf("switch off => %q, want %q", got, "'It's'|It's")
	}
}

func runEscape(t *testing.T, src string, data any, escape bool) string {
	t.Helper()
	tm := New("t").SetEscapeQuote(escape).Funcs(FuncMap{"raw": func(s string) string {
		return "'" + s + "'"
	}})
	if _, err := tm.Parse(src); err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, data); err != nil {
		t.Fatalf("execute %q: %v", src, err)
	}
	return b.String()
}

func TestDelims(t *testing.T) {
	exec := func(t *testing.T, tm *Template, data any) string {
		t.Helper()
		var b bytes.Buffer
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return b.String()
	}
	parse := func(t *testing.T, tm *Template, src string) {
		t.Helper()
		if _, err := tm.Parse(src); err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
	}

	// Default delimiters {{ }} remain unchanged.
	tm := New("double")
	parse(t, tm, `Hello, {{ .Name }}! Sum={{ add(1, 2) }}`)
	if got := exec(t, tm, map[string]string{"Name": "world"}); got != "Hello, world! Sum=3" {
		t.Errorf("default delims => %q", got)
	}

	// Switch to single curly braces { }.
	tm2 := New("single")
	tm2.Delims("{", "}")
	parse(t, tm2, `{ if .On }ON: { .Name }{ else }OFF{ end } {/* comment */}`)
	if got := exec(t, tm2, map[string]any{"On": true, "Name": "go"}); got != "ON: go " {
		t.Errorf("single braces on => %q", got)
	}
	if got := exec(t, tm2, map[string]any{"On": false, "Name": "go"}); got != "OFF " {
		t.Errorf("single braces off => %q", got)
	}

	// { define }d{ end } associated templates share the delimiters of the root.
	tm3 := New("grp")
	tm3.Delims("{", "}")
	parse(t, tm3, `{ define "d" }[ { . } ]{ end }{ template "d" . }`)
	if got := exec(t, tm3, "x"); got != "[ x ]" {
		t.Errorf("define with single braces => %q", got)
	}

	// Arbitrary custom delimiters (empty restores the default).
	tm4 := New("custom")
	tm4.Delims("<<", ">>")
	parse(t, tm4, `a<< . >>b<< printf("%s!", upper("ok")) >>`)
	if got := exec(t, tm4, "v"); got != "avbOK!" {
		t.Errorf("custom delims => %q", got)
	}
	tm5 := New("reset")
	tm5.Delims("{", "}").Delims("", "")
	parse(t, tm5, `{{ . }}`)
	if got := exec(t, tm5, "def"); got != "def" {
		t.Errorf("reset delims => %q", got)
	}
}

func TestCaseInsensitiveFuncs(t *testing.T) {
	exec := func(t *testing.T, tm *Template, data any) string {
		t.Helper()
		var b bytes.Buffer
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return b.String()
	}

	// Default (case-sensitive): built-in name in wrong case must error at
	// parse time.
	tm := New("cs")
	if _, err := tm.Parse(`{{ ADD(1, 2) }}`); err == nil {
		t.Fatalf("expected error for ADD when case-sensitive")
	}

	// Default for user functions: must error if not declared.
	tm = New("cs_user")
	if _, err := tm.Funcs(FuncMap{"MyFn": func(s string) string { return s + "!" }}).
		Parse(`{{ myFn("hi") }}`); err == nil {
		t.Fatalf("expected error for lower-case myFn when case-sensitive")
	}

	// Built-ins become case-insensitive.
	tm = New("ci")
	tm.SetCaseInsensitiveFuncs(true)
	if _, err := tm.Parse(`{{ ADD(1, 2) }} | {{ Upper("hi") }} | {{ LEN("abc") }}`); err != nil {
		t.Fatalf("parse ci builtins: %v", err)
	}
	if got := exec(t, tm, nil); got != "3 | HI | 3" {
		t.Errorf("ci builtins => %q", got)
	}

	// User functions case-insensitive lookup.
	tm = New("ci_user")
	tm.SetCaseInsensitiveFuncs(true)
	if _, err := tm.Funcs(FuncMap{"MyFn": func(s string) string { return "<" + s + ">" }}).
		Parse(`{{ myfn("hi") }} {{ MYFN("go") }}`); err != nil {
		t.Fatalf("parse ci user fn: %v", err)
	}
	if got := exec(t, tm, nil); got != "<hi> <go>" {
		t.Errorf("ci user fn => %q", got)
	}

	// Case-insensitive must NOT affect variables or keywords.
	tm = New("ci_var")
	tm.SetCaseInsensitiveFuncs(true)
	if _, err := tm.Parse(`{{ $x := 5 }}{{ . }} {{ $x }}`); err != nil {
		t.Fatalf("parse ci vars: %v", err)
	}
	if got := exec(t, tm, "dot"); got != "dot 5" {
		t.Errorf("ci vars => %q", got)
	}

	// Setting can be queried.
	tm = New("ci_flag")
	tm.SetCaseInsensitiveFuncs(true)
	if !tm.CaseInsensitiveFuncs() {
		t.Errorf("expected CaseInsensitiveFuncs to be true")
	}
}

func TestEnv(t *testing.T) {
	exec := func(t *testing.T, tm *Template, data any) string {
		t.Helper()
		var b bytes.Buffer
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return b.String()
	}

	// Reading from env as a pseudo-function.
	tm := New("env1")
	tm.SetEnv(map[string]any{"Name": "world", "PI": 3.14, "Items": []string{"a", "b"}})
	if _, err := tm.Parse(`Hello, {{ env("Name") }}! pi={{ env("PI") }} first={{ index(env("Items"), 0) }}`); err != nil {
		t.Fatalf("parse env: %v", err)
	}
	if got := exec(t, tm, nil); got != "Hello, world! pi=3.14 first=a" {
		t.Errorf("env read => %q", got)
	}

	// Assigning env() value to a custom variable and using it.
	tm = New("env2")
	tm.SetEnv(map[string]any{"Greeting": "hi"})
	if _, err := tm.Parse(`{{ $g := env("Greeting") }}{{ $g }} {{ upper($g) }}`); err != nil {
		t.Fatalf("parse env assign: %v", err)
	}
	if got := exec(t, tm, nil); got != "hi HI" {
		t.Errorf("env assign => %q", got)
	}

	// env(...) passed as argument to a user function.
	tm = New("env3")
	tm.SetEnv(map[string]any{"Name": "go"})
	if _, err := tm.Funcs(FuncMap{"greet": func(n string) string { return "Hello, " + n }}).
		Parse(`{{ greet(env("Name")) }}`); err != nil {
		t.Fatalf("parse env arg: %v", err)
	}
	if got := exec(t, tm, nil); got != "Hello, go" {
		t.Errorf("env arg => %q", got)
	}

	// Unknown env key renders as <no value>.
	tm = New("env4")
	tm.SetEnv(map[string]any{"X": 1})
	if _, err := tm.Parse(`[{{ env("missing") }}]`); err != nil {
		t.Fatalf("parse env missing: %v", err)
	}
	if got := exec(t, tm, nil); got != "[<no value>]" {
		t.Errorf("env missing => %q", got)
	}

	// env without configuration: parsing succeeds, executing surfaces the error.
	tm = New("env5")
	if _, err := tm.Parse(`{{ env("X") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, nil); err == nil {
		t.Fatalf("expected runtime error: env without configuration")
	}

	// User can override env by registering their own function with that name.
	tm = New("env6")
	tm.SetEnv(map[string]any{"X": "env-value"})
	if _, err := tm.Funcs(FuncMap{"env": func(k string) string { return "user:" + k }}).
		Parse(`{{ env("X") }}`); err != nil {
		t.Fatalf("parse user env: %v", err)
	}
	if got := exec(t, tm, nil); got != "user:X" {
		t.Errorf("user env override => %q", got)
	}

	// SetEnv(nil) clears the environment.
	tm = New("env7")
	tm.SetEnv(map[string]any{"X": 1})
	if tm.Env() == nil {
		t.Fatalf("expected env to be set")
	}
	tm.SetEnv(nil)
	if tm.Env() != nil {
		t.Errorf("expected env to be nil after SetEnv(nil)")
	}
	var bb bytes.Buffer
	if err := Must(tm.Parse(`{{ env("X") }}`)).Execute(&bb, nil); err == nil {
		t.Fatalf("expected runtime error after clearing env")
	}

	// Case-insensitive + env: user function lookup is also case-insensitive.
	tm = New("env8")
	tm.SetCaseInsensitiveFuncs(true)
	tm.SetEnv(map[string]any{"Name": "world"})
	if _, err := tm.Parse(`{{ ENV("Name") }}`); err != nil {
		t.Fatalf("parse ci env: %v", err)
	}
	if got := exec(t, tm, nil); got != "world" {
		t.Errorf("ci env => %q", got)
	}
}

func TestFuncEnvParam(t *testing.T) {
	exec := func(t *testing.T, tm *Template, data any) string {
		t.Helper()
		var b bytes.Buffer
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return b.String()
	}

	// A parameter of type Env is injected by the engine and does not consume
	// a template argument, so the function body can read env directly.
	tm := New("fenv1")
	tm.SetEnv(map[string]any{"Prefix": "Hello, ", "Suffix": "!"})
	tm.Funcs(FuncMap{
		"greet": func(env Env, name string) string {
			return env["Prefix"].(string) + name + env["Suffix"].(string)
		},
	})
	if _, err := tm.Parse(`{{ greet("world") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, nil); got != "Hello, world!" {
		t.Errorf("env param => %q", got)
	}

	// The Env parameter may appear in any fixed position.
	tm = New("fenv2")
	tm.SetEnv(map[string]any{"X": "9"})
	tm.Funcs(FuncMap{
		"wrap": func(before string, env Env, after string) string {
			return before + env["X"].(string) + after
		},
	})
	if _, err := tm.Parse(`{{ wrap("[", "]") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, nil); got != "[9]" {
		t.Errorf("env param middle => %q", got)
	}

	// Variadic functions still work alongside an injected Env parameter.
	tm = New("fenv3")
	tm.SetEnv(map[string]any{"Sep": "-"})
	tm.Funcs(FuncMap{
		"joinEnv": func(env Env, parts ...string) string {
			return strings.Join(parts, env["Sep"].(string))
		},
	})
	if _, err := tm.Parse(`{{ joinEnv("a", "b", "c") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, nil); got != "a-b-c" {
		t.Errorf("env param variadic => %q", got)
	}

	// Without a configured environment the injected map is empty (not nil
	// pointer dereference), so lookups simply miss.
	tm = New("fenv4")
	tm.Funcs(FuncMap{
		"has": func(env Env, k string) bool {
			_, ok := env[k]
			return ok
		},
	})
	if _, err := tm.Parse(`{{ has("Nope") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, nil); got != "false" {
		t.Errorf("empty env => %q", got)
	}

	// A parameter typed map[string]any is NOT injected: it is an ordinary
	// argument and must be supplied from the template.
	tm = New("fenv5")
	tm.SetEnv(map[string]any{"X": "env"})
	tm.Funcs(FuncMap{
		"pick": func(m map[string]any, k string) string { return m[k].(string) },
	})
	if _, err := tm.Parse(`{{ pick(.m, "k") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, map[string]any{"m": map[string]any{"k": "arg"}}); got != "arg" {
		t.Errorf("plain map arg => %q", got)
	}

	// Method calls on data values never inject, even when the receiver type
	// is exactly Env, so the receiver is still passed as the first argument.
	tm = New("fenv6")
	tm.SetEnv(map[string]any{"X": "ignored"})
	if _, err := tm.Parse(`{{ .Get("X") }}`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := exec(t, tm, Env{"X": "receiver"}); got != "receiver" {
		t.Errorf("method receiver => %q", got)
	}
}

// Get is declared in the test file purely so TestFuncEnvParam can verify that
// method calls whose receiver type is exactly Env are not treated as the
// injected environment parameter.
func (e Env) Get(k string) string { return e[k].(string) }

func TestNestedCallsAndOperators(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ printf("%d", add(1, mul(2, 3))) }}`, "7"},
		{`{{ add(1, sub(10, mul(2, 3))) }}`, "5"},
		{`{{ 2 + 3 * 4 }}`, "14"},
		{`{{ (2 + 3) * 4 }}`, "20"},
		{`{{ 10 % 3 }}`, "1"},
		{`{{ 7 / 2 }}`, "3"},
		{`{{ 1 < 2 && 2 <= 2 }}`, "true"},
		{`{{ 0 || 3 }}`, "true"},
		{`{{ !false }}`, "true"},
		{`{{ "a" + "b" + upper("cd") }}`, "abCD"},
		{`{{ 3 > 2 ? "yes" : "no" }}`, "yes"},
		{`{{ len("hello") }}`, "5"},
		{`{{ hasPrefix("hello", "he") }}`, "true"},
		{`{{ 1.5 + 1 }}`, "2.5"},
		{`{{ max(3, min(8, 5)) }}`, "5"},
		{`{{ printf("%s-%d", "n", 42) }}`, "n-42"},
		{`{{ 'A' + 1 }}`, "66"},
		{`{{ nil == nil }}`, "true"},
		{`{{ 6 & 3 }}`, "2"},
		{`{{ 6 | 1 }}`, "7"},
		{`{{ 6 ^ 1 }}`, "7"},
		{`{{ 1 << 4 }}`, "16"},
		{`{{ 0xF0 >> 4 }}`, "15"},
		{`{{ ~0 }}`, "-1"},
	}
	for _, c := range cases {
		if got := run(t, c.src, nil, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
}

func TestStructData(t *testing.T) {
	type person struct {
		Name string
		Age  int
	}
	type dept struct{ Staff []person }
	d := dept{Staff: []person{{Name: "Alice", Age: 30}, {Name: "Bob", Age: 40}}}

	cases := []struct{ src, want string }{
		{`{{ .Staff[0].Name }} is {{ .Staff[0].Age }}`, "Alice is 30"},
		{`{{ upper(.Staff[1].Name) }}`, "BOB"},
		{`{{ $x := .Staff[1].Age }}{{ $x + 5 }}`, "45"},
		{`{{ .Staff[0].Age >= 18 ? "adult" : "child" }}`, "adult"},
	}
	for _, c := range cases {
		if got := run(t, c.src, d, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMethodCall(t *testing.T) {
	// method-style call on a function-valued struct field
	tm := New("m")
	if _, err := tm.Parse(`{{ .Double(21) }}`); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	data := struct{ Double func(int) int }{func(i int) int { return i * 2 }}
	if err := tm.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if b.String() != "42" {
		t.Errorf("method got %q", b.String())
	}
	// selecting a method that does not exist must be an execution error
	tm2 := New("m2")
	if _, err := tm2.Parse(`{{ .Name.Length() }}`); err != nil {
		t.Fatal(err)
	}
	if err := tm2.Execute(&bytes.Buffer{}, struct{ Name string }{"gopher"}); err == nil {
		t.Error("expected error calling .Length on a string")
	}
}

func TestIfElse(t *testing.T) {
	cases := []struct {
		src  string
		data int
		want string
	}{
		{`{{ if . > 10 }}big{{ else }}small{{ end }}`, 5, "small"},
		{`{{ if . > 10 }}big{{ else }}small{{ end }}`, 42, "big"},
		{`{{ if . == 1 }}one{{ else if . == 2 }}two{{ else }}many{{ end }}`, 2, "two"},
		{`{{ if . == 1 }}one{{ else if . == 2 }}two{{ else }}many{{ end }}`, 9, "many"},
		{`{{ if . }}yes{{ end }}`, 0, ""},
		{`{{ if $x := . }}{{ $x * 2 }}{{ end }}`, 21, "42"},
	}
	for _, c := range cases {
		if got := run(t, c.src, c.data, nil); got != c.want {
			t.Errorf("data=%d %q => %q, want %q", c.data, c.src, got, c.want)
		}
	}
}

func TestFor(t *testing.T) {
	cases := []struct {
		src  string
		data any
		want string
	}{
		{`{{ for . }}{{ . }},{{ end }}`, []int{1, 2, 3}, "1,2,3,"},
		{`{{ for $i, $v := . }}[{{ $i }}:{{ $v }}]{{ end }}`, []string{"a", "b"}, "[0:a][1:b]"},
		{`{{ for $v := . }}{{ $v }}{{ else }}none{{ end }}`, []int{}, "none"},
		{`{{ for . }}{{ if . == 2 }}{{ break }}{{ end }}{{ . }}{{ end }}`, []int{1, 2, 3}, "1"},
		{`{{ for . }}{{ if . == 2 }}{{ continue }}{{ end }}{{ . }}{{ end }}`, []int{1, 2, 3}, "13"},
	}
	for _, c := range cases {
		if got := run(t, c.src, c.data, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMapIndexAndFor(t *testing.T) {
	data := map[string]int{"a": 1, "b": 2}
	got := run(t, `{{ .["a"] }}`, data, nil)
	if got != "1" {
		t.Errorf("map index got %q", got)
	}
	got = run(t, `{{ for $k, $v := . }}{{ $k }}={{ $v }};{{ end }}`, data, nil)
	if !strings.Contains(got, "a=1;") || !strings.Contains(got, "b=2;") {
		t.Errorf("map iteration got %q", got)
	}
	if err := execErr(t, `{{ index(. , "zz") }}`, data); err != nil {
		// missing key -> <no value>, no error expected
		t.Fatalf("missing key should not error: %v", err)
	}
}

func TestMapDotFieldAccess(t *testing.T) {
	// dot syntax indexes a map by its key, matching text/template.
	cases := []struct {
		src  string
		data any
		want string
	}{
		{`{{ .a }}`, map[string]int{"a": 1}, "1"},
		{`{{ .user.name }}`, map[string]any{"user": map[string]any{"name": "n"}}, "n"},
		{`{{ .zz }}`, map[string]int{"a": 1}, "<no value>"},
	}
	for _, c := range cases {
		if got := run(t, c.src, c.data, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
	// the reported scenario: C-style call with map[string]any receivers
	d := map[string]any{"x": 1, "y": 2, "z": 3}
	fm := FuncMap{"sum": func(x, y, z int) int { return x + y + z }}
	if got := run(t, `call: {{ sum(.x, .y, .z) }}`, d, fm); got != "call: 6" {
		t.Errorf("sum(.x, .y, .z) => %q, want %q", got, "call: 6")
	}
}

func TestWith(t *testing.T) {
	type user struct{ Name string }
	type wrap struct{ User *user }
	cases := []struct {
		src  string
		data wrap
		want string
	}{
		{`{{ with .User }}{{ .Name }}{{ else }}anon{{ end }}`,
			wrap{&user{"n"}}, "n"},
		{`{{ with .User }}{{ .Name }}{{ else }}anon{{ end }}`,
			wrap{}, "anon"},
		{`{{ with $u := .User }}{{ $u.Name }}{{ end }}`,
			wrap{&user{"x"}}, "x"},
	}
	for _, c := range cases {
		if got := run(t, c.src, c.data, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
}

func TestDefineTemplateBlock(t *testing.T) {
	src := `{{ define "row" }}<{{ . }}>{{ end }}` +
		`{{ for . }}{{ template "row" . }}{{ end }}`
	got := run(t, src, []string{"a", "b"}, nil)
	if got != "<a><b>" {
		t.Errorf("got %q", got)
	}

	src2 := `{{ block "title" . }}hello {{ . }}{{ end }}!`
	if got := run(t, src2, "world", nil); got != "hello world!" {
		t.Errorf("block got %q", got)
	}
}

func TestExecuteTemplateAndFuncs(t *testing.T) {
	tm := New("base")
	tm.Funcs(FuncMap{"shout": func(s string) string { return strings.ToUpper(s) + "!" }})
	if _, err := tm.Parse(`outer {{ template "inner" . }}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.New("inner").Parse(`{{ shout(.Name) }}`); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tm.ExecuteTemplate(&b, "base", struct{ Name string }{"go"}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "outer GO!" {
		t.Errorf("got %q", b.String())
	}
	if tm.Lookup("nope") != nil {
		t.Error("Lookup of missing template should be nil")
	}
}

func TestWhitespaceTrimAndComments(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a{{- if true -}} b {{- end -}}c", "abc"},
		{"a {{ if true }} b {{ end }} c", "a  b  c"},
		{"x{{/* comment */}}y", "xy"},
		{"x {{- /* comment */ -}} y", "xy"},
	}
	for _, c := range cases {
		if got := run(t, c.src, nil, nil); got != c.want {
			t.Errorf("%q => %q, want %q", c.src, got, c.want)
		}
	}
}

func TestVariablesScope(t *testing.T) {
	src := `{{ $total := 0 }}{{ for $i, $v := . }}{{ $total = $total + $v }}{{ end }}{{ $total }}`
	got := run(t, src, []int{1, 2, 3}, nil)
	if got != "6" {
		t.Errorf("got %q", got)
	}
}

func TestErrors(t *testing.T) {
	// field access on a struct via dot must fail for missing fields
	if err := execErr(t, `{{ .Name }}`, struct{}{}); err == nil {
		t.Error("expected error evaluating missing struct field via dot")
	}
	// undefined function fails at parse
	if _, err := New("t").Parse(`{{ foo(1) }}`); err == nil {
		t.Error("expected parse error for unknown function")
	}
	// break outside for fails at parse
	if _, err := New("t").Parse(`{{ break }}`); err == nil {
		t.Error("expected parse error for break outside for")
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		`{{ if true }}`,
		`{{ 1 + }}`,
		`{{ else }}`,
		`{{ end }}`,
		`{{ define "a" }}x{{ end }}{{ define "a" }}y{{ end }}`,
		`{{ foo(1) }}`,
	}
	for _, src := range bad {
		tm := New("t")
		if _, err := tm.Parse(src); err == nil {
			t.Errorf("expected parse error for %q", src)
		}
	}
}

func TestStringQuoteForms(t *testing.T) {
	if got := run(t, `{{ "a\nb" }}`, nil, nil); got != "a\nb" {
		t.Errorf("double quote got %q", got)
	}
	if got := run(t, "{{ `raw\nstr` }}", nil, nil); got != "raw\nstr" {
		t.Errorf("raw quote got %q", got)
	}
	if got := run(t, `{{ "say \"hi\"" }}`, nil, nil); got != `say "hi"` {
		t.Errorf("escaped quote got %q", got)
	}
}

func TestNilOutput(t *testing.T) {
	type u struct{ Name any }
	if got := run(t, `[{{ .Name }}]`, u{nil}, nil); got != "[<no value>]" {
		t.Errorf("nil print got %q", got)
	}
	// accessing a nonexistent field is an execution error
	if err := execErr(t, `[{{ .Missing }}]`, struct{}{}); err == nil {
		t.Error("expected error for missing field")
	}
}

func TestFuncReturningValueAndError(t *testing.T) {
	fm := FuncMap{
		"parseNum": func(s string) (int, error) {
			if s == "bad" {
				return 0, &errBad{msg: "bad number"}
			}
			return 42, nil
		},
	}
	if got := run(t, `{{ parseNum("ok") }}`, nil, fm); got != "42" {
		t.Errorf("got %q", got)
	}
	if err := execErr(t, `{{ parseNum("bad") }}`, nil, fm); err == nil ||
		!strings.Contains(err.Error(), "bad number") {
		t.Errorf("expected wrapped error, got %v", err)
	}
}

type errBad struct{ msg string }

func (e *errBad) Error() string { return e.msg }
