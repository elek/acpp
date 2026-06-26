// Package permission decides how to answer inbound session/request_permission
// requests from an ACP agent. It hooks into the router by registering a
// Subscriber: when an acp.PermissionRequest flows through the router the
// subscriber computes an outcome (per config) and sends the JSON-RPC response
// via the wrapper's Respond closure.
package permission

//// AskCallback is consulted when a rule's action is ToolPermissionAsk. It
//// receives a short description of the tool call and returns true to approve.
//type AskCallback func(description string) (bool, error)
//
//
//func New(cfg *config.Config, ask AskCallback) router.Subscriber {
//	return func(ctx context.Context, id *types.ConversationID, msg any) {
//		req, ok := msg.(acp.PermissionRequest)
//		if !ok {
//			return
//		}
//		if len(req.Request.Options) == 0 {
//			respond(req, cancelled())
//			return
//		}
//
//		title := ""
//		if req.Request.ToolCall.Title != nil {
//			title = *req.Request.ToolCall.Title
//		}
//		input := extractInput(req.Request.ToolCall.RawInput)
//
//		action := config.ToolPermissionApprove
//		if cfg != nil {
//			if matched := cfg.MatchToolPermission(title, input); matched != "" {
//				action = matched
//			}
//		}
//
//		switch action {
//		case config.ToolPermissionDeny:
//			slog.Info("tool call denied by config", "title", title, "input", input)
//			respond(req, deny(req.Request.Options))
//		case config.ToolPermissionAsk:
//			if ask == nil {
//				slog.Info("tool call denied: ask action but no callback", "title", title)
//				respond(req, deny(req.Request.Options))
//				return
//			}
//			approved, err := ask(describe(title, input))
//			if err != nil {
//				slog.Warn("ask callback failed", "title", title, "error", err)
//				respond(req, deny(req.Request.Options))
//				return
//			}
//			if approved {
//				respond(req, approve(req.Request.Options))
//			} else {
//				respond(req, deny(req.Request.Options))
//			}
//		default:
//			respond(req, approve(req.Request.Options))
//		}
//	}
//}
//
//
//
//// approve finds an allow option, or cancels.
//func approve(options []acp.PermissionOption) acp.RequestPermissionResponse {
//	for _, opt := range options {
//		if opt.Kind == acp.PermissionOptionKindAllowOnce || opt.Kind == acp.PermissionOptionKindAllowAlways {
//			return acp.RequestPermissionResponse{
//				Outcome: acp.RequestPermissionOutcome{
//					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
//				},
//			}
//		}
//	}
//	return cancelled()
//}
//
//// deny finds a reject option from the permission options, or cancels.
//func deny(options []acp.PermissionOption) acp.RequestPermissionResponse {
//	for _, opt := range options {
//		if opt.Kind == acp.PermissionOptionKindRejectOnce || opt.Kind == acp.PermissionOptionKindRejectAlways {
//			return acp.RequestPermissionResponse{
//				Outcome: acp.RequestPermissionOutcome{
//					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
//				},
//			}
//		}
//	}
//	return cancelled()
//}
//
//func cancelled() acp.RequestPermissionResponse {
//	return acp.RequestPermissionResponse{
//		Outcome: acp.RequestPermissionOutcome{
//			Cancelled: &acp.RequestPermissionOutcomeCancelled{},
//		},
//	}
//}
//
//func describe(title string, input map[string]string) string {
//	if len(input) == 0 {
//		return title
//	}
//	parts := title
//	for k, v := range input {
//		parts += " " + k + "=" + v
//	}
//	return parts
//}
//
//func extractInput(rawInput any) map[string]string {
//	result := make(map[string]string)
//	if rawInput == nil {
//		return result
//	}
//	if inputs, ok := rawInput.(map[string]interface{}); ok {
//		for key, value := range inputs {
//			if strVal, ok := value.(string); ok {
//				result[key] = strVal
//			}
//		}
//	}
//	return result
//}
