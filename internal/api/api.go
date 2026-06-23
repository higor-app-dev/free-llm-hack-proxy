// Package api implements OpenAI-compatible HTTP handlers for the proxy.
//
// The API exposes a single endpoint that accepts and responds in the standard
// OpenAI chat completions format:
//
//	POST /v1/chat/completions
//
// The router selects a provider based on the model name in the request body.
// If the model starts with a known provider prefix (e.g. "deepseek/..."),
// the request is forwarded to that provider's adapter.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/higor/free-llm-hack-proxy/internal/providers"
)

// =============================================================================
// Errors
// =============================================================================

// apiError is a JSON-serialisable error response that mirrors the OpenAI error
// shape so clients can parse it uniformly.
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(apiError{struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	}{Message: msg, Type: "api_error"}})
}

// =============================================================================
// Chat request / response (OpenAI-compatible subset)
// =============================================================================

// chatRequest is a minimal OpenAI-compatible chat completions request.
type chatRequest struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

// chatResponse is a minimal OpenAI-compatible chat completions response.
type chatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
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
	} `json:"usage,omitempty"`
}

// =============================================================================
// SSE chunk types (OpenAI streaming format)
// =============================================================================

// sseDelta holds the incremental content for one SSE chunk.
type sseDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// sseChunk is one event in the SSE stream.
type sseChunk struct {
	ID      string     `json:"id"`
	Object  string     `json:"object"`
	Model   string     `json:"model"`
	Choices []sseChoice `json:"choices"`
}

type sseChoice struct {
	Index        int      `json:"index"`
	Delta        sseDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

// =============================================================================
// Handler
// =============================================================================

// NewHandler creates an http.Handler that routes incoming requests to the
// correct provider via the provided Registry.
func NewHandler(registry *providers.Registry) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletions(w, r, registry)
	})

	// Health check.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Provider listing.
	mux.HandleFunc("/v1/providers", func(w http.ResponseWriter, r *http.Request) {
		handleListProviders(w, r, registry)
	})

	// Models listing (OpenAI-compatible).
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		handleListModels(w, r, registry)
	})

	return mux
}

// handleChatCompletions dispatches a chat completion request to the
// appropriate provider based on the model field.
func handleChatCompletions(w http.ResponseWriter, r *http.Request, reg *providers.Registry) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// ---- Parse request body ----
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// ---- Parse messages ----
	if req.Messages == nil {
		writeError(w, http.StatusBadRequest, "messages is required")
		return
	}
	var messages []providers.ChatMessage
	if err := json.Unmarshal(req.Messages, &messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid messages: "+err.Error())
		return
	}

	// ---- Build provider ChatRequest and validate ----
	pReq := &providers.ChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
		Options:  make(map[string]interface{}),
	}
	if req.MaxTokens > 0 {
		pReq.Options["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		pReq.Options["temperature"] = req.Temperature
	}

	if err := pReq.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ---- Resolve provider ----
	provider, err := resolveProvider(req.Model, reg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if provider == nil {
		writeError(w, http.StatusNotFound, "unknown model: "+req.Model)
		return
	}

	log.Printf("[api] chat completions: model=%q provider=%q stream=%v", req.Model, provider.Name(), req.Stream)

	if req.Stream {
		handleStreamingChat(w, r, provider, pReq, req.Model)
	} else {
		handleNonStreamingChat(w, provider, pReq, req.Model)
	}
}

// handleNonStreamingChat calls the provider and returns the full JSON response.
func handleNonStreamingChat(w http.ResponseWriter, provider providers.Provider, pReq *providers.ChatRequest, modelName string) {
	resp, err := provider.Chat(pReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "provider error: "+err.Error())
		return
	}
	if resp == nil {
		writeError(w, http.StatusInternalServerError, "provider returned empty response")
		return
	}

	id := resp.ID
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}

	httpResp := chatResponse{
		ID:     id,
		Object: "chat.completion",
		Model:  modelName,
	}

	for _, choice := range resp.Choices {
		httpResp.Choices = append(httpResp.Choices, struct {
			Index        int `json:"index"`
			Message      struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			Index: choice.Index,
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{
				Role:    choice.Message.Role,
				Content: choice.Message.Content,
			},
			FinishReason: choice.FinishReason,
		})
	}

	httpResp.Usage = struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(httpResp)
}

