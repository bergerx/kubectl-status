// Package calicoselector evaluates Calico's label-selector expression language
// (e.g. `app == 'api' && has(tier)`), used by projectcalico.org/v3 NetworkPolicy
// and GlobalNetworkPolicy spec.selector/spec.namespaceSelector. Unlike upstream
// NetworkPolicy's podSelector, this is not a Kubernetes LabelSelector -- it's a
// small boolean expression language with its own operators (==, !=, in, not in,
// has(), !has(), contains, starts with, ends with, all(), &&, ||, !, parens).
//
// This is adapted from Calico's own parser/AST at
// github.com/projectcalico/calico/libcalico-go/lib/selector (parser and tokenizer
// sub-packages), commit 69a90ed878d5c1f617214977593b9b85d89bfdbd, rather than
// re-implemented from scratch, so that kubectl-status's notion of "this selector
// matches these labels" agrees with Calico's own evaluation -- getting Calico's
// operator precedence/negation semantics subtly wrong (e.g. what `!=` and
// `not in` mean when the label is entirely absent) would silently misreport
// which policies select a Pod.
//
// It is a straight port, not a `go get`-able dependency: libcalico-go is not
// independently module-versioned inside the github.com/projectcalico/calico
// monorepo (no nested go.mod), so importing it would pin this whole project to
// Calico's module graph just to reuse a few hundred lines of parsing logic.
// Compared to the upstream source, this port drops:
//   - the uniquestr/hash-based string interning and Selector.UniqueID/Equal --
//     those exist upstream to make repeated evaluation of many long-lived
//     selectors cheap inside Calico's Felix dataplane; kubectl-status parses a
//     handful of selectors once per render, so plain strings are simpler and
//     just as correct.
//   - the Visitor/PrefixVisitor and LabelRestrictions machinery -- used
//     upstream for policy-program optimization, unused by a Parse+Evaluate
//     caller.
//   - all logrus debug logging -- gated upstream behind compile-time-false
//     `parserDebug`/`tokenizerDebug` consts, i.e. already dead code there.
//
// The tokenizing/parsing/evaluation logic itself (operator semantics, error
// cases) is preserved as-is. Each file below carries Calico's original
// Apache-2.0 copyright header.
package calicoselector

import (
	"errors"
	"fmt"
	"strings"
)

// Selector is a parsed Calico selector expression.
type Selector struct {
	root node
	expr string
}

// Parse parses a Calico selector expression. An empty string is equivalent to
// "all()" -- it matches everything, per Calico's documented default.
func Parse(selector string) (*Selector, error) {
	tokens, err := tokenize(selector)
	if err != nil {
		return nil, err
	}
	var n node
	if tokens[0].kind == tokEOF {
		n = allNode{}
	} else {
		n, tokens, err = parseOrExpression(tokens)
		if err != nil {
			return nil, err
		}
		if len(tokens) != 1 || tokens[0].kind != tokEOF {
			return nil, fmt.Errorf("unexpected content at end of selector: %v", tokens)
		}
	}
	rep := strings.TrimSpace(selector)
	if rep == "" {
		rep = "all()"
	}
	return &Selector{root: n, expr: rep}, nil
}

// Evaluate reports whether labels satisfies this selector.
func (s *Selector) Evaluate(labels map[string]string) bool {
	if s == nil {
		return false
	}
	return s.root.evaluate(labels)
}

// String returns the original (trimmed) selector expression, or "all()" for
// an originally-empty selector.
func (s *Selector) String() string {
	if s == nil {
		return ""
	}
	return s.expr
}

// --- AST ---

type node interface {
	evaluate(labels map[string]string) bool
}

type labelEqValueNode struct{ label, value string }

func (n labelEqValueNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	return ok && v == n.value
}

type labelNeValueNode struct{ label, value string }

func (n labelNeValueNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	if !ok {
		// Absent label is considered "not equal" -- matches Calico's semantics.
		return true
	}
	return v != n.value
}

type labelContainsValueNode struct{ label, value string }

func (n labelContainsValueNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	return ok && strings.Contains(v, n.value)
}

type labelStartsWithValueNode struct{ label, value string }

func (n labelStartsWithValueNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	return ok && strings.HasPrefix(v, n.value)
}

type labelEndsWithValueNode struct{ label, value string }

func (n labelEndsWithValueNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	return ok && strings.HasSuffix(v, n.value)
}

type labelInSetNode struct {
	label string
	set   map[string]struct{}
}

