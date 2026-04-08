package mcp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 envelopes used over every transport. The MCP spec
// (2024-11) layers tools/list, tools/call, etc. on top of plain
// JSON-RPC, so the same Request/Response shapes are reusable across
// stdio, SSE, and WebSocket — only the framing differs.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message)
}

func newRequest(id int, method string, params any) (rpcRequest, error) {
	var p json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return rpcRequest{}, err
		}
		p = raw
	}
	idRaw, _ := json.Marshal(id)
	return rpcRequest{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  p,
	}, nil
}