// handleStreamingChat calls the provider and streams the response as SSE events.
func handleStreamingChat(w http.ResponseWriter, r *http.Request, provider providers.Provider, pReq *providers.ChatRequest, modelName string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported: http.Flusher unavailable")
		return
	}

	// ---- SSE headers ----
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// ---- Call provider ----
	resp, err := provider.Chat(pReq)
	if err != nil {
		// Headers already sent — write error as an SSE event
		errData, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	if resp == nil || len(resp.Choices) == 0 {
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	id := resp.ID
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}

	// ---- 1) Role chunk ----
	roleChunk := sseChunk{
		ID:     id,
		Object: "chat.completion.chunk",
		Model:  modelName,
		Choices: []sseChoice{
			{
				Index: 0,
				Delta: sseDelta{Role: "assistant"},
			},
		},
	}
	data, _ := json.Marshal(roleChunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// ---- 2) Content chunks (word-by-word) ----
	content := resp.Choices[0].Message.Content
	chunks := splitContentChunks(content)
	for _, chunk := range chunks {
		select {
		case <-r.Context().Done():
			return // client disconnected
		default:
		}

		contentChunk := sseChunk{
			ID:     id,
			Object: "chat.completion.chunk",
			Model:  modelName,
			Choices: []sseChoice{
				{
					Index: 0,
					Delta: sseDelta{Content: chunk},
				},
			},
		}
		data, _ = json.Marshal(contentChunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// ---- 3) Finish chunk ----
	finishReason := "stop"
	finishChunk := sseChunk{
		ID:     id,
		Object: "chat.completion.chunk",
		Model:  modelName,
		Choices: []sseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
	}
	data, _ = json.Marshal(finishChunk)
	fmt.Fprintf(w, "data: %s\n\n", data)

	// ---- 4) Done signal ----
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// splitContentChunks splits content into word-level SSE chunks suitable for
// streaming. Each chunk is a single word (preserving spacing between words).
func splitContentChunks(content string) []string {
	if content == "" {
		return []string{""}
	}
	// Split into words separated by spaces, preserving the space on each word
	// except the last one.
	parts := strings.Fields(content)
	if len(parts) == 0 {
		return []string{""}
	}
	chunks := make([]string, len(parts))
	for i, p := range parts {
		if i < len(parts)-1 {
			chunks[i] = p + " "
		} else {
			chunks[i] = p
		}
	}
	return chunks
}

// handleListProviders returns all registered providers.
func handleListProviders(w http.ResponseWriter, r *http.Request, reg *providers.Registry) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reg.All())
}

// handleListModels returns the union of all models from all registered providers.
func handleListModels(w http.ResponseWriter, r *http.Request, reg *providers.Registry) {
	now := time.Now().Unix()
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var models []modelEntry
	for _, p := range reg.All() {
		for _, m := range p.Models() {
			models = append(models, modelEntry{
				ID:      m.ID,
				Object:  "model",
				Created: now,
				OwnedBy: p.Name(),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   models,
	})
}

// resolveProvider extracts the provider name from a model string and looks it
// up in the registry.
//
// Supported formats:
//   - "deepseek/deepseek-chat" → provider "deepseek", model "deepseek-chat"
//   - "deepseek-chat"          → defaults to first provider whose model list
//                                 contains this model ID
//   - "deepseek/*"             → provider "deepseek" (no specific model)
func resolveProvider(model string, reg *providers.Registry) (providers.Provider, error) {
	if model == "" {
		return nil, nil // no model specified — caller can handle
	}

	// Try provider/model prefix notation.
	for _, p := range reg.All() {
		prefix := p.Name() + "/"
		if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			return p, nil
		}
	}

	// Fallback: look for a provider that offers this model ID.
	for _, p := range reg.All() {
		for _, m := range p.Models() {
			if m.ID == model {
				return p, nil
			}
		}
	}

	return nil, nil // unknown
}
