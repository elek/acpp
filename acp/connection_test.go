package acp

//
//// fakeAgent drives the agent side of an in-memory stdio connection: it reads
//// line-delimited JSON requests from the client and lets the test script the
//// responses/notifications written back.
//type fakeAgent struct {
//	in  *bufio.Scanner // requests from client
//	out io.Writer      // notifications/responses to client
//}
//
//func newPipePair(t *testing.T, handler MethodHandler) (*Connection, *fakeAgent) {
//	t.Helper()
//	agentReads, clientWrites := io.Pipe() // client -> agent
//	clientReads, agentWrites := io.Pipe() // agent -> client
//	conn := NewConnection(handler, clientWrites, clientReads)
//	sc := bufio.NewScanner(agentReads)
//	sc.Buffer(make([]byte, 0, 1<<20), 10<<20)
//	return conn, &fakeAgent{in: sc, out: agentWrites}
//}
//
//func (a *fakeAgent) readRequest(t *testing.T) anyMessage {
//	t.Helper()
//	if !a.in.Scan() {
//		t.Fatalf("agent: expected a request, got EOF")
//	}
//	var m anyMessage
//	if err := json.Unmarshal(a.in.Bytes(), &m); err != nil {
//		t.Fatalf("agent: bad request json: %v", err)
//	}
//	return m
//}
//
//func (a *fakeAgent) writeLine(t *testing.T, v any) {
//	t.Helper()
//	b, err := json.Marshal(v)
//	if err != nil {
//		t.Fatalf("agent: marshal: %v", err)
//	}
//	if _, err := a.out.Write(append(b, '\n')); err != nil {
//		t.Fatalf("agent: write: %v", err)
//	}
//}
//
//// TestSendRequest_DeliversResponseAfterAllNotifications is the core ordering
//// guarantee: every session/update notification sent before the prompt response
//// must be fully handled by the time the prompt request returns.
//func TestSendRequest_DeliversResponseAfterAllNotifications(t *testing.T) {
//	const n = 200
//
//	var mu sync.Mutex
//	var handled int
//	var respReturned bool
//	var orderViolation bool
//
//	handler := func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
//		if method == "session/update" {
//			mu.Lock()
//			if respReturned {
//				orderViolation = true
//			}
//			handled++
//			mu.Unlock()
//			// Slow the handler down so a concurrent implementation would race
//			// the response ahead of the notifications.
//			time.Sleep(time.Millisecond)
//		}
//		return nil, nil
//	}
//
//	conn, agent := newPipePair(t, handler)
//
//	go func() {
//		req := agent.readRequest(t)
//		for i := 0; i < n; i++ {
//			agent.writeLine(t, map[string]any{
//				"jsonrpc": "2.0",
//				"method":  "session/update",
//				"params":  map[string]any{"sessionId": "s1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": fmt.Sprintf("%d", i)}}},
//			})
//		}
//		agent.writeLine(t, map[string]any{
//			"jsonrpc": "2.0",
//			"id":      json.RawMessage(*req.ID),
//			"result":  map[string]any{"stopReason": "end_turn"},
//		})
//	}()
//
//	resp, err := SendRequest[PromptResponse](conn, context.Background(), "session/prompt", PromptRequest{SessionId: "s1", Prompt: []ContentBlock{TextBlock("hi")}})
//	mu.Lock()
//	respReturned = true
//	gotHandled := handled
//	violated := orderViolation
//	mu.Unlock()
//
//	if err != nil {
//		t.Fatalf("SendRequest error: %v", err)
//	}
//	if resp.StopReason != StopReasonEndTurn {
//		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
//	}
//	if violated {
//		t.Fatalf("a notification was handled after the response returned (ordering violated)")
//	}
//	if gotHandled != n {
//		t.Fatalf("handled %d notifications before response, want %d", gotHandled, n)
//	}
//}
//
//// TestInboundRequestGetsResponse verifies the connection answers requests the
//// peer makes (e.g. fs/read_text_file) by routing through the handler.
//func TestInboundRequestGetsResponse(t *testing.T) {
//	handler := func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
//		if method == "fs/read_text_file" {
//			return ReadTextFileResponse{Content: "file-body"}, nil
//		}
//		return nil, NewMethodNotFound(method)
//	}
//
//	_, agent := newPipePair(t, handler)
//
//	// Agent sends a request to the client and reads the response.
//	agent.writeLine(t, map[string]any{
//		"jsonrpc": "2.0",
//		"id":      1,
//		"method":  "fs/read_text_file",
//		"params":  map[string]any{"path": "/tmp/x", "sessionId": "s1"},
//	})
//
//	resp := agent.readRequest(t) // reuse line reader for the response
//	if resp.Result == nil {
//		t.Fatalf("expected a result, got error: %v", resp.Error)
//	}
//	var r ReadTextFileResponse
//	if err := json.Unmarshal(resp.Result, &r); err != nil {
//		t.Fatalf("unmarshal result: %v", err)
//	}
//	if r.Content != "file-body" {
//		t.Fatalf("content = %q, want file-body", r.Content)
//	}
//}
