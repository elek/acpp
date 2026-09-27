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

// ConversationCreated is emitted through the router's subscriber stream the
// moment a conversation is registered — synchronously within Router.Create,
// before the ACP handshake assigns a SessionID. Subscribers use it to establish
// per-conversation state keyed by the stable ConversationID; the persister writes
// the session row here (so a conversation has a durable home even if its ACP
// session never initializes, e.g. a working directory could not be resolved).
type ConversationCreated struct {
	Meta ConversationMeta
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

// ConversationAdopted is emitted through the router's subscriber stream when a
// conversation that outlived a restart of this server — its agent kept running
// on a remote host — is taken back into the router (Router.Adopt). Its session
// row already exists and was never finalized; subscribers that key live state
// off ConversationCreated (a channel's routing table) should register it again.
type ConversationAdopted struct {
	Meta ConversationMeta
}
