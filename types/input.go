package types

// ImageData represents a downloaded image attachment.
type ImageData struct {
	Data     []byte // raw image bytes (not base64-encoded)
	MimeType string
}

// Input represents user input from an external channel.
// Exactly one of Command or Message is set.
type Input struct {
	Command    string      // slash command (e.g. "/start"), empty if Message is set
	Message    string      // free-form message, empty if Command is set
	Images     []ImageData // optional image attachments
	OnComplete func()      // optional callback invoked after prompt processing finishes
}
