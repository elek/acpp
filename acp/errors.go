package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// RequestError represents a JSON-RPC 2.0 error object.
type RequestError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error renders the error as compact JSON so callers get details by default.
func (e *RequestError) Error() string {
	if e == nil {
		return "<nil>"
	}
	b, err := json.Marshal(e)
	if err == nil {
		return string(b)
	}
	if e.Data != nil {
		return fmt.Sprintf("code %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC 2.0 error constructors.

func NewParseError(data any) *RequestError {
	return &RequestError{Code: -32700, Message: "Parse error", Data: data}
}

func NewInvalidRequest(data any) *RequestError {
	return &RequestError{Code: -32600, Message: "Invalid request", Data: data}
}

func NewMethodNotFound(method string) *RequestError {
	return &RequestError{Code: -32601, Message: "Method not found", Data: map[string]any{"method": method}}
}

func NewInvalidParams(data any) *RequestError {
	return &RequestError{Code: -32602, Message: "Invalid params", Data: data}
}

func NewInternalError(data any) *RequestError {
	return &RequestError{Code: -32603, Message: "Internal error", Data: data}
}

func NewRequestCancelled(data any) *RequestError {
	return &RequestError{Code: -32800, Message: "Request cancelled", Data: data}
}

// toReqErr coerces an arbitrary error into a JSON-RPC RequestError.
func toReqErr(err error) *RequestError {
	if err == nil {
		return nil
	}
	var re *RequestError
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewRequestCancelled(map[string]any{"error": err.Error()})
	}
	return NewInternalError(map[string]any{"error": err.Error()})
}
