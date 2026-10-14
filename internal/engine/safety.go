package engine

import (
	"errors"
	"fmt"
	"text/template/parse"
)

// What a template may do. A Go template cannot be interrupted, so the
// engine refuses up front what could loop without bound:
//
//   - {{range}} iterates only over data: a field (.items, $.a.b, $e.x), a
//     variable holding data, never a number (Go 1.22 ranges over ints) nor
//     a function's result; JSON numbers decode as float64, which range
//     refuses, so data cannot smuggle an int in;
//   - ranges nest at most MaxRangeDepth deep, counted through included
//     shared pieces, and data holds at most MaxDataItems elements, so the
//     iterations of one render stay under MaxDataItems^MaxRangeDepth;
//   - parts and shared pieces define no templates of their own and shared
//     pieces include each other without cycles, so nothing recurses.
//
// Output (MaxOutputBytes), time (RenderTimeout) and concurrent renders
// (slots) are bounded at render.
const (
	MaxRangeDepth = 2
	MaxDataItems  = 10000
)

// Errors of the checks.
var (
	ErrRangeSource = errors.New("range iterates only over data: a field or a variable holding one")
	ErrRangeDepth  = errors.New("ranges nest at most 2 deep")
	ErrDefine      = errors.New("define and block are not allowed: use a shared piece")
	ErrCycle       = errors.New("shared pieces include each other in a cycle")
	ErrDataItems   = errors.New("template: data holds more than 10000 elements")
)

type treeOf struct {
	root    *parse.ListNode
	defined int
}

type checker struct {
	lookup   func(string) *treeOf
	visiting map[string]bool
	// reassigned are variables some {{$x = ...}} in the set changes: an
	// assignment can run after a range that reads the variable (the next
	// iteration of an outer range), so such a variable never counts as data.
	reassigned map[string]bool
}

// checkSet checks the part named root with every template of its set
// (the part and the shared pieces it can include).
func checkSet(root string, declared int, lookup func(string) *treeOf) error {
	tree := lookup(root)
	if tree == nil {
		return nil
	}

	if tree.defined > declared {
		return ErrDefine
	}

	c := checker{lookup: lookup, visiting: map[string]bool{root: true}, reassigned: map[string]bool{}}
	c.collect(tree.root, map[string]bool{root: true})

	return c.list(tree.root, 0, map[string]bool{"$": true, ".": true})
}

// collect gathers the variables reassigned anywhere in the part and the
// shared pieces it reaches.
func (c checker) collect(node parse.Node, seen map[string]bool) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}

		for _, child := range n.Nodes {
			c.collect(child, seen)
		}
	case *parse.ActionNode:
		if n.Pipe != nil && n.Pipe.IsAssign {
			for _, v := range n.Pipe.Decl {
				c.reassigned[v.Ident[0]] = true
			}
		}
	case *parse.RangeNode:
		c.collect(n.List, seen)
		c.collect(n.ElseList, seen)
	case *parse.IfNode:
		c.collect(n.List, seen)
		c.collect(n.ElseList, seen)
	case *parse.WithNode:
		c.collect(n.List, seen)
		c.collect(n.ElseList, seen)
	case *parse.TemplateNode:
		if seen[n.Name] {
			return
		}

		seen[n.Name] = true

		if tree := c.lookup(n.Name); tree != nil {
			c.collect(tree.root, seen)
		}
	}
}

// list walks nodes; vars says which variables hold data.
func (c checker) list(list *parse.ListNode, depth int, vars map[string]bool) error {
	if list == nil {
		return nil
	}

	for _, node := range list.Nodes {
		if err := c.node(node, depth, vars); err != nil {
			return err
		}
	}

	return nil
}

