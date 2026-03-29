package channel

import (
	"context"

	"github.com/coder/acp-go-sdk"
)

type ToolUsage struct {
	Name     string
	Title    string            // Original title, used to decide whether to show input
	Input    map[string]string // All input parameters
	Status   string
	ToolKind string // Tool kind for emoji selection
}

type ToolUsageUpdater func(ToolUsage) error

type CreateChannel func(context.Context) (Channel, error)

// ChannelID identifies a Channel instance (e.g., "discord-bot-1", "console").
type ChannelID string

// SourceID identifies an endpoint within a Channel (e.g., a Discord channel ID).
type SourceID string

// ChannelSource uniquely identifies an external endpoint across all channels.
type ChannelSource struct {
	ChannelID ChannelID
	SourceID  SourceID
}

// ImageData represents a downloaded image attachment.
type ImageData struct {
	Data     []byte // raw image bytes (not base64-encoded)
	MimeType string
}

// Input represents user input from an external channel.
// Exactly one of Command or Message is set.
type Input struct {
	Source  SourceID
	Command string      // slash command (e.g. "/start"), empty if Message is set
	Message string      // free-form message, empty if Command is set
	Images  []ImageData // optional image attachments
}

// PlanUpdate represents a plan update to be displayed.
type PlanUpdate struct {
	Entries []PlanEntry
}

// PlanEntry represents a single entry in a plan.
type PlanEntry struct {
	Content  string
	Priority string // "high", "medium", "low"
	Status   string // "pending", "in_progress", "completed"
}

// Channel is the contract between internal bot --> external channel.
type Channel interface {
	SendTextFragment(SourceID, string) error
	SendThoughtFragment(SourceID, string) error
	SendTextMessage(SourceID, string) error
	SendToolUsage(SourceID, string) ToolUsageUpdater
	SendPlanUpdate(SourceID, PlanUpdate) error
	StartConversation(SourceID) error
	FinishConversation(SourceID, acp.PromptResponse) error
	GetTopic(source SourceID) (string, error)
	GetChannelName(source SourceID) (string, error)
	// SendImage sends an inline image to the channel.
	SendImage(source SourceID, mimeType string, data []byte) error
	// SendFile sends a file attachment to the channel.
	SendFile(source SourceID, filename string, content []byte) error
	// AskYesNo sends a yes/no question to the user and waits for a response.
	// Returns true if the user answered yes, false otherwise.
	AskYesNo(source SourceID, question string) (bool, error)
	// Input returns a channel that receives user inputs (commands and messages).
	Input() <-chan Input
	// Close shuts down the channel and releases resources.
	Close() error
}
