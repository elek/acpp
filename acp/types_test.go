package acp

import (
	"encoding/json"
	"testing"
)

func TestContentBlock_TextRoundTrip(t *testing.T) {
	b, err := json.Marshal(TextBlock("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"text":"hello","type":"text"}` {
		t.Fatalf("marshal = %s", b)
	}
	var cb ContentBlock
	if err := json.Unmarshal(b, &cb); err != nil {
		t.Fatal(err)
	}
	if cb.Text == nil || cb.Text.Text != "hello" {
		t.Fatalf("round-trip lost text: %+v", cb)
	}
}

func TestContentBlock_ImageRoundTrip(t *testing.T) {
	b, err := json.Marshal(ImageBlock("ZGF0YQ==", "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	var cb ContentBlock
	if err := json.Unmarshal(b, &cb); err != nil {
		t.Fatal(err)
	}
	if cb.Image == nil || cb.Image.Data != "ZGF0YQ==" || cb.Image.MimeType != "image/png" {
		t.Fatalf("round-trip lost image: %+v", cb)
	}
}

func TestSessionUpdate_AgentMessageChunkRoundTrip(t *testing.T) {
	in := SessionUpdate{AgentMessageChunk: &SessionUpdateAgentMessageChunk{
		Content: TextBlock("world"),
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Discriminator must be injected even though the caller left it empty.
	if disc := readDiscriminator(b, "sessionUpdate"); disc != "agent_message_chunk" {
		t.Fatalf("sessionUpdate discriminator = %q", disc)
	}
	var out SessionUpdate
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.AgentMessageChunk == nil || out.AgentMessageChunk.Content.Text == nil ||
		out.AgentMessageChunk.Content.Text.Text != "world" {
		t.Fatalf("round-trip lost agent message chunk: %+v", out)
	}
}

func TestSessionUpdate_ToolCallRoundTrip(t *testing.T) {
	in := SessionUpdate{ToolCall: &SessionUpdateToolCall{
		ToolCallId: "tc1",
		Title:      "Read file",
		Kind:       ToolKindRead,
		Status:     ToolCallStatusCompleted,
		RawInput:   map[string]any{"path": "/tmp/x"},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionUpdate
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	tc := out.ToolCall
	if tc == nil || tc.ToolCallId != "tc1" || tc.Title != "Read file" ||
		tc.Kind != ToolKindRead || tc.Status != ToolCallStatusCompleted {
		t.Fatalf("round-trip lost tool call: %+v", out)
	}
}

func TestSessionUpdate_UsageUpdateRoundTrip(t *testing.T) {
	in := SessionUpdate{UsageUpdate: &SessionUsageUpdate{
		Used: 12000, Size: 200000, Cost: &Cost{Amount: 1.23, Currency: "USD"},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionUpdate
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	u := out.UsageUpdate
	if u == nil || u.Used != 12000 || u.Size != 200000 || u.Cost == nil || u.Cost.Amount != 1.23 {
		t.Fatalf("round-trip lost usage update: %+v", out)
	}
}

func TestSessionUpdate_UnknownVariantIgnored(t *testing.T) {
	var out SessionUpdate
	// A future/extension update variant must not break decoding.
	if err := json.Unmarshal([]byte(`{"sessionUpdate":"something_new","foo":1}`), &out); err != nil {
		t.Fatalf("unexpected error on unknown variant: %v", err)
	}
}

func TestRequestPermissionOutcome_RoundTrip(t *testing.T) {
	in := RequestPermissionResponse{Outcome: RequestPermissionOutcome{
		Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if disc := readDiscriminator(mustField(t, b, "outcome"), "outcome"); disc != "selected" {
		t.Fatalf("outcome discriminator = %q", disc)
	}
	var out RequestPermissionResponse
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Outcome.Selected == nil || out.Outcome.Selected.OptionId != "allow" {
		t.Fatalf("round-trip lost outcome: %+v", out)
	}
}

func mustField(t *testing.T, b []byte, key string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m[key]
}
