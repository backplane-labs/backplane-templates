package engine

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"text/template/parse"
)

// A template holds no integers. Go templates range over an int (Go 1.22),
// which is the one loop data does not bound, so the engine removes every
// way to make one: number literals are rewritten to floats when the
// template compiles, and the builtins that return or need an int (len,
// index, slice) are replaced by versions on floats. JSON data decodes
// numbers as float64 already, and range refuses a float: whatever a
// template computes, it cannot loop more times than data has elements.

// floatLiterals rewrites every number literal under node to a float.
func floatLiterals(node parse.Node) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}

		for _, child := range n.Nodes {
			floatLiterals(child)
		}
	case *parse.ActionNode:
		floatLiterals(n.Pipe)
	case *parse.IfNode, *parse.RangeNode, *parse.WithNode:
		floatBranch(branchOf(n))
	case *parse.TemplateNode:
		floatLiterals(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return
		}

		for _, cmd := range n.Cmds {
			floatLiterals(cmd)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			floatLiterals(arg)
		}
	case *parse.ChainNode:
		floatLiterals(n.Node)
	case *parse.NumberNode:
		toFloatNode(n)
	}
}

func branchOf(node parse.Node) parse.BranchNode {
	switch n := node.(type) {
	case *parse.IfNode:
		return n.BranchNode
	case *parse.RangeNode:
		return n.BranchNode
	case *parse.WithNode:
		return n.BranchNode
	}

	return parse.BranchNode{}
}

func floatBranch(b parse.BranchNode) {
	floatLiterals(b.Pipe)
	floatLiterals(b.List)
	floatLiterals(b.ElseList)
}

func toFloatNode(n *parse.NumberNode) {
	if n.IsComplex {
		return
	}

	f := n.Float64
	if !n.IsFloat {
		f = float64(n.Int64)
		if n.IsUint && !n.IsInt {
			f = float64(n.Uint64)
		}
	}

	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}

	n.IsInt, n.IsUint, n.IsFloat, n.Float64, n.Text = false, false, true, f, text
}

var (
	errIndex = errors.New("index must be a whole number")
	errLen   = errors.New("len of a value without a length")
)

// lenOf is len as a float.
func lenOf(v any) (float64, error) {
	r := reflect.ValueOf(v)

	switch r.Kind() {
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
		return float64(r.Len()), nil
	default:
		return 0, fmt.Errorf("len: %w: %T", errLen, v)
	}
}

func intOf(f any) (int, error) {
	v, err := toFloat(f)
	if err != nil || v != math.Trunc(v) || math.Abs(v) > math.MaxInt32 {
		return 0, errIndex
	}

	return int(v), nil
}

// indexOf is index with float indices: a list by position, a map by key.
func indexOf(item any, keys ...any) (any, error) {
	for _, key := range keys {
		switch v := item.(type) {
		case []any:
			i, err := intOf(key)
			if err != nil || i < 0 || i >= len(v) {
				return nil, fmt.Errorf("index %v out of range", key) //nolint:err113 // formatted
			}

			item = v[i]
		case map[string]any:
			k, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("index: map key %v is not a string", key) //nolint:err113 // formatted
			}

			item = v[k]
		default:
			return nil, fmt.Errorf("index of %T", item) //nolint:err113 // formatted
		}
	}

	return item, nil
}

// sliceOf is slice with float bounds on a list or a string.
func sliceOf(item any, bounds ...any) (any, error) {
	if len(bounds) > 2 { //nolint:mnd // from and to
		return nil, errors.New("slice: at most two bounds") //nolint:err113 // one message
	}

	var length int

	switch v := item.(type) {
	case []any:
		length = len(v)
	case string:
		length = len(v)
	default:
		return nil, fmt.Errorf("slice of %T", item) //nolint:err113 // formatted
	}

	from, until, err := bounded(bounds, length)
	if err != nil {
		return nil, err
	}

	if v, ok := item.([]any); ok {
		return v[from:until], nil
	}

	s, _ := item.(string)

	return s[from:until], nil
}

// bounded reads slice bounds within length.
func bounded(bounds []any, length int) (int, int, error) {
	from, until := 0, length

	var err error
	if len(bounds) > 0 {
		if from, err = intOf(bounds[0]); err != nil {
			return 0, 0, err
		}
	}

	if len(bounds) > 1 {
		if until, err = intOf(bounds[1]); err != nil {
			return 0, 0, err
		}
	}

	if from < 0 || until > length || from > until {
		return 0, 0, fmt.Errorf("slice [%d:%d] out of range", from, until) //nolint:err113 // formatted
	}

	return from, until, nil
}
