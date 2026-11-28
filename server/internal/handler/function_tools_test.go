package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The published definitions must match the tools kb serves. To refresh them
// after changing a tool, run:
//
//	KB_UPDATE_FUNCTION_TOOLS=1 go test ./internal/handler -run TestPublishedFunctionToolsAreCurrent
func TestPublishedFunctionToolsAreCurrent(t *testing.T) {
	for _, format := range FunctionToolFormats {
		want, err := FunctionTools(format)
		require.NoError(t, err)
		path := filepath.Join("..", "..", "..", "integrations", "function-tools", format+".json")
		if os.Getenv("KB_UPDATE_FUNCTION_TOOLS") == "1" {
			require.NoError(t, os.WriteFile(path, want, 0o644))
		}
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), "%s is stale; see this test's comment", path)
	}
}

func TestFunctionToolsKeepEveryMCPTool(t *testing.T) {
	names := []string{}
	for _, tool := range mcpTools() {
		names = append(names, tool["name"].(string))
	}

	raw, err := FunctionTools("openai")
	require.NoError(t, err)
	var openai []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	}
	require.NoError(t, json.Unmarshal(raw, &openai))
	require.Len(t, openai, len(names))
	for i, tool := range openai {
		assert.Equal(t, "function", tool.Type)
		assert.Equal(t, names[i], tool.Function.Name)
		assert.Equal(t, "object", tool.Function.Parameters["type"])
	}

	raw, err = FunctionTools("gemini")
	require.NoError(t, err)
	var gemini struct {
		FunctionDeclarations []struct {
			Name   string         `json:"name"`
			Schema map[string]any `json:"parametersJsonSchema"`
		} `json:"functionDeclarations"`
	}
	require.NoError(t, json.Unmarshal(raw, &gemini))
	require.Len(t, gemini.FunctionDeclarations, len(names))
	for i, decl := range gemini.FunctionDeclarations {
		assert.Equal(t, names[i], decl.Name)
		assert.Equal(t, "object", decl.Schema["type"])
	}

	_, err = FunctionTools("other")
	assert.Error(t, err)
}
