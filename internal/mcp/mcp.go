package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Method represents an MCP method name.
type Method string

const (
	// MethodSearch searches for contracts matching a query.
	MethodSearch Method = "search"
	// MethodInterface fetches a contract's interface.
	MethodInterface Method = "interface"
	// MethodClient generates a client for a contract.
	MethodClient Method = "client"
)

// Params are the parameters for an MCP method call.
type Params struct {
	Method  Method          `json:"method"`
	ID      float64         `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Data    json.RawMessage `json:"params"`
}

// Result is the result of an MCP method call.
type Result struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      float64         `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *MCPError       `json:"error,omitempty"`
}

// MCPError represents an MCP error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server defines the interface for an MCP server.
// Implementations must provide methods for handling each MCP method.
type Server interface {
	Handle(ctx context.Context, method Method, params *Params) (json.RawMessage, error)
}

// mcpServer is the default MCP server implementation.
type mcpServer struct{}

// NewServer creates a new MCP server instance.
func NewServer() Server {
	return &mcpServer{}
}

// Handle implements the Server interface.
func (s *mcpServer) Handle(ctx context.Context, method Method, params *Params) (json.RawMessage, error) {
	switch method {
	case MethodSearch:
		return s.handleSearch(params)
	case MethodInterface:
		return s.handleInterface(params)
	case MethodClient:
		return s.handleClient(params)
	default:
		return nil, fmt.Errorf("mcp: unknown method %s", method)
	}
}

// handleSearch searches for contracts matching a query.
func (s *mcpServer) handleSearch(params *Params) (json.RawMessage, error) {
	var searchReq struct {
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}

	if err := json.Unmarshal(params.Data, &searchReq); err != nil {
		return nil, fmt.Errorf("mcp: invalid search params: %w", err)
	}

	if searchReq.Query == "" {
		return nil, fmt.Errorf("mcp: search query is required")
	}

	result := map[string]interface{}{
		"status": "ok",
		"result": map[string]interface{}{
			"contracts": []interface{}{},
			"total":     0,
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp: encoding search result: %w", err)
	}
	return data, nil
}

// handleInterface fetches a contract's interface.
func (s *mcpServer) handleInterface(params *Params) (json.RawMessage, error) {
	var ifaceReq struct {
		ContractID string `json:"contract_id"`
		WasmHash   string `json:"wasm_hash"`
	}

	if err := json.Unmarshal(params.Data, &ifaceReq); err != nil {
		return nil, fmt.Errorf("mcp: invalid interface params: %w", err)
	}

	if ifaceReq.ContractID == "" {
		return nil, fmt.Errorf("mcp: contract_id is required")
	}

	result := map[string]interface{}{
		"status": "ok",
		"result": map[string]interface{}{
			"contract_id": ifaceReq.ContractID,
			"network":     "",
			"wasm_hash":   ifaceReq.WasmHash,
			"interface":   nil,
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp: encoding interface result: %w", err)
	}
	return data, nil
}

// handleClient generates a TypeScript client for a contract's interface.
func (s *mcpServer) handleClient(params *Params) (json.RawMessage, error) {
	var clientReq struct {
		ContractID string `json:"contract_id"`
		WasmHash   string `json:"wasm_hash"`
	}

	if err := json.Unmarshal(params.Data, &clientReq); err != nil {
		return nil, fmt.Errorf("mcp: invalid client params: %w", err)
	}

	if clientReq.ContractID == "" {
		return nil, fmt.Errorf("mcp: contract_id is required")
	}

	result := map[string]interface{}{
		"status": "ok",
		"result": map[string]interface{}{
			"contract_id": clientReq.ContractID,
			"network":     "",
			"wasm_hash":   clientReq.WasmHash,
			"client":      "",
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp: encoding client result: %w", err)
	}
	return data, nil
}
