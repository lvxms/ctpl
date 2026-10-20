// Package template implements a text templating engine whose syntax is close
// to the standard library's text/template, but whose expressions use C-style
// nested function calls and operators instead of pipelines.
//
// Example:
//
//	var t = template.Must(template.New("t").Parse(
//		"Hello, {{ upper(name) }}! Sum = {{ add(1, mul(2, 3)) }}"))
//	t.Execute(os.Stdout, map[string]any{"name": "world"})
//
// Actions are delimited by {{ and }} by default, configurable per template
// group with Template.Delims (whitespace trimming via {{- and -}} is
// supported, as are comments {{/* ... */}}). Inside an action ordinary
// C-like/Go-like expressions can be used: literals, nested function calls
// fn(a, b, ...), field chains .a.b, indexing a[i], method calls .m(args),
// variables $x, the operators
// (+ - * / % == != < <= > >= && || ! & | ^ << >> ~ ?:), and variable
// assignment with := and =.
package ctpl

import (
	"fmt"
	"strconv"
	"strings"
)

// Pos identifies a position in a (post-trim) template source; 1-based.
type Pos struct {
	Line int
	Col  int
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// ---------------------------------------------------------------------------
// Main scanner: splits raw template text into text segments and actions.
// ---------------------------------------------------------------------------

type segKind int

const (
	segText segKind = iota
	segAction
	segComment
)

type segment struct {
	kind  segKind
	text  string // literal text for segText; action body otherwise
	ltrim bool   // {{-
	rtrim bool   // -}}
	line  int
	col   int
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func advancePos(line, col *int, s string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			*line++
			*col = 1
		} else {
			*col++
		}
	}
}

// lexMain splits text into segments and performs the whitespace trimming
// requested by the trim markers ({{- and -}} for the default delimiters).
// Comment segments are removed entirely. left/right are the active action
// delimiters (e.g. "{{"/"}}" or "{"/"}").
func lexMain(text string, left, right string) ([]segment, error) {
	var raw []segment
	i, n := 0, len(text)
	for i < n {
		rel := strings.Index(text[i:], left)
		if rel < 0 {
			raw = append(raw, segment{kind: segText, text: text[i:]})
			break
		}
		if rel > 0 {
			raw = append(raw, segment{kind: segText, text: text[i : i+rel]})
			i += rel
		}
		// i points at the opening delimiter.
		seg := segment{}
		j := i + len(left)
		if j < n && text[j] == '-' {
			seg.ltrim = true
			j++
		}
		// Whitespace is permitted between the delimiter (and optional trim
		// marker) and the comment opener "/*".
		k := j
		for k < n && isSpaceByte(text[k]) {
			k++
		}
		if k+1 < n && text[k] == '/' && text[k+1] == '*' {
			// Comment action.
			seg.kind = segComment
			relEnd := strings.Index(text[k+2:], "*/")
			if relEnd < 0 {
				return nil, fmt.Errorf("unclosed comment")
			}
			k = k + 2 + relEnd + 2 // just past "*/"
			// Skip whitespace and an optional '-' then expect "}}".
			for k < n && isSpaceByte(text[k]) {
				k++
			}
			if k < n && text[k] == '-' {
				seg.rtrim = true
				k++
				for k < n && isSpaceByte(text[k]) {
					k++
				}
			}
			if k+len(right) > n || text[k:k+len(right)] != right {
				return nil, fmt.Errorf("unclosed comment")
			}
			i = k + len(right)
			raw = append(raw, seg)
			continue
		}
		// Ordinary action.
		seg.kind = segAction
		ci, err := findActionEnd(text, j, right)
		if err != nil {
			return nil, err
		}
		content := text[j:ci]
		trimmed := strings.TrimRight(content, " \t\r\n")
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '-' &&
			(len(trimmed) == 1 || isSpaceByte(trimmed[len(trimmed)-2])) {
			seg.rtrim = true
			trimmed = strings.TrimRight(trimmed[:len(trimmed)-1], " \t\r\n")
		}
		seg.text = trimmed
		raw = append(raw, seg)
		i = ci + len(right)
	}

	raw = applyTrims(raw)

	// Compute positions over the trimmed text.
	out := raw[:0:0]
	line, col := 1, 1
	for _, s := range raw {
		if s.kind == segComment {
			continue
		}
		s.line, s.col = line, col
		advancePos(&line, &col, s.text)
		out = append(out, s)
	}
	return out, nil
}

