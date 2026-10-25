package mcp

import "github.com/zigmund/garmin-papi-mcp/internal/tools"

type registry struct {
	list   []tools.Tool
	byName map[string]tools.Tool
}

func newRegistry(reg []tools.Tool) *registry {
	r := &registry{list: reg, byName: make(map[string]tools.Tool, len(reg))}
	for _, t := range reg {
		r.byName[t.Name] = t
	}
	return r
}

func (r *registry) descriptors() []map[string]any {
	out := make([]map[string]any, 0, len(r.list))
	for _, t := range r.list {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
			"annotations": map[string]any{"readOnlyHint": true},
		})
	}
	return out
}
