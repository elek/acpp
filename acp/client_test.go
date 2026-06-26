package acp

//// recordingClient records the session updates it receives, in order.
//type recordingClient struct {
//	mu       sync.Mutex
//	updates  []string
//	finished bool // set true once Prompt has returned
//}
//
//func (c *recordingClient) ReadTextFile(ctx context.Context, p ReadTextFileRequest) (ReadTextFileResponse, error) {
//	return ReadTextFileResponse{Content: "stub"}, nil
//}
//func (c *recordingClient) WriteTextFile(ctx context.Context, p WriteTextFileRequest) (WriteTextFileResponse, error) {
//	return WriteTextFileResponse{}, nil
//}
//func (c *recordingClient) RequestPermission(ctx context.Context, p RequestPermissionRequest) (RequestPermissionResponse, error) {
//	return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId}}}, nil
//}
//func (c *recordingClient) SessionUpdate(ctx context.Context, p SessionNotification) error {
//	c.mu.Lock()
//	defer c.mu.Unlock()
//	if c.finished {
//		c.updates = append(c.updates, "AFTER_PROMPT_RETURNED")
//		return nil
//	}
//	if p.Update.AgentMessageChunk != nil && p.Update.AgentMessageChunk.Content.Text != nil {
//		c.updates = append(c.updates, p.Update.AgentMessageChunk.Content.Text.Text)
//	}
//	return nil
//}
//func (c *recordingClient) CreateTerminal(ctx context.Context, p CreateTerminalRequest) (CreateTerminalResponse, error) {
//	return CreateTerminalResponse{TerminalId: "t"}, nil
//}
//func (c *recordingClient) KillTerminalCommand(ctx context.Context, p KillTerminalCommandRequest) (KillTerminalCommandResponse, error) {
//	return KillTerminalCommandResponse{}, nil
//}
//func (c *recordingClient) TerminalOutput(ctx context.Context, p TerminalOutputRequest) (TerminalOutputResponse, error) {
//	return TerminalOutputResponse{}, nil
//}
//func (c *recordingClient) ReleaseTerminal(ctx context.Context, p ReleaseTerminalRequest) (ReleaseTerminalResponse, error) {
//	return ReleaseTerminalResponse{}, nil
//}
//func (c *recordingClient) WaitForTerminalExit(ctx context.Context, p WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error) {
//	return WaitForTerminalExitResponse{}, nil
//}
//
//// TestClientFullFlow exercises initialize -> session/new -> session/prompt
//// against a scripted agent, and asserts that all streamed updates are observed
//// before Prompt returns.
//func TestClientFullFlow(t *testing.T) {
//	const nChunks = 50
//
//	client := &recordingClient{}
//
//	agentReads, clientWrites := io.Pipe()
//	clientReads, agentWrites := io.Pipe()
//	csc := NewClientSideConnection(client, clientWrites, clientReads)
//
//	agent := &scriptedAgent{t: t, reads: agentReads, writes: agentWrites, nChunks: nChunks}
//	go agent.run()
//
//	ctx := context.Background()
//
//	initResp, err := csc.Initialize(ctx, InitializeRequest{ProtocolVersion: ProtocolVersionNumber})
//	if err != nil {
//		t.Fatalf("initialize: %v", err)
//	}
//	if initResp.ProtocolVersion != ProtocolVersionNumber {
//		t.Fatalf("protocol version = %d", initResp.ProtocolVersion)
//	}
//
//	sess, err := csc.NewSession(ctx, NewSessionRequest{Cwd: "/tmp", McpServers: []McpServer{}})
//	if err != nil {
//		t.Fatalf("new session: %v", err)
//	}
//	if sess.SessionId != "s1" {
//		t.Fatalf("session id = %q", sess.SessionId)
//	}
//
//	resp, err := csc.Prompt(ctx, PromptRequest{SessionId: sess.SessionId, Prompt: []ContentBlock{TextBlock("hi")}})
//	if err != nil {
//		t.Fatalf("prompt: %v", err)
//	}
//
//	client.mu.Lock()
//	client.finished = true
//	got := append([]string(nil), client.updates...)
//	client.mu.Unlock()
//
//	if resp.StopReason != StopReasonEndTurn {
//		t.Fatalf("stop reason = %q", resp.StopReason)
//	}
//	if len(got) != nChunks {
//		t.Fatalf("observed %d updates before prompt returned, want %d (%v)", len(got), nChunks, got)
//	}
//	for i, s := range got {
//		if s == "AFTER_PROMPT_RETURNED" {
//			t.Fatalf("an update was delivered after Prompt returned at index %d", i)
//		}
//	}
//}
//
//// scriptedAgent answers the client's requests over an in-memory stdio pipe.
//type scriptedAgent struct {
//	t       *testing.T
//	reads   io.Reader
//	writes  io.Writer
//	nChunks int
//}
//
//func (a *scriptedAgent) run() {
//	dec := json.NewDecoder(a.reads)
//	for {
//		var req anyMessage
//		if err := dec.Decode(&req); err != nil {
//			return
//		}
//		switch req.Method {
//		case AgentMethodInitialize:
//			a.respond(req, map[string]any{"protocolVersion": 1})
//		case AgentMethodSessionNew:
//			a.respond(req, map[string]any{"sessionId": "s1"})
//		case AgentMethodSessionPrompt:
//			for i := 0; i < a.nChunks; i++ {
//				a.notify("session/update", map[string]any{
//					"sessionId": "s1",
//					"update": map[string]any{
//						"sessionUpdate": "agent_message_chunk",
//						"content":       map[string]any{"type": "text", "text": "chunk"},
//					},
//				})
//			}
//			a.respond(req, map[string]any{"stopReason": "end_turn"})
//		default:
//			a.respondErr(req, NewMethodNotFound(req.Method))
//		}
//	}
//}
//
//func (a *scriptedAgent) respond(req anyMessage, result any) {
//	rb, _ := json.Marshal(result)
//	a.writeMsg(anyMessage{ID: req.ID, Result: rb})
//}
//
//func (a *scriptedAgent) respondErr(req anyMessage, e *RequestError) {
//	a.writeMsg(anyMessage{ID: req.ID, Error: e})
//}
//
//func (a *scriptedAgent) notify(method string, params any) {
//	pb, _ := json.Marshal(params)
//	a.writeMsg(anyMessage{Method: method, Params: pb})
//}
//
//func (a *scriptedAgent) writeMsg(m anyMessage) {
//	m.JSONRPC = "2.0"
//	b, err := json.Marshal(m)
//	if err != nil {
//		a.t.Errorf("agent marshal: %v", err)
//		return
//	}
//	if _, err := a.writes.Write(append(b, '\n')); err != nil {
//		return
//	}
//}
