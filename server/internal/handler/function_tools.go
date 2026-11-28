package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FunctionToolFormats lists the function-calling formats FunctionTools can
// write, each named for the file it is published as under
// integrations/function-tools.
var FunctionToolFormats = []string{"openai", "gemini"}

// FunctionTools renders kb's MCP tools as function definitions for agents
// that call functions but do not speak MCP. Each definition keeps the MCP
// tool's name, description and JSON Schema, so a call the model makes can be
// sent unchanged as an MCP tools/call to /mcp.
//
//   - openai: a Chat Completions tools array, also accepted by most
//     OpenAI-compatible APIs.
//   - gemini: a Gemini tool with functionDeclarations, using
//     parametersJsonSchema so the full JSON Schema is kept.
func FunctionTools(format string) ([]byte, error) {
	var out any
	switch format {
	case "openai":
		tools := []map[string]any{}
		for _, t := range mcpTools() {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t["name"], "description": t["description"], "parameters": t["inputSchema"],
			}})
		}
		out = tools
	case "gemini":
		decls := []map[string]any{}
		for _, t := range mcpTools() {
			decls = append(decls, map[string]any{"name": t["name"], "description": t["description"], "parametersJsonSchema": t["inputSchema"]})
		}
		out = map[string]any{"functionDeclarations": decls}
	default:
		return nil, fmt.Errorf("unknown function tool format %q", format)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
