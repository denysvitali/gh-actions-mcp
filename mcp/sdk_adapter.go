package mcp

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolBuilder keeps the existing schema declarations readable while producing
// schemas understood by the official SDK. It carries no state: the methods are
// namespaced onto it so that a declaration in tools_*.go reads
// b.WithString("owner", b.Description("…")).
type toolBuilder struct{}

// toolDefinition accumulates one tool while its options are applied.
type toolDefinition struct {
	tool       *sdkmcp.Tool
	properties map[string]any
	required   []string
}

// propertyDefinition accumulates one input-schema property while its options are
// applied.
type propertyDefinition struct {
	property map[string]any
	required bool
}

// toolOption configures a tool as a whole: its description, its annotations, or
// the addition of one input property.
type toolOption func(*toolDefinition)

// propertyOption configures a single input-schema property.
//
// It is deliberately a distinct type from toolOption: the two sets are not
// interchangeable, and separating them means passing a property option at tool
// level (or a tool option to WithString) fails to compile instead of being
// silently discarded at runtime.
type propertyOption func(*propertyDefinition)

// NewTool builds a tool with an object input schema. Property order does not
// reach the wire — the schema is a map, so JSON encoding sorts the keys — but
// the "required" list preserves declaration order.
func (toolBuilder) NewTool(name string, options ...toolOption) *sdkmcp.Tool {
	d := &toolDefinition{
		tool:       &sdkmcp.Tool{Name: name},
		properties: make(map[string]any),
	}
	for _, option := range options {
		option(d)
	}
	schema := map[string]any{
		"type":       "object",
		"properties": d.properties,
	}
	if len(d.required) > 0 {
		schema["required"] = d.required
	}
	d.tool.InputSchema = schema
	return d.tool
}

func (toolBuilder) WithDescription(description string) toolOption {
	return func(d *toolDefinition) {
		d.tool.Description = description
	}
}

// ReadOnly marks a tool as making no observable change to the repository.
func (toolBuilder) ReadOnly() toolOption {
	return func(d *toolDefinition) {
		if d.tool.Annotations == nil {
			d.tool.Annotations = &sdkmcp.ToolAnnotations{}
		}
		d.tool.Annotations.ReadOnlyHint = true
		d.tool.Annotations.DestructiveHint = boolPtr(false)
		d.tool.Annotations.OpenWorldHint = boolPtr(true)
	}
}

// Destructive marks a tool as mutating remote state or the local filesystem.
func (toolBuilder) Destructive() toolOption {
	return func(d *toolDefinition) {
		if d.tool.Annotations == nil {
			d.tool.Annotations = &sdkmcp.ToolAnnotations{}
		}
		d.tool.Annotations.DestructiveHint = boolPtr(true)
		d.tool.Annotations.OpenWorldHint = boolPtr(true)
	}
}

// combine applies several tool options as one, so a group of arguments that
// always travel together can be declared in a single place.
func combine(options ...toolOption) toolOption {
	return func(d *toolDefinition) {
		for _, option := range options {
			option(d)
		}
	}
}

// repoOverrides declares the optional "owner" and "repo" arguments that let a
// single call target a repository other than the configured one. Every tool
// accepts them, and it is one rule rather than twelve: a tool either supports
// per-call repository overrides or it does not.
func (b toolBuilder) repoOverrides() toolOption {
	return combine(
		b.WithString("owner",
			b.Description("Optional: override repository owner for this call"),
		),
		b.WithString("repo",
			b.Description("Optional: override repository name for this call"),
		),
	)
}

func boolPtr(value bool) *bool { return &value }

func (toolBuilder) Description(description string) propertyOption {
	return func(p *propertyDefinition) {
		p.property["description"] = description
	}
}

func (toolBuilder) Required() propertyOption {
	return func(p *propertyDefinition) {
		p.required = true
	}
}

func (toolBuilder) DefaultString(value string) propertyOption {
	return func(p *propertyDefinition) {
		p.property["default"] = value
	}
}

func (toolBuilder) DefaultNumber(value float64) propertyOption {
	return func(p *propertyDefinition) {
		p.property["default"] = value
	}
}

// Enum restricts a property to the given values, in the given order. The order
// is part of the wire contract.
func (toolBuilder) Enum(values ...string) propertyOption {
	return func(p *propertyDefinition) {
		p.property["enum"] = values
	}
}

func (toolBuilder) Minimum(value float64) propertyOption {
	return func(p *propertyDefinition) {
		p.property["minimum"] = value
	}
}

func (toolBuilder) Maximum(value float64) propertyOption {
	return func(p *propertyDefinition) {
		p.property["maximum"] = value
	}
}

// property adds one named property to the tool's input schema. A nil typ omits
// the "type" keyword, which is how an argument that accepts several JSON types
// is declared.
func (toolBuilder) property(name string, typ any, options ...propertyOption) toolOption {
	return func(d *toolDefinition) {
		property := map[string]any{}
		if typ != nil {
			property["type"] = typ
		}
		definition := &propertyDefinition{property: property}
		for _, option := range options {
			option(definition)
		}
		d.properties[name] = property
		if definition.required {
			d.required = append(d.required, name)
		}
	}
}

func (b toolBuilder) WithString(name string, options ...propertyOption) toolOption {
	return b.property(name, "string", options...)
}

func (b toolBuilder) WithAny(name string, options ...propertyOption) toolOption {
	return b.property(name, nil, options...)
}

func (b toolBuilder) WithNumber(name string, options ...propertyOption) toolOption {
	return b.property(name, "integer", options...)
}

func (b toolBuilder) WithBoolean(name string, options ...propertyOption) toolOption {
	return b.property(name, "boolean", options...)
}

// addTool registers a tool together with its end-to-end typed handler. Binding
// both in one call is what guarantees a declared tool cannot be served without a
// handler, and the handler's input type is what the SDK decodes and validates
// against before the business logic runs — the legacy wire request is never
// reconstructed.
func addTool[In, Out any](s *MCPServer, tool *sdkmcp.Tool, handler sdkmcp.ToolHandlerFor[In, Out]) {
	sdkmcp.AddTool(s.srv, tool, handler)
}
