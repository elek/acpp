// Package hook provides per-conversation message-transform hooks. A hook can
// rewrite or drop messages flowing client->agent (Outgoing) and agent->subscriber
// (Incoming), and can inject follow-up prompts via HookContext.Trigger.
//
// Hooks are configured per project in .acpp.yaml (see config.HookConfig) and
// instantiated per conversation by the router, so each conversation gets its own
// hook instances and may carry independent state.
package hook

import (
	"fmt"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/types"
)

// HookContext gives a callback the conversation it is acting on plus a Trigger
// function for injecting follow-up prompts. It is constructed fresh by the router
// for each invocation, so Meta reflects the conversation's current state (the
// SessionID is populated even though it is empty when the conversation is first
// created).
type HookContext struct {
	Meta types.ConversationMeta
	CWD  string
	// Trigger submits a follow-up prompt through the full router pipeline (fanned
	// out to subscribers, sent to the agent) but BYPASSES Outgoing to prevent
	// re-entrancy. The prompt is delivered after the message currently being
	// processed, preserving in-order delivery.
	Trigger func(prompt string) error
}

// Hook transforms messages flowing through a conversation. Implementations may
// carry per-conversation mutable state.
type Hook interface {
	// Outgoing is called for each client->agent message before it is sent to the
	// agent and fanned out to subscribers. Return the (possibly modified) message
	// to proceed, or nil to drop it.
	Outgoing(hc HookContext, msg any) any

	// Incoming is called for each agent->subscriber message before fan-out. Return
	// the (possibly modified) message to deliver, or nil to drop it. Use
	// hc.Trigger to inject a follow-up prompt.
	Incoming(hc HookContext, msg any) any
}

// HookFactory builds a Hook from its .acpp.yaml params (per the spec: a
// map[string]string maps to an interface implementation).
type HookFactory func(params map[string]string) (Hook, error)

// registry maps a hook type name to its factory. Populated by Register, typically
// from package init functions.
var registry = map[string]HookFactory{}

// Register adds a named hook factory to the global registry. It panics on a
// duplicate registration, which can only be a programming error.
func Register(typ string, f HookFactory) {
	if _, ok := registry[typ]; ok {
		panic(fmt.Sprintf("hook: type %q already registered", typ))
	}
	registry[typ] = f
}

// Build instantiates the hooks described by cfgs, in order. An unknown type or a
// factory error fails the whole build so misconfiguration surfaces loudly at
// conversation creation rather than being silently ignored.
func Build(cfgs []config.HookConfig) ([]Hook, error) {
	hooks := make([]Hook, 0, len(cfgs))
	for _, c := range cfgs {
		f, ok := registry[c.Type]
		if !ok {
			return nil, fmt.Errorf("hook: unknown type %q", c.Type)
		}
		h, err := f(c.Params)
		if err != nil {
			return nil, fmt.Errorf("hook %q: %w", c.Type, err)
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}
