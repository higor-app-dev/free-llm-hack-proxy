package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/higor/free-llm-hack-proxy/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock provider for testing
// =============================================================================

type mockProvider struct {
	name  string
	desc  string
	mdls  []providers.ModelInfo
	chatFn func(req *providers.ChatRequest) (*providers.ChatResponse, error)
}

func (m *mockProvider) Name() string                { return m.name }
func (m *mockProvider) Description() string          { return m.desc }
func (m *mockProvider) Models() []providers.ModelInfo { return m.mdls }
func (m *mockProvider) Chat(req *providers.ChatRequest) (*providers.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(req)
	}
	return nil, nil
}

func newMock(name, desc string, mdls ...providers.ModelInfo) *mockProvider {
	return &mockProvider{name: name, desc: desc, mdls: mdls}
}

// =============================================================================
// /v1/models endpoint
// =============================================================================

// openAIModelEntry mirrors the JSON structure returned by /v1/models for
// deserialisation.
type openAIModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelsResponse struct {
	Object string             `json:"object"`
	Data   []openAIModelEntry `json:"data"`
}

func TestHandleListModels_ReturnsOpenAICompatibleShape(t *testing.T) {
	// Set up a registry with two mock providers, each offering multiple models.
	reg := providers.NewRegistry()

	p1 := newMock("deepseek", "DeepSeek",
		providers.ModelInfo{ID: "deepseek-chat", MaxTokens: 8192},
		providers.ModelInfo{ID: "deepseek-reasoner", MaxTokens: 16384},
	)
	p2 := newMock("mimo", "MiMo",
		providers.ModelInfo{ID: "mimo-mini", MaxTokens: 4096},
	)

	require.NoError(t, reg.Register(p1))
	require.NoError(t, reg.Register(p2))

	// Create the handler and issue a GET /v1/models request.
	handler := NewHandler(reg)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	handler.ServeHTTP(rr, req)

	// Check HTTP status.
	assert.Equal(t, http.StatusOK, rr.Code, "expected 200 OK")
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	// Decode the response.
	var resp openAIModelsResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err, "response must be valid JSON")

	// Verify top-level OpenAI list shape.
	assert.Equal(t, "list", resp.Object, `response must have object: "list"`)

	// Verify we got all expected models.
	require.Len(t, resp.Data, 3, "expected 3 models across 2 providers")

	// Build a lookup of returned models by ID.
	modelsByID := make(map[string]openAIModelEntry, 3)
	for _, e := range resp.Data {
		modelsByID[e.ID] = e
	}

	// Check deepseek models.
	for _, id := range []string{"deepseek-chat", "deepseek-reasoner"} {
		e, ok := modelsByID[id]
		require.True(t, ok, "expected model %q in response", id)
		assert.Equal(t, "model", e.Object, "model object type must be 'model'")
		assert.Equal(t, "deepseek", e.OwnedBy, "owned_by must be the provider name")
		assert.Greater(t, e.Created, int64(0), "created must be a positive Unix timestamp")
	}

	// Check mimo model.
	e, ok := modelsByID["mimo-mini"]
	require.True(t, ok, "expected model 'mimo-mini' in response")
	assert.Equal(t, "model", e.Object)
	assert.Equal(t, "mimo", e.OwnedBy, "owned_by must be the provider name")
	assert.Greater(t, e.Created, int64(0), "created must be a positive Unix timestamp")

	// All models from the same request must share the same created timestamp.
	createdSet := make(map[int64]struct{}, 1)
	for _, e := range resp.Data {
		createdSet[e.Created] = struct{}{}
	}
	assert.Len(t, createdSet, 1, "all models should share the same created timestamp (now)")
}

func TestHandleListModels_EmptyRegistry(t *testing.T) {
	reg := providers.NewRegistry()
	handler := NewHandler(reg)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp openAIModelsResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)

	assert.Equal(t, "list", resp.Object)
	assert.Empty(t, resp.Data, "empty registry should return empty data array")
}

// =============================================================================
// Chat completions helpers
// =============================================================================

