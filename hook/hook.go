// Package hook provides per-conversation message-transform hooks. A hook can
// rewrite or drop messages flowing client->agent (Outgoing) and agent->subscriber
// (Incoming), and can inject follow-up prompts via HookContext.Trigger.
//
// Hooks are configured per project in the project's `hooks` column (see
// config.ParseHookList) plus the global config, and instantiated per conversation
// by the router, so each conversation gets its own hook instances and may carry
// independent state.
package hook

import (
	"fmt"
	"sort"

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

// SessionContext is the context passed to a SessionHook's SetupSession. Unlike
// HookContext (per-message), it describes a conversation being created: its
// freshly-minted ConversationID, the directory originally requested for it
// (BaseDir, before any hook rewrites opts.CWD), and a way to ask how many other
// live sessions already target a directory.
type SessionContext struct {
	Meta           types.ConversationMeta
	ConversationID string
	// BaseDir is the working directory requested for this session before any
	// SessionHook rewrites it (equal to opts.CWD on entry).
	BaseDir string
	// RunningSessionsForDir reports how many sessions are currently live with the
	// given base directory, letting a hook decide whether a directory is under
	// contention. The session being created is not yet counted.
	RunningSessionsForDir func(dir string) int
	// Notify queues a harness message to be shown as one of the session's first
	// messages. It is emitted by the router after the conversation is announced
	// (so it is persisted and ordered before any agent output), rendered as a
	// harness notice rather than agent output. A SessionHook uses it to explain a
	// setup action it took, e.g. the worktree hook naming the worktree it created.
	Notify func(text string)
}

// SessionHook is an OPTIONAL interface a Hook may also implement to participate
// in session setup and teardown. The router type-asserts for it. SetupSession
// runs inside Router.Create after the conversation id is minted but BEFORE the
// sandbox is resolved and the subprocess starts, so it may mutate opts.CWD and
// append to opts.RWBinds. The returned cleanup func (may be nil) runs exactly
// once when the session's process closes. A non-nil error aborts creation.
type SessionHook interface {
	SetupSession(sc SessionContext, opts *types.SessionOpts) (cleanup func(), err error)
}

// HookFactory builds a Hook from its configured params (per the spec: a
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

// RegisteredTypes returns the registered hook type names, sorted. It lets the web
// UI offer the hooks a project can actually enable instead of a free-text field.
func RegisteredTypes() []string {
	types := make([]string, 0, len(registry))
	for typ := range registry {
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
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
