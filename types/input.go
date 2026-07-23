package types

import (
	"encoding/base64"
	"strings"

	"github.com/elek/acpp/acp"
)

// ImageData represents a downloaded image attachment.
type ImageData struct {
	Data     []byte // raw image bytes (not base64-encoded)
	MimeType string
}

// BuildPrompt assembles ACP content blocks from message text and image
// attachments: a leading text block when text is non-blank, followed by one
// base64-encoded image block per attachment (in order). Shared by every channel
// that turns user input into a prompt.
func BuildPrompt(text string, images []ImageData) []acp.ContentBlock {
	var blocks []acp.ContentBlock
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, acp.TextBlock(text))
	}
	for _, img := range images {
		blocks = append(blocks, acp.ImageBlock(base64.StdEncoding.EncodeToString(img.Data), img.MimeType))
	}
	return blocks
}

// PromptImage is a base64-encoded image in a prompt-echo payload, mirroring an
// acp.ContentBlockImage's data and mime type.
type PromptImage struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// PromptEcho is the browser-facing shape of a user prompt, used for both the
// live "prompt" event and the persisted log replayed on page load. Text blocks
// concatenate into Prompt; image blocks become Images.
type PromptEcho struct {
	Prompt string        `json:"prompt"`
	Images []PromptImage `json:"images,omitempty"`
}

// PromptEchoFromBlocks flattens a prompt's content blocks into the browser echo
// shape, so a rendered user turn carries both its text and any pasted images.
func PromptEchoFromBlocks(blocks []acp.ContentBlock) PromptEcho {
	var echo PromptEcho
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			echo.Prompt += b.Text.Text
		case b.Image != nil:
			echo.Images = append(echo.Images, PromptImage{
				Data:     b.Image.Data,
				MimeType: b.Image.MimeType,
			})
		}
	}
	return echo
}

// Input represents user input from an external channel.
// Exactly one of Command or Message is set.
type Input struct {
	Command    string      // slash command (e.g. "/start"), empty if Message is set
	Message    string      // free-form message, empty if Command is set
	Images     []ImageData // optional image attachments
	OnComplete func()      // optional callback invoked after prompt processing finishes
}