func (n labelInSetNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	if !ok {
		return false
	}
	_, in := n.set[v]
	return in
}

type labelNotInSetNode struct {
	label string
	set   map[string]struct{}
}

func (n labelNotInSetNode) evaluate(labels map[string]string) bool {
	v, ok := labels[n.label]
	if !ok {
		// Absent label is considered "not in" any set -- matches Calico's semantics.
		return true
	}
	_, in := n.set[v]
	return !in
}

type hasNode struct{ label string }

func (n hasNode) evaluate(labels map[string]string) bool {
	_, ok := labels[n.label]
	return ok
}

type notNode struct{ operand node }

func (n notNode) evaluate(labels map[string]string) bool {
	return !n.operand.evaluate(labels)
}

type andNode struct{ operands []node }

func (n andNode) evaluate(labels map[string]string) bool {
	for _, op := range n.operands {
		if !op.evaluate(labels) {
			return false
		}
	}
	return true
}

type orNode struct{ operands []node }

func (n orNode) evaluate(labels map[string]string) bool {
	for _, op := range n.operands {
		if op.evaluate(labels) {
			return true
		}
	}
	return false
}

// allNode matches everything -- the "all()" selector and the default for "".
type allNode struct{}

func (allNode) evaluate(map[string]string) bool { return true }

// globalNode matches everything -- the "global()" selector (host endpoints not
// bound to any specific set of labels). Treated the same as all() here since
// kubectl-status only evaluates selectors against Pod/Namespace labels, never
// against Calico host endpoints.
type globalNode struct{}

func (globalNode) evaluate(map[string]string) bool { return true }

// --- tokenizer ---

type tokKind int

const (
	tokNone tokKind = iota
	tokLabel
	tokStringLiteral
	tokLBrace
	tokRBrace
	tokComma
	tokEq
	tokNe
	tokIn
	tokNot
	tokNotIn
	tokContains
	tokStartsWith
	tokEndsWith
	tokAll
	tokHas
	tokLParen
	tokRParen
	tokAnd
	tokOr
	tokGlobal
	tokEOF
)

const maxLabelLength = 512

type token struct {
	kind  tokKind
	value string
}

func (t token) String() string {
	return fmt.Sprintf("%d(%s)", t.kind, t.value)
}

func tokenize(input string) ([]token, error) {
	var tokens []token
	for {
		startLen := len(input)
		input = trimWhitespace(input)
		if len(input) == 0 {
			tokens = append(tokens, token{kind: tokEOF})
			return tokens, nil
		}
		var lastKind = tokNone
		if len(tokens) > 0 {
			lastKind = tokens[len(tokens)-1].kind
		}
		var found bool
		switch input[0] {
		case '(':
			tokens = append(tokens, token{kind: tokLParen})
			input = input[1:]
		case ')':
			tokens = append(tokens, token{kind: tokRParen})
			input = input[1:]
		case '"':
			input = input[1:]
			idx := strings.Index(input, `"`)
			if idx == -1 {
				return nil, errors.New("unterminated string")
			}
			tokens = append(tokens, token{tokStringLiteral, input[:idx]})
			input = input[idx+1:]
		case '\'':
			input = input[1:]
			idx := strings.Index(input, `'`)
			if idx == -1 {
				return nil, errors.New("unterminated string")
			}
			tokens = append(tokens, token{tokStringLiteral, input[:idx]})
			input = input[idx+1:]
		case '{':
			tokens = append(tokens, token{kind: tokLBrace})
			input = input[1:]
		case '}':
			tokens = append(tokens, token{kind: tokRBrace})
			input = input[1:]
		case ',':
			tokens = append(tokens, token{kind: tokComma})
			input = input[1:]
		case '=':
			if input, found = strings.CutPrefix(input, "=="); found {
				tokens = append(tokens, token{kind: tokEq})
			} else {
				return nil, errors.New("expected ==")
			}
		case '!':
			if input, found = strings.CutPrefix(input, "!="); found {
				tokens = append(tokens, token{kind: tokNe})
			} else {
				tokens = append(tokens, token{kind: tokNot})
				input = input[1:]
			}
		case '&':
			if input, found = strings.CutPrefix(input, "&&"); found {
				tokens = append(tokens, token{kind: tokAnd})
			} else {
				return nil, errors.New("expected &&")
			}
		case '|':
			if input, found = strings.CutPrefix(input, "||"); found {
				tokens = append(tokens, token{kind: tokOr})
			} else {
				return nil, errors.New("expected ||")
			}
		default:
			var ident string
			var err error
			if lastKind == tokLabel {
				if input, found = cutPrefixCheckBreak(input, "contains"); found {
					tokens = append(tokens, token{kind: tokContains})
				} else if input, found = cutMultiWordPrefixCheckBreak(input, "starts", "with"); found {
					tokens = append(tokens, token{kind: tokStartsWith})
				} else if input, found = cutMultiWordPrefixCheckBreak(input, "ends", "with"); found {
					tokens = append(tokens, token{kind: tokEndsWith})
				} else if input, found = cutMultiWordPrefixCheckBreak(input, "not", "in"); found {
					tokens = append(tokens, token{kind: tokNotIn})
				} else if input, found = cutPrefixCheckBreak(input, "in"); found {
					tokens = append(tokens, token{kind: tokIn})
				} else {
					return nil, fmt.Errorf("expected operator after label %q", tokens[len(tokens)-1].value)
				}
			} else if input, found = strings.CutPrefix(input, "has("); found {
				input = trimWhitespace(input)
				if ident, input, err = cutIdentifier(input); err != nil {
					return nil, err
				}
				input = trimWhitespace(input)
				if input, found = strings.CutPrefix(input, ")"); found {
					tokens = append(tokens, token{tokHas, ident})
				} else {
					return nil, errors.New("no closing ')' after has(")
				}
			} else if input, found = strings.CutPrefix(input, "all("); found {
				input = trimWhitespace(input)
				if input, found = strings.CutPrefix(input, ")"); found {
					tokens = append(tokens, token{kind: tokAll})
				} else {
					return nil, errors.New("no closing ')' after all(")
				}
			} else if input, found = strings.CutPrefix(input, "global("); found {
				input = trimWhitespace(input)
				if input, found = strings.CutPrefix(input, ")"); found {
					tokens = append(tokens, token{kind: tokGlobal})
				} else {
					return nil, errors.New("no closing ')' after global(")
				}
			} else if ident, input, err = cutIdentifier(input); err == nil {
				tokens = append(tokens, token{tokLabel, ident})
			} else {
				return nil, err
			}
		}
		if len(input) >= startLen {
			return nil, errors.New("infinite loop detected in tokenizer")
		}
	}
}

