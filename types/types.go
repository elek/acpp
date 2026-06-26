package types

import "github.com/elek/acpp/acp"

//type Project struct {
//	ID string
//}
//
//type Session struct {
//	ProjectID string
//	ID        string
//}

type ConversationMeta struct {
	ProjectID      string
	ProcessPID     int
	ConversationID string
	SessionID      acp.SessionId
}

// ConversationReplaced is emitted through the router's subscriber stream when a
// conversation's underlying session is swapped (e.g. via /clear), changing its
// ConversationMeta. Subscribers that key off ConversationMeta (such as channels
// mapping an endpoint to a conversation) should re-point Old to New.
type ConversationReplaced struct {
	Old ConversationMeta
	New ConversationMeta
}

// ConversationClosed is emitted through the router's subscriber stream when a
// conversation's underlying session is torn down (explicit close or router
// shutdown). Subscribers use it to finalize per-conversation state — e.g. the
// persister marks the session complete and stamps finished_at. Err is non-empty
// if the conversation ended because of an error.
type ConversationClosed struct {
	Meta ConversationMeta
	Err  string
}