// chatCompletionResponse is used to decode the non-streaming JSON response.
type chatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// sseEvent represents a single SSE data line parsed from the stream.
type sseEvent struct {
	Data string
}

// parseSSEStream splits a raw SSE response body into individual events.
func parseSSEStream(body string) []sseEvent {
	var events []sseEvent
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			events = append(events, sseEvent{Data: data})
		}
	}
	return events
}

// sseChunkRaw is used to decode individual SSE JSON chunks.
type sseChunkRaw struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int            `json:"index"`
		Delta        map[string]string `json:"delta"`
		FinishReason *string        `json:"finish_reason"`
	} `json:"choices"`
}

// =============================================================================
// POST /v1/chat/completions — non-streaming
// =============================================================================

func TestChatCompletions_Basic(t *testing.T) {
	// Mock provider that returns a known response.
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "test-model", MaxTokens: 4096},
	)
	mock.chatFn = func(_ *providers.ChatRequest) (*providers.ChatResponse, error) {
		return &providers.ChatResponse{
			ID:    "chatcmpl-test123",
			Model: "test-model",
			Choices: []providers.ChatChoice{
				{
					Index: 0,
					Message: providers.ChatMessage{
						Role:    "assistant",
						Content: "Hello, world!",
					},
					FinishReason: "stop",
				},
			},
			Usage: providers.UsageInfo{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}, nil
	}

	reg := providers.NewRegistry()
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	body := `{"model":"test-model","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	// Verify status and content type.
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	// Decode response.
	var resp chatCompletionResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)

	// Verify fields.
	assert.Equal(t, "chatcmpl-test123", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "test-model", resp.Model)
	assert.Len(t, resp.Choices, 1)
	assert.Equal(t, 0, resp.Choices[0].Index)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "Hello, world!", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

// =============================================================================
// POST /v1/chat/completions — streaming (SSE)
// =============================================================================

func TestChatCompletions_SSE(t *testing.T) {
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "test-model", MaxTokens: 4096},
	)
	mock.chatFn = func(_ *providers.ChatRequest) (*providers.ChatResponse, error) {
		return &providers.ChatResponse{
			ID:    "chatcmpl-sse-001",
			Model: "test-model",
			Choices: []providers.ChatChoice{
				{
					Index: 0,
					Message: providers.ChatMessage{
						Role:    "assistant",
						Content: "Hello world",
					},
					FinishReason: "stop",
				},
			},
			Usage: providers.UsageInfo{},
		}, nil
	}

	reg := providers.NewRegistry()
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	body := `{"model":"test-model","messages":[{"role":"user","content":"Hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	// Check SSE headers.
	assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", rr.Header().Get("Connection"))

	// Parse SSE events.
	events := parseSSEStream(rr.Body.String())
	t.Logf("SSE body:\n%s", rr.Body.String())

	// Expected events:
	//   1. role chunk: {"role":"assistant"}
	//   2. content chunk: "Hello "
	//   3. content chunk: "world"
	//   4. finish chunk: finish_reason="stop"
	//   5. [DONE]

	require.GreaterOrEqual(t, len(events), 5, "expected at least 5 SSE events")

	// The last event must be [DONE]
	lastEvent := events[len(events)-1]
	assert.Equal(t, "[DONE]", lastEvent.Data, "last SSE event must be [DONE]")

	// Verify the first event is a role chunk.
	var first sseChunkRaw
	err := json.Unmarshal([]byte(events[0].Data), &first)
	require.NoError(t, err, "first SSE event must be valid JSON")
	assert.Equal(t, "chat.completion.chunk", first.Object)
	require.Len(t, first.Choices, 1)
	assert.Equal(t, "assistant", first.Choices[0].Delta["role"])

	// Verify content chunks contain the words from "Hello world".
	var contentWords []string
	for i := 1; i < len(events)-2; i++ {
		var chunk sseChunkRaw
		err := json.Unmarshal([]byte(events[i].Data), &chunk)
		require.NoError(t, err, "SSE event %d must be valid JSON", i)
		if content, ok := chunk.Choices[0].Delta["content"]; ok {
			contentWords = append(contentWords, content)
		}
	}
	assert.Equal(t, []string{"Hello ", "world"}, contentWords, "content chunks should match words")

	// Verify the finish chunk (second-to-last event before [DONE]).
	var finishChunk sseChunkRaw
	err = json.Unmarshal([]byte(events[len(events)-2].Data), &finishChunk)
	require.NoError(t, err, "finish SSE event must be valid JSON")
	require.Len(t, finishChunk.Choices, 1)
	require.NotNil(t, finishChunk.Choices[0].FinishReason)
	assert.Equal(t, "stop", *finishChunk.Choices[0].FinishReason)
}

// =============================================================================
// POST /v1/chat/completions — missing model (400)
// =============================================================================

func TestChatCompletions_MissingModel(t *testing.T) {
	reg := providers.NewRegistry()
	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	// Send a request with no model field.
	body := `{"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	err := json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Contains(t, errResp.Error.Message, "model is required")
	assert.Equal(t, "api_error", errResp.Error.Type)
}

// =============================================================================
// POST /v1/chat/completions — unknown model (404)
// =============================================================================

func TestChatCompletions_UnknownModel(t *testing.T) {
	// Register a provider so the registry is non-empty, but the model doesn't match.
	reg := providers.NewRegistry()
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "existing-model", MaxTokens: 4096},
	)
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	body := `{"model":"nonexistent-model","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	err := json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Equal(t, "unknown model: nonexistent-model", errResp.Error.Message)
	assert.Equal(t, "api_error", errResp.Error.Type)
}

// =============================================================================
// POST /v1/chat/completions — empty messages (400)
// =============================================================================

func TestChatCompletions_EmptyMessages(t *testing.T) {
	reg := providers.NewRegistry()
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "test-model", MaxTokens: 4096},
	)
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	// Valid model but empty messages array.
	body := `{"model":"test-model","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	err := json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Contains(t, errResp.Error.Message, "at least one message is required")
}

// =============================================================================
// POST /v1/chat/completions — missing messages (400)
// =============================================================================

func TestChatCompletions_MissingMessages(t *testing.T) {
	reg := providers.NewRegistry()
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "test-model", MaxTokens: 4096},
	)
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	// Valid model but no messages field.
	body := `{"model":"test-model"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	err := json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Equal(t, "messages is required", errResp.Error.Message)
}

// =============================================================================
// POST /v1/chat/completions — provider error (500)
// =============================================================================

func TestChatCompletions_ProviderError(t *testing.T) {
	mock := newMock("test-provider", "Test Provider",
		providers.ModelInfo{ID: "test-model", MaxTokens: 4096},
	)
	mock.chatFn = func(_ *providers.ChatRequest) (*providers.ChatResponse, error) {
		return nil, fmt.Errorf("rate limit exceeded")
	}

	reg := providers.NewRegistry()
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	body := `{"model":"test-model","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	err := json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Contains(t, errResp.Error.Message, "provider error: rate limit exceeded")
}

// =============================================================================
// POST /v1/chat/completions — provider prefix notation resolves correctly
// =============================================================================

func TestChatCompletions_ProviderPrefix(t *testing.T) {
	mock := newMock("deepseek", "DeepSeek",
		providers.ModelInfo{ID: "deepseek-chat", MaxTokens: 8192},
	)
	mock.chatFn = func(req *providers.ChatRequest) (*providers.ChatResponse, error) {
		return &providers.ChatResponse{
			ID:    "chatcmpl-ds",
			Model: req.Model,
			Choices: []providers.ChatChoice{
				{
					Index: 0,
					Message: providers.ChatMessage{
						Role:    "assistant",
						Content: "via deepseek prefix",
					},
					FinishReason: "stop",
				},
			},
			Usage: providers.UsageInfo{},
		}, nil
	}

	reg := providers.NewRegistry()
	require.NoError(t, reg.Register(mock))

	handler := NewHandler(reg)
	rr := httptest.NewRecorder()

	// Use provider prefix notation: "deepseek/deepseek-chat"
	body := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp chatCompletionResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "via deepseek prefix", resp.Choices[0].Message.Content)
}
