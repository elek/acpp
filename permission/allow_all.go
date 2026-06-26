package permission

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

type AllowAll struct {
	router *router.Router
}

func NewAllowAll(router *router.Router) *AllowAll {
	a := &AllowAll{router: router}
	router.Subscribe(a.Subscribe)
	return a
}

func (a *AllowAll) Subscribe(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	req, ok := msg.(acp.RequestPermissionRequest)
	if !ok {
		return
	}
	resp := decide(req.Options)
	if resp == nil {
		return
	}
	if err := a.router.Respond(ctx, rid, id, resp); err != nil {
		slog.Warn("failed to respond to permission request", "error", err)
	}
}

// decide computes the outcome for a permission request: cancel when no options
// are offered, otherwise select the first allow-once option. It returns nil when
// no allow-once option exists, signalling that no response should be sent.
func decide(options []acp.PermissionOption) *acp.RequestPermissionResponse {
	if len(options) == 0 {
		return &acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Cancelled: &acp.RequestPermissionOutcomeCancelled{
					Outcome: "cancelled",
				},
			},
		}
	}

	for _, option := range options {
		if strings.Contains(string(option.Kind), "allow") && strings.Contains(string(option.Kind), "once") {
			return &acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{
						OptionId: option.OptionId,
						Outcome:  "selected",
					},
				},
			}
		}
	}
	return nil
}
