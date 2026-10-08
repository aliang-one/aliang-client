// Package tools defines the agent's tool registry: the single source of truth
// for every remotely-invokable read-only tool this agent advertises to the
// cloud server. Adding a tool = one entry in the services-layer registry
// builder; the server discovers it at runtime (hello + tools.list) and needs
// no code change.
package tools

import (
	"fmt"
	"sort"
)

// Caps bounds how a tool may run. All Phase-1 tools are read-only; ReadOnly is
// declared for future policy use and surfaced to the server.
type Caps struct {
	ReadOnly       bool `json:"read_only"`
	NeedsProject   bool `json:"needs_project"`
	TimeoutMs      int  `json:"timeout_ms"`
	MaxOutputBytes int  `json:"max_output_bytes"`
}

// HandlerFunc is the shape every existing payload function already has:
// msg in, complete response map out (type/request_id/... filled by the handler).
type HandlerFunc func(msg map[string]interface{}) map[string]interface{}

// ErrorPayloadFunc builds the family error payload for a tool (e.g. the
// legacy file.error shape). Optional; the generic shape is used when nil.
type ErrorPayloadFunc func(requestID string, err error) map[string]interface{}

// Tool is one registered capability. Event is the WS message type the server
// sends; ID is the LLM-facing tool name (== descriptor "name").
type Tool struct {
	ID          string
	Event       string
	Description string
	Parameters  map[string]interface{} // JSON-schema-shaped object
	Caps        Caps
	Handler     HandlerFunc
	OnError     ErrorPayloadFunc
}

func (t *Tool) validate() error {
	if t.ID == "" {
		return fmt.Errorf("tool id is empty")
	}
	if t.Event == "" {
		return fmt.Errorf("tool %s event is empty", t.ID)
	}
	if t.Description == "" {
		return fmt.Errorf("tool %s description is empty", t.ID)
	}
	if t.Handler == nil {
		return fmt.Errorf("tool %s handler is nil", t.ID)
	}
	for _, r := range t.Event {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if !valid {
			return fmt.Errorf("tool %s event %q has invalid charset", t.ID, t.Event)
		}
	}
	return nil
}

// DescriptorJSON returns the advertisement shape carried by agent.hello and
// tools.list.result. "name" duplicates "id" because the server-side LLM tool
// name is the id; keeping both makes the descriptor self-describing.
func (t *Tool) DescriptorJSON() map[string]interface{} {
	name := t.ID
	return map[string]interface{}{
		"id":          t.ID,
		"name":        name,
		"event":       t.Event,
		"description": t.Description,
		"parameters":  t.Parameters,
		"caps":        t.Caps,
	}
}

// Registry is an immutable, validated tool set. Construction panics on
// programmer error (empty/duplicate/invalid entries) — fail fast at process
// start, never at request time.
type Registry struct {
	rev     int
	byEvent map[string]*Tool
	list    []*Tool
}

func NewRegistry(rev int, tools ...*Tool) *Registry {
	r := &Registry{rev: rev, byEvent: make(map[string]*Tool, len(tools))}
	for _, t := range tools {
		if err := t.validate(); err != nil {
			panic(fmt.Sprintf("tool registry: %v", err))
		}
		if _, dup := r.byEvent[t.Event]; dup {
			panic(fmt.Sprintf("tool registry: duplicate event %q", t.Event))
		}
		r.byEvent[t.Event] = t
		r.list = append(r.list, t)
	}
	sort.Slice(r.list, func(i, j int) bool { return r.list[i].ID < r.list[j].ID })
	return r
}

func (r *Registry) Get(event string) (*Tool, bool) {
	t, ok := r.byEvent[event]
	return t, ok
}

func (r *Registry) List() []*Tool {
	out := make([]*Tool, len(r.list))
	copy(out, r.list)
	return out
}

func (r *Registry) Rev() int { return r.rev }

// Descriptors returns advertisement JSON for every tool (sorted by ID).
func (r *Registry) Descriptors() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(r.list))
	for _, t := range r.list {
		out = append(out, t.DescriptorJSON())
	}
	return out
}