// findActionEnd returns the offset (into text) of the close delimiter (right)
// that closes the action opened at off, honouring quoted strings so that a
// delimiter sequence such as `}}` inside a string does not close the action.
func findActionEnd(text string, off int, right string) (int, error) {
	i, n := off, len(text)
	for i < n {
		c := text[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			q := c
			i++
			for i < n {
				if text[i] == '\\' && q != '`' {
					i += 2
					continue
				}
				if text[i] == q {
					break
				}
				i++
			}
			if i >= n {
				return 0, fmt.Errorf("unterminated quoted string in action")
			}
			i++
		case strings.HasPrefix(text[i:], right):
			return i, nil
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unclosed action")
}

func applyTrims(segs []segment) []segment {
	for idx, s := range segs {
		if s.ltrim {
			for k := idx - 1; k >= 0; k-- {
				if segs[k].kind == segText {
					segs[k].text = strings.TrimRight(segs[k].text, " \t\r\n")
					break
				}
			}
		}
	}
	for idx, s := range segs {
		if s.rtrim {
			for k := idx + 1; k < len(segs); k++ {
				if segs[k].kind == segText {
					segs[k].text = strings.TrimLeft(segs[k].text, " \t\r\n")
					break
				}
			}
		}
	}
	return segs
}

// ---------------------------------------------------------------------------
// Sub lexer: tokens of the expression language inside an action.
// ---------------------------------------------------------------------------

type tokKind int

const (
	tkEOF tokKind = iota
	tkIdent
	tkNumber
	tkString
	tkChar
	tkKeyword
	tkDot
	tkComma
	tkDollar
	tkOp
)

var keywords = map[string]bool{
	"if": true, "else": true, "end": true, "for": true, "with": true,
	"define": true, "block": true, "template": true, "break": true, "continue": true,
}

type token struct {
	kind tokKind
	text string
	val  any
	line int
	col  int
}

type subLexer struct {
	src  string
	i    int
	line int // relative line inside action, 1-based
	col  int // relative column inside action, 1-based
	base Pos // position where the action body starts in the template
}

func (s *subLexer) peek() rune {
	if s.i >= len(s.src) {
		return 0
	}
	return runeAt(s.src[s.i:])
}

func (s *subLexer) peekAt(off int) rune {
	k := s.i + off
	if k >= len(s.src) {
		return 0
	}
	return runeAt(s.src[k:])
}

func (s *subLexer) advance() rune {
	r, size := decodeRune(s.src[s.i:])
	s.i += size
	if r == '\n' {
		s.line++
		s.col = 1
	} else {
		s.col++
	}
	return r
}

func (s *subLexer) skipSpace() {
	for s.i < len(s.src) {
		if isSpaceByte(s.src[s.i]) {
			s.advance()
			continue
		}
		break
	}
}

// tokPos converts the internal (relative line/col) cursor position into an
// absolute template position.
func (s *subLexer) tokPos(line, col int) Pos {
	if line == 1 {
		return Pos{Line: s.base.Line, Col: s.base.Col + col - 1}
	}
	return Pos{Line: s.base.Line + line - 1, Col: col}
}

func lexSub(src string, base Pos) ([]token, error) {
	lx := &subLexer{src: src, line: 1, col: 1, base: base}
	var toks []token
	for {
		lx.skipSpace()
		if lx.i >= len(lx.src) {
			break
		}
		t, err := lx.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
	}
	return toks, nil
}

func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 0x80
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}

func (s *subLexer) next() (token, error) {
	line, col := s.line, s.col
	c := s.peek()
	abs := s.tokPos(line, col)
	switch {
	case isIdentStart(c):
		start := s.i
		for s.i < len(s.src) {
			r := s.peek()
			if !isIdentPart(r) {
				break
			}
			s.advance()
		}
		word := s.src[start:s.i]
		kind := tkIdent
		if keywords[word] {
			kind = tkKeyword
		}
		return token{kind: kind, text: word, val: word, line: abs.Line, col: abs.Col}, nil
	case c >= '0' && c <= '9':
		return s.lexNumber()
	case c == '"' || c == '`':
		return s.lexQuoted(c)
	case c == '\'':
		return s.lexChar()
	case c == '.':
		s.advance()
		return token{kind: tkDot, text: ".", line: abs.Line, col: abs.Col}, nil
	case c == ',':
		s.advance()
		return token{kind: tkComma, text: ",", line: abs.Line, col: abs.Col}, nil
	case c == '$':
		s.advance()
		return token{kind: tkDollar, text: "$", line: abs.Line, col: abs.Col}, nil
	case c == '(' || c == ')' || c == '[' || c == ']':
		s.advance()
		return token{kind: tkOp, text: string(c), line: abs.Line, col: abs.Col}, nil
	default:
		return s.lexOp()
	}
}

func (s *subLexer) lexOp() (token, error) {
	line, col := s.line, s.col
	abs := s.tokPos(line, col)
	if s.i+1 < len(s.src) {
		two := s.src[s.i : s.i+2]
		switch two {
		case "==", "!=", "<=", ">=", "&&", "||", "<<", ">>", ":=":
			s.advance()
			s.advance()
			return token{kind: tkOp, text: two, line: abs.Line, col: abs.Col}, nil
		}
	}
	c := s.peek()
	switch c {
	case '+', '-', '*', '/', '%', '!', '<', '>', '&', '|', '^', '~', '?', ':', '=':
		s.advance()
		return token{kind: tkOp, text: string(c), line: abs.Line, col: abs.Col}, nil
	}
	return token{}, fmt.Errorf("unexpected character %q", string(c))
}

func (s *subLexer) lexQuoted(q rune) (token, error) {
	abs := s.tokPos(s.line, s.col)
	s.advance() // opening quote
	if q == '`' {
		start := s.i
		for s.i < len(s.src) && s.peek() != '`' {
			s.advance()
		}
		if s.i >= len(s.src) {
			return token{}, fmt.Errorf("unterminated raw string")
		}
		val := s.src[start:s.i]
		s.advance()
		return token{kind: tkString, text: val, val: val, line: abs.Line, col: abs.Col}, nil
	}
	var sb strings.Builder
	for {
		if s.i >= len(s.src) {
			return token{}, fmt.Errorf("unterminated string")
		}
		c := s.peek()
		if c == '"' {
			s.advance()
			break
		}
		if c == '\\' {
			s.advance()
			esc := s.peek()
			s.advance()
			switch esc {
			case 'x', 'u', 'U':
				need := 2
				if esc == 'u' {
					need = 4
				} else if esc == 'U' {
					need = 8
				}
				v := 0
				for k := 0; k < need; k++ {
					h := hexVal(s.peek())
					if h < 0 {
						return token{}, fmt.Errorf("invalid hexadecimal escape")
					}
					v = v*16 + h
					s.advance()
				}
				sb.WriteRune(rune(v))
			default:
				sb.WriteRune(unescape(esc))
			}
			continue
		}
		sb.WriteRune(s.advance())
	}
	return token{kind: tkString, text: sb.String(), val: sb.String(), line: abs.Line, col: abs.Col}, nil
}

func hexVal(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

func (s *subLexer) lexChar() (token, error) {
	abs := s.tokPos(s.line, s.col)
	s.advance() // opening '
	if s.i >= len(s.src) {
		return token{}, fmt.Errorf("unterminated character literal")
	}
	var r rune
	c := s.peek()
	if c == '\\' {
		s.advance()
		esc := s.peek()
		s.advance()
		if esc == 'x' || esc == 'u' || esc == 'U' {
			return token{}, fmt.Errorf("complex escapes not allowed in character literals")
		}
		r = unescape(esc)
	} else {
		r = s.advance()
	}
	if s.i >= len(s.src) || s.peek() != '\'' {
		return token{}, fmt.Errorf("character literal must contain exactly one character")
	}
	s.advance()
	return token{kind: tkChar, text: string(r), val: r, line: abs.Line, col: abs.Col}, nil
}

func unescape(esc rune) rune {
	switch esc {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'v':
		return '\v'
	case 'f':
		return '\f'
	case 'b':
		return '\b'
	case 'a':
		return '\a'
	case '\\':
		return '\\'
	case '\'':
		return '\''
	case '"':
		return '"'
	case '0':
		return 0
	}
	return esc
}

func (s *subLexer) lexNumber() (token, error) {
	abs := s.tokPos(s.line, s.col)
	start := s.i
	isFloat := false
	if s.peek() == '0' && (s.peekAt(1) == 'x' || s.peekAt(1) == 'X') {
		s.advance()
		s.advance()
		for isHexRune(s.peek()) {
			s.advance()
		}
		lit := s.src[start:s.i]
		val, err := parseIntLit(lit, 0)
		if err != nil {
			return token{}, err
		}
		return token{kind: tkNumber, text: lit, val: val, line: abs.Line, col: abs.Col}, nil
	}
	for s.i < len(s.src) {
		c := s.peek()
		switch {
		case c >= '0' && c <= '9' || c == '_':
			s.advance()
		case c == '.':
			isFloat = true
			s.advance()
		case c == 'e' || c == 'E':
			isFloat = true
			s.advance()
			if s.peek() == '+' || s.peek() == '-' {
				s.advance()
			}
		default:
			goto done
		}
	}
done:
	lit := s.src[start:s.i]
	if isFloat {
		f, err := parseFloatLit(lit)
		if err != nil {
			return token{}, err
		}
		return token{kind: tkNumber, text: lit, val: f, line: abs.Line, col: abs.Col}, nil
	}
	val, err := parseIntLit(lit, 10)
	if err != nil {
		return token{}, err
	}
	return token{kind: tkNumber, text: lit, val: val, line: abs.Line, col: abs.Col}, nil
}

func isHexRune(r rune) bool { return hexVal(r) >= 0 }

func parseIntLit(lit string, base int) (any, error) {
	clean := strings.ReplaceAll(lit, "_", "")
	switch base {
	case 0:
		// hex "0x..." prefix already included.
		if v, err := strconv.ParseUint(clean[2:], 16, 64); err == nil {
			if v <= 1<<63-1 {
				return int64(v), nil
			}
			return v, nil
		}
		return nil, fmt.Errorf("invalid integer literal %q", lit)
	default:
		if v, err := strconv.ParseInt(clean, 10, 64); err == nil {
			return v, nil
		}
		if u, err := strconv.ParseUint(clean, 10, 64); err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("invalid integer literal %q", lit)
	}
}

func parseFloatLit(lit string) (float64, error) {
	clean := strings.ReplaceAll(lit, "_", "")
	if !strings.ContainsAny(clean, ".eE") {
		return 0, fmt.Errorf("invalid float literal %q", lit)
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float literal %q", lit)
	}
	return v, nil
}

// decodeRune returns the first rune of s and its byte length.
func decodeRune(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

func runeAt(s string) rune {
	r, _ := decodeRune(s)
	return r
}