func (c checker) node(node parse.Node, depth int, vars map[string]bool) error {
	switch n := node.(type) {
	case *parse.ActionNode:
		c.assign(n.Pipe, vars)
	case *parse.RangeNode:
		return c.rangeNode(n, depth, vars)
	case *parse.IfNode:
		inner := scope(vars)
		c.declare(n.Pipe, inner, c.isData(n.Pipe, vars))

		return errors.Join(c.list(n.List, depth, inner), c.list(n.ElseList, depth, scope(vars)))
	case *parse.WithNode:
		inner := scope(vars)
		data := c.isData(n.Pipe, vars)
		c.declare(n.Pipe, inner, data)
		inner["."] = data

		return errors.Join(c.list(n.List, depth, inner), c.list(n.ElseList, depth, scope(vars)))
	case *parse.TemplateNode:
		return c.include(n.Name, depth, n.Pipe == nil || c.isData(n.Pipe, vars))
	}

	return nil
}

func (c checker) rangeNode(n *parse.RangeNode, depth int, vars map[string]bool) error {
	if !c.isData(n.Pipe, vars) {
		return ErrRangeSource
	}

	if depth+1 > MaxRangeDepth {
		return ErrRangeDepth
	}

	inner := scope(vars)
	c.declare(n.Pipe, inner, true)
	inner["."] = true

	// {{range $i, $e := ...}}: $i is an index or a key, not data.
	if len(n.Pipe.Decl) == 2 { //nolint:mnd // index and element
		inner[n.Pipe.Decl[0].Ident[0]] = false
	}

	return errors.Join(c.list(n.List, depth+1, inner), c.list(n.ElseList, depth, scope(vars)))
}

// include follows {{template "name"}} into a shared piece with the
// current depth: a range around an include counts inside it.
func (c checker) include(name string, depth int, data bool) error {
	if c.visiting[name] {
		return fmt.Errorf("%w: %s", ErrCycle, name)
	}

	tree := c.lookup(name)
	if tree == nil {
		return nil // the executor reports an undefined template
	}

	c.visiting[name] = true
	defer delete(c.visiting, name)

	// Dot and $ in the piece are the include's argument.
	return c.list(tree.root, depth, map[string]bool{"$": data, ".": data})
}

// isData: the pipeline is one command of one field or data variable.
func (c checker) isData(pipe *parse.PipeNode, vars map[string]bool) bool {
	if pipe == nil || len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) != 1 {
		return false
	}

	return c.dataNode(pipe.Cmds[0].Args[0], vars)
}

// dataNode: a field of data, dot holding data, or a variable holding data
// that nothing reassigns. A field of a number fails at render, so only
// the root of a chain needs to be data.
func (c checker) dataNode(node parse.Node, vars map[string]bool) bool {
	switch n := node.(type) {
	case *parse.FieldNode:
		return vars["."]
	case *parse.DotNode:
		return vars["."]
	case *parse.VariableNode:
		return vars[n.Ident[0]] && !c.reassigned[n.Ident[0]]
	case *parse.ChainNode:
		return c.dataNode(n.Node, vars)
	}

	return false
}

// assign records {{$x := ...}} and {{$x = ...}}: data or not.
func (c checker) assign(pipe *parse.PipeNode, vars map[string]bool) {
	if pipe == nil || len(pipe.Decl) == 0 {
		return
	}

	c.declare(pipe, vars, c.isData(pipe, vars))
}

func (c checker) declare(pipe *parse.PipeNode, vars map[string]bool, data bool) {
	if pipe == nil {
		return
	}

	for _, v := range pipe.Decl {
		vars[v.Ident[0]] = data
	}
}

// scope is a nested scope: a declaration inside does not leak out.
func scope(vars map[string]bool) map[string]bool {
	inner := make(map[string]bool, len(vars))
	for k, v := range vars {
		inner[k] = v
	}

	return inner
}

// CheckData bounds the elements a template can iterate over.
func CheckData(data map[string]any) error {
	count := 0

	var walk func(v any) error

	walk = func(v any) error {
		switch x := v.(type) {
		case []any:
			count += len(x)

			for _, item := range x {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			count += len(x)

			for _, item := range x {
				if err := walk(item); err != nil {
					return err
				}
			}
		}

		if count > MaxDataItems {
			return ErrDataItems
		}

		return nil
	}

	return walk(any(data))
}