func trimWhitespace(input string) string {
	end := 0
	for ; end < len(input); end++ {
		if input[end] == ' ' || input[end] == '\t' {
			continue
		}
		break
	}
	return input[end:]
}

func cutPrefixCheckBreak(input, prefix string) (string, bool) {
	remainder, found := strings.CutPrefix(input, prefix)
	if !found || !isWordBoundary(remainder) {
		return input, false
	}
	return remainder, true
}

func cutMultiWordPrefixCheckBreak(input string, words ...string) (string, bool) {
	remainder := input
	for _, word := range words {
		var found bool
		if remainder, found = strings.CutPrefix(remainder, word); !found {
			return input, false
		}
		remainder = trimWhitespace(remainder)
	}
	if !isWordBoundary(remainder) {
		return input, false
	}
	return remainder, true
}

func isWordBoundary(in string) bool {
	if in == "" {
		return true
	}
	return !identifierChar(in[0])
}

func cutIdentifier(in string) (ident, remainder string, err error) {
	defer func() {
		if len(ident) > maxLabelLength {
			err = fmt.Errorf("label too long: %s", ident)
			ident = ""
		} else if len(ident) == 0 {
			err = errors.New("expected identifier")
		}
	}()
	for i := 0; i < len(in); i++ {
		if identifierChar(in[i]) {
			continue
		}
		return in[:i], in[i:], nil
	}
	return in, "", nil
}

func identifierChar(c uint8) bool {
	return c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' ||
		c == '_' ||
		c == '.' ||
		c == '/' ||
		c == '-'
}

// --- parser ---

var (
	errUnexpectedEOF  = errors.New("unexpected end of string looking for op")
	errExpectedRParen = errors.New("expected )")
	errExpectedRBrace = errors.New("expected }")
	errExpectedString = errors.New("expected string")
	errExpectedSetLit = errors.New("expected set literal")
)

// parseOrExpression parses one or more "&&" terms, separated by "||" operators.
func parseOrExpression(tokens []token) (n node, remTokens []token, err error) {
	var orOperands []node
	n, remTokens, err = parseAndExpression(tokens)
	if err != nil {
		return nil, nil, err
	}
	orOperands = append(orOperands, n)
	for {
		if remTokens[0].kind != tokOr {
			break
		}
		remTokens = remTokens[1:]
		n, remTokens, err = parseAndExpression(remTokens)
		if err != nil {
			return nil, nil, err
		}
		orOperands = append(orOperands, n)
	}
	if len(orOperands) == 1 {
		return orOperands[0], remTokens, nil
	}
	return orNode{orOperands}, remTokens, nil
}

