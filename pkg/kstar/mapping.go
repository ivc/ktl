package kstar

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
	"sigs.k8s.io/kustomize/kyaml/openapi"
	"sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/kustomize/kyaml/yaml/merge2"
	"sigs.k8s.io/kustomize/kyaml/yaml/walk"
)

const MappingNodeType = "MappingNode"

type MappingNode struct {
	schema *openapi.ResourceSchema
	value  *yaml.Node
	fields map[string]starlark.Value
}

var (
	_ starlark.Value       = new(MappingNode)
	_ starlark.HasSetField = new(MappingNode)
	_ starlark.HasSetKey   = new(MappingNode)
	_ starlark.HasBinary   = new(MappingNode)

	errUnsupportedFieldType = errors.New("unsupported field type")
)

func toMappingNode(value starlark.Value) (*MappingNode, bool) {
	node, ok := value.(*MappingNode)
	if ok {
		return node, true
	}

	ynode, err := FromStarlark(value)
	if err != nil {
		return nil, false
	}

	if ynode.Kind != yaml.MappingNode {
		return nil, false
	}

	node = &MappingNode{value: ynode}

	return node, true
}

func (node *MappingNode) String() string {
	//REVISIT: maybe return json
	panic(errNotImplemented)
}

func (node *MappingNode) Type() string {
	return MappingNodeType
}

func (node *MappingNode) Freeze() {
	//TODO: freeze node
}

func (node *MappingNode) Truth() starlark.Bool {
	return !starlark.Bool(yaml.IsYNodeNilOrEmpty(node.value))
}

func (node *MappingNode) Hash() (uint32, error) {
	panic(errNotImplemented)
}

func (node *MappingNode) loadFields() {
	if node.value == nil {
		node.fields = make(map[string]starlark.Value)
		return
	}

	content := node.value.Content
	node.fields = make(map[string]starlark.Value, len(content)/2)

	for idx := range len(content) / 2 {
		key, value := content[idx*2], content[idx*2+1]
		node.fields[key.Value] = FromYNode(value)
	}
}

func (node *MappingNode) field(name string) (starlark.Value, error) {
	if node.fields == nil {
		node.loadFields()
	}

	field, found := node.fields[name]
	if !found {
		//FIXME: return unsetNode
		return starlark.None, nil
	}

	if scalar, ok := field.(*ScalarNode); ok {
		return scalar.Value()
	}

	return field, nil
}

func (node *MappingNode) Attr(name string) (starlark.Value, error) {
	return node.field(name)
}

func (node *MappingNode) AttrNames() []string {
	if node.fields == nil {
		node.loadFields()
	}

	return slices.Sorted(maps.Keys(node.fields))
}

func (node *MappingNode) SetField(name string, value starlark.Value) error {
	if node.fields != nil {
		field := node.fields[name]
		if field == value {
			return nil
		}

		expr, ok := value.(*nodeExpr)
		if ok {
			if expr.target == field {
				return expr.evaluate()
			}
		}
	}

	newYNode, err := FromStarlark(value)
	if err != nil {
		return fmt.Errorf("unable to set %q: %w", name, err)
	}

	if node.fields != nil {
		node.fields[name] = FromYNode(newYNode)
	}

	newRNode := yaml.NewRNode(newYNode)
	keyRNode := yaml.NewStringRNode(name)
	thisRNode := yaml.NewRNode(node.value)

	return thisRNode.PipeE(yaml.MapEntrySetter{
		Name:  name,
		Key:   keyRNode,
		Value: newRNode,
	})
}

func (node *MappingNode) Get(key starlark.Value) (_ starlark.Value, found bool, _ error) {
	switch key := key.(type) {
	case starlark.String:
		value, err := node.Attr(key.GoString())
		return value, true, err
	case *MappingNode:
		//TODO: add match lookup
		panic(errNotImplemented)
	default:
		return nil, false, fmt.Errorf(
			"%w: %q",
			errUnsupportedFieldType,
			key.Type(),
		)
	}
}

func (node *MappingNode) SetKey(key, value starlark.Value) error {
	switch key := key.(type) {
	case starlark.String:
		field := key.GoString()

		return node.SetField(field, value)
	case *MappingNode:
		//TODO: add match lookup
		panic(errNotImplemented)
	default:
		return fmt.Errorf(
			"%w: %q",
			errUnsupportedFieldType,
			key.Type(),
		)
	}
}

func (node *MappingNode) clone() nodeExprTarget {
	return &MappingNode{
		schema: node.schema,
		value:  yaml.CopyYNode(node.value),
	}
}

func (node *MappingNode) Binary(op syntax.Token, value starlark.Value, side starlark.Side) (starlark.Value, error) {
	exprOp := node.exprOp(op, value, side)
	if exprOp == nil {
		return nil, nil
	}

	return &nodeExpr{target: node, ops: []nodeExprOp{exprOp}}, nil
}

func (*MappingNode) exprOp(op syntax.Token, value starlark.Value, side starlark.Side) nodeExprOp {
	if side != starlark.Left {
		return nil
	}

	right, ok := toMappingNode(value)
	if !ok {
		return nil
	}

	switch op {
	case syntax.PLUS:
		return func(target nodeExprTarget) (nodeExprTarget, error) {
			left := target.(*MappingNode)

			err := left.merge(right)
			if err != nil {
				return nil, err
			}

			return left, nil
		}
	case syntax.MINUS:
		panic(errNotImplemented)
	default:
		return nil
	}
}

func (node *MappingNode) merge(other *MappingNode) error {
	dest := yaml.NewRNode(node.value)
	src := yaml.NewRNode(other.value)

	rnode, err := walk.Walker{
		Schema:       node.schema,
		Sources:      []*yaml.RNode{dest, src},
		Visitor:      merge2.Merger{},
		MergeOptions: yaml.MergeOptions{},
	}.Walk()

	if err != nil {
		return fmt.Errorf("unable to merge values: %w", err)
	}

	node.value = rnode.YNode()
	node.fields = nil

	return nil
}
