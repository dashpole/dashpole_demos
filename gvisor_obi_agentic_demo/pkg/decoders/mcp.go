// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package decoders

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MCPRequest captures Model Context Protocol JSON-RPC request details.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// MCPToolCallParams captures parameters for "tools/call".
type MCPToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// MCPResponse captures Model Context Protocol JSON-RPC response details.
type MCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPCallDetails stores normalized telemetry attributes extracted from MCP frames.
type MCPCallDetails struct {
	MethodName    string
	ToolName      string
	ToolArguments string
	ToolResult    string
	IsError       bool
	ErrorMessage  string
}

// ParseMCPRequest attempts to parse JSON-RPC payload as an MCP request.
func ParseMCPRequest(data []byte) (*MCPCallDetails, bool) {
	var req MCPRequest
	if err := json.Unmarshal(data, &req); err != nil || req.Method == "" {
		return nil, false
	}

	// Validate JSON-RPC 2.0 version
	if req.JSONRPC != "2.0" {
		return nil, false
	}

	details := &MCPCallDetails{
		MethodName: req.Method,
	}

	if req.Method == "tools/call" && len(req.Params) > 0 {
		var toolParams MCPToolCallParams
		if err := json.Unmarshal(req.Params, &toolParams); err == nil {
			details.ToolName = toolParams.Name
			if len(toolParams.Arguments) > 0 {
				argBytes, _ := json.Marshal(toolParams.Arguments)
				details.ToolArguments = string(argBytes)
			}
		}
	}

	return details, true
}

// ParseMCPResponse attempts to parse JSON-RPC payload as an MCP response.
func ParseMCPResponse(data []byte) (*MCPCallDetails, bool) {
	var resp MCPResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false
	}

	if resp.JSONRPC != "2.0" {
		return nil, false
	}

	if resp.Result == nil && resp.Error == nil {
		return nil, false
	}

	details := &MCPCallDetails{}
	if resp.Error != nil {
		details.IsError = true
		details.ErrorMessage = fmt.Sprintf("[%d] %s", resp.Error.Code, resp.Error.Message)
	} else if len(resp.Result) > 0 {
		details.ToolResult = strings.TrimSpace(string(resp.Result))
	}

	return details, true
}