// parseAndExpression parses one or more operations, separated by "&&" operators.
func parseAndExpression(tokens []token) (n node, remTokens []token, err error) {
	var andOperands []node
	n, remTokens, err = parseOperation(tokens)
	if err != nil {
		return nil, nil, err
	}
	andOperands = append(andOperands, n)
	for {
		if remTokens[0].kind != tokAnd {
			break
		}
		remTokens = remTokens[1:]
		n, remTokens, err = parseOperation(remTokens)
		if err != nil {
			return nil, nil, err
		}
		andOperands = append(andOperands, n)
	}
	if len(andOperands) == 1 {
		return andOperands[0], remTokens, nil
	}
	return andNode{andOperands}, remTokens, nil
}

// parseOperation parses a single, possibly negated operation (==, !=, has(), ...),
// or a parenthesized sub-expression.
func parseOperation(tokens []token) (n node, remTokens []token, err error) {
	if len(tokens) == 0 {
		return nil, nil, errUnexpectedEOF
	}
	negated := false
	for len(tokens) > 0 && tokens[0].kind == tokNot {
		negated = !negated
		tokens = tokens[1:]
	}
	switch tokens[0].kind {
	case tokHas:
		n = hasNode{tokens[0].value}
		remTokens = tokens[1:]
	case tokAll:
		n = allNode{}
		remTokens = tokens[1:]
	case tokGlobal:
		n = globalNode{}
		remTokens = tokens[1:]
	case tokLabel:
		if len(tokens) < 3 {
			return nil, nil, errUnexpectedEOF
		}
		label := tokens[0].value
		switch tokens[1].kind {
		case tokEq:
			if tokens[2].kind != tokStringLiteral {
				return nil, nil, errExpectedString
			}
			n = labelEqValueNode{label, tokens[2].value}
			remTokens = tokens[3:]
		case tokNe:
			if tokens[2].kind != tokStringLiteral {
				return nil, nil, errExpectedString
			}
			n = labelNeValueNode{label, tokens[2].value}
			remTokens = tokens[3:]
		case tokContains:
			if tokens[2].kind != tokStringLiteral {
				return nil, nil, errExpectedString
			}
			n = labelContainsValueNode{label, tokens[2].value}
			remTokens = tokens[3:]
		case tokStartsWith:
			if tokens[2].kind != tokStringLiteral {
				return nil, nil, errExpectedString
			}
			n = labelStartsWithValueNode{label, tokens[2].value}
			remTokens = tokens[3:]
		case tokEndsWith:
			if tokens[2].kind != tokStringLiteral {
				return nil, nil, errExpectedString
			}
			n = labelEndsWithValueNode{label, tokens[2].value}
			remTokens = tokens[3:]
		case tokIn, tokNotIn:
			if tokens[2].kind != tokLBrace {
				return nil, nil, errExpectedSetLit
			}
			remTokens = tokens[3:]
			set := map[string]struct{}{}
			for {
				if len(remTokens) == 0 || remTokens[0].kind != tokStringLiteral {
					break
				}
				set[remTokens[0].value] = struct{}{}
				remTokens = remTokens[1:]
				if len(remTokens) > 0 && remTokens[0].kind == tokComma {
					remTokens = remTokens[1:]
				} else {
					break
				}
			}
			if len(remTokens) == 0 || remTokens[0].kind != tokRBrace {
				return nil, nil, errExpectedRBrace
			}
			remTokens = remTokens[1:]
			if tokens[1].kind == tokIn {
				n = labelInSetNode{label, set}
			} else {
				n = labelNotInSetNode{label, set}
			}
		default:
			return nil, nil, fmt.Errorf("expected == or != not: %v", tokens[1])
		}
	case tokLParen:
		n, remTokens, err = parseOrExpression(tokens[1:])
		if err != nil {
			return nil, nil, err
		}
		if len(remTokens) < 1 || remTokens[0].kind != tokRParen {
			return nil, nil, errExpectedRParen
		}
		remTokens = remTokens[1:]
	default:
		return nil, nil, fmt.Errorf("unexpected token: %v", tokens[0])
	}
	if negated {
		n = notNode{n}
	}
	return n, remTokens, nil
}
