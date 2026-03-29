package channel

// HookRegistry maps hook names to their factory functions.
var HookRegistry = map[string]func() Hook{}

// RegisterHook adds a named hook factory to the global registry.
func RegisterHook(name string, factory func() Hook) {
	HookRegistry[name] = factory
}

// PromptFunc sends a follow-up prompt to the session.
// It goes through the full pipeline (logging, broadcasting, usage tracking)
// but does NOT invoke hooks (preventing re-entrancy).
type PromptFunc func(prompt string) error

// Hook defines lifecycle callbacks for a session.
// Implementations may carry per-session mutable state.
// Each session gets its own hook instances created via factory functions.
type Hook interface {
	// OnSessionStarted is called after the session and relay are created.
	OnSessionStarted(cwd string)

	// BeforeFirstPrompt is called before the very first user prompt.
	// It may augment the prompt text by appending to it.
	// Returns the (possibly modified) prompt text.
	BeforeFirstPrompt(cwd string, prompt string) string

	// AfterPromptFinished is called after each prompt completes and the
	// relay has flushed all pending events. The promptFunc callback can
	// be used to send follow-up prompts through the full pipeline.
	AfterPromptFinished(cwd string, prompt string, promptFunc PromptFunc)
}
