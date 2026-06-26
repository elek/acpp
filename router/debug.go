package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/elek/acpp/types"
)

func Debug(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	marshal, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("router: debug marshal failed", "conversation_id", id, "error", err)
		return
	}

	slog.Info("router: debug", "type", fmt.Sprintf("%T", msg), "conversation_id", id, "message", string(marshal))
}
