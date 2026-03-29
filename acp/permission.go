package acp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/config"
)

// PermissionHandler is called for each tool permission request.
// It decides whether to approve, deny, or ask the user.
type PermissionHandler func(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)

// AskPermission is a callback that presents a yes/no question to the user.
type AskPermission func(question string) (bool, error)

// AskAll creates a PermissionHandler that asks the user for every tool call.
func AskAll(askPermission AskPermission) PermissionHandler {
	return func(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
		if len(p.Options) == 0 {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Cancelled: &acp.RequestPermissionOutcomeCancelled{},
				},
			}, nil
		}

		title := ""
		if p.ToolCall.Title != nil {
			title = *p.ToolCall.Title
		}
		input := extractInput(p.ToolCall.RawInput)

		question := fmt.Sprintf("Allow tool call **%s**?", title)
		if len(input) > 0 {
			for k, v := range input {
				if len(v) > 100 {
					v = v[:97] + "..."
				}
				question += fmt.Sprintf("\n`%s`: %s", k, v)
			}
		}

		approved, err := askPermission(question)
		if err != nil {
			slog.Warn("ask permission failed, denying", "err", err)
			return denyPermission(p.Options)
		}
		if !approved {
			slog.Info("tool call denied by user", "title", title)
			return denyPermission(p.Options)
		}
		slog.Info("tool call approved by user", "title", title)

		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId},
			},
		}, nil
	}
}

// AllowAll is a PermissionHandler that auto-approves all tool calls.
func AllowAll(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if len(p.Options) == 0 {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Cancelled: &acp.RequestPermissionOutcomeCancelled{},
			},
		}, nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId},
		},
	}, nil
}

// NewPermissionHandler creates a PermissionHandler that checks config rules
// and optionally asks the user for approval.
func NewPermissionHandler(cfg *config.Config, askPermission AskPermission) PermissionHandler {
	if cfg == nil || len(cfg.ToolPermissions) == 0 {
		return AllowAll
	}

	return func(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
		if len(p.Options) == 0 {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Cancelled: &acp.RequestPermissionOutcomeCancelled{},
				},
			}, nil
		}

		title := ""
		if p.ToolCall.Title != nil {
			title = *p.ToolCall.Title
		}
		input := extractInput(p.ToolCall.RawInput)

		action := cfg.MatchToolPermission(title, input)
		switch action {
		case config.ToolPermissionDeny:
			slog.Info("tool call denied by config", "title", title, "input", input)
			return denyPermission(p.Options)
		case config.ToolPermissionAsk:
			if askPermission == nil {
				slog.Info("tool call denied (ask configured but no interactive prompt)", "title", title)
				return denyPermission(p.Options)
			}
			question := fmt.Sprintf("Allow tool call **%s**?", title)
			if len(input) > 0 {
				for k, v := range input {
					if len(v) > 100 {
						v = v[:97] + "..."
					}
					question += fmt.Sprintf("\n`%s`: %s", k, v)
				}
			}
			approved, err := askPermission(question)
			if err != nil {
				slog.Warn("ask permission failed, denying", "err", err)
				return denyPermission(p.Options)
			}
			if !approved {
				slog.Info("tool call denied by user", "title", title)
				return denyPermission(p.Options)
			}
			slog.Info("tool call approved by user", "title", title)
		}

		// Auto-approve: select first option
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId},
			},
		}, nil
	}
}

// denyPermission finds a reject option from the permission options, or cancels.
func denyPermission(options []acp.PermissionOption) (acp.RequestPermissionResponse, error) {
	for _, opt := range options {
		if opt.Kind == acp.PermissionOptionKindRejectOnce || opt.Kind == acp.PermissionOptionKindRejectAlways {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
				},
			}, nil
		}
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Cancelled: &acp.RequestPermissionOutcomeCancelled{},
		},
	}, nil
}

// extractInput extracts string key-value pairs from a raw input (typically map[string]interface{}).
func extractInput(rawInput any) map[string]string {
	result := make(map[string]string)
	if rawInput == nil {
		return result
	}
	if inputs, ok := rawInput.(map[string]interface{}); ok {
		for key, value := range inputs {
			if strVal, ok := value.(string); ok {
				result[key] = strVal
			}
		}
	}
	return result
}
