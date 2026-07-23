package types

import (
	"encoding/base64"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	pngB64 := base64.StdEncoding.EncodeToString(png)

	t.Run("text only", func(t *testing.T) {
		blocks := BuildPrompt("hello", nil)
		if len(blocks) != 1 {
			t.Fatalf("blocks = %d, want 1", len(blocks))
		}
		if blocks[0].Text == nil || blocks[0].Text.Text != "hello" {
			t.Fatalf("first block is not text %q", "hello")
		}
	})

	t.Run("image only", func(t *testing.T) {
		blocks := BuildPrompt("", []ImageData{{Data: png, MimeType: "image/png"}})
		if len(blocks) != 1 {
			t.Fatalf("blocks = %d, want 1", len(blocks))
		}
		if blocks[0].Image == nil {
			t.Fatal("first block is not an image")
		}
		if blocks[0].Image.Data != pngB64 {
			t.Fatalf("image data = %q, want base64 %q", blocks[0].Image.Data, pngB64)
		}
		if blocks[0].Image.MimeType != "image/png" {
			t.Fatalf("mime = %q, want image/png", blocks[0].Image.MimeType)
		}
	})

	t.Run("text before images, in order", func(t *testing.T) {
		blocks := BuildPrompt("look", []ImageData{
			{Data: png, MimeType: "image/png"},
			{Data: png, MimeType: "image/jpeg"},
		})
		if len(blocks) != 3 {
			t.Fatalf("blocks = %d, want 3", len(blocks))
		}
		if blocks[0].Text == nil {
			t.Fatal("block 0 should be text")
		}
		if blocks[1].Image == nil || blocks[1].Image.MimeType != "image/png" {
			t.Fatal("block 1 should be the png image")
		}
		if blocks[2].Image == nil || blocks[2].Image.MimeType != "image/jpeg" {
			t.Fatal("block 2 should be the jpeg image")
		}
	})

	t.Run("blank text is dropped", func(t *testing.T) {
		blocks := BuildPrompt("   ", []ImageData{{Data: png, MimeType: "image/png"}})
		if len(blocks) != 1 || blocks[0].Image == nil {
			t.Fatalf("blank text should be dropped, got %d blocks", len(blocks))
		}
	})
}

func TestPromptEchoFromBlocks(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	pngB64 := base64.StdEncoding.EncodeToString(png)

	echo := PromptEchoFromBlocks(BuildPrompt("look here", []ImageData{
		{Data: png, MimeType: "image/png"},
	}))
	if echo.Prompt != "look here" {
		t.Fatalf("prompt = %q, want %q", echo.Prompt, "look here")
	}
	if len(echo.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(echo.Images))
	}
	if echo.Images[0].Data != pngB64 || echo.Images[0].MimeType != "image/png" {
		t.Fatalf("image echo = %+v", echo.Images[0])
	}

	// Multiple text blocks concatenate; image-only has empty prompt.
	echo = PromptEchoFromBlocks(BuildPrompt("", []ImageData{{Data: png, MimeType: "image/jpeg"}}))
	if echo.Prompt != "" || len(echo.Images) != 1 {
		t.Fatalf("image-only echo = %+v", echo)
	}
}
