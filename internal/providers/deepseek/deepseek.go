// Package deepseek implements the DeepSeek AIProvider adapter.
//
// DeepSeek is a browser-based provider: the proxy uses go-rod to automate
// chat.deepseek.com for authenticated chat completions. The Login method
// opens a visible browser window, waits for the user to log in manually,
// then captures and persists the authenticated session (cookies + localStorage)
// via the session store.
package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/higor/free-llm-hack-proxy/internal/providers"
	"github.com/higor/free-llm-hack-proxy/internal/session"
)

const (
	// Name is the registry key for this provider.
	Name = "deepseek"

	// ProviderHost is the domain used for session file storage and browser
	// navigation.
	ProviderHost = "chat.deepseek.com"

	// DefaultLoginTimeout is how long to wait for manual login before
	// aborting. The caller can override this via context deadline.
	DefaultLoginTimeout = 5 * time.Minute

	// pollInterval is how often to check for the chat interface element
	// while waiting for the user to log in.
	pollInterval = 500 * time.Millisecond

	// pageStableTimeout is the wait time for the page to settle after
	// navigation before we start polling for the chat UI.
	pageStableTimeout = 2 * time.Second

	// =============================================================================
	// Prompt flow constants
	// =============================================================================

	// promptMaxRetries is the maximum number of attempts for a single prompt
	// operation when transient errors occur.
	promptMaxRetries = 3

	// responsePollInterval is how often to poll for the AI response element
	// after submitting a question.
	responsePollInterval = 500 * time.Millisecond

	// responseTimeout is the maximum time to wait for an AI response to
	// appear after submitting a question.
	responseTimeout = 3 * time.Minute

	// responseStableDelay is the time to wait after the response element
	// first appears before extracting text, to catch streaming updates.
	responseStableDelay = 1 * time.Second
)

// loginPageURL is the DeepSeek chat page URL.
const loginPageURL = "https://chat.deepseek.com"

// chatDetectSelectors are CSS selectors that, when present and visible,
// indicate the chat interface is loaded and the user is authenticated.
// The selectors are tried in order, and the first match is accepted.
// Selecting robust, generic selectors that match DeepSeek's chat UI:
//   - "textarea" catches message input fields
//   - "div[contenteditable=\"true\"]" catches rich-text inputs
//   - various class-based patterns for flexibility
var chatDetectSelectors = []string{
	"textarea",
	"div[contenteditable=\"true\"]",
	"[class*=\"chat-input\"]",
	"[class*=\"message-list\"]",
	"[class*=\"conversation\"]",
}

// inputSelectors are CSS selectors for locating the chat input element where
// the user types questions. Tried in order; the first visible match is used.
var inputSelectors = []string{
	"textarea",
	"div[contenteditable=\"true\"]",
	"[class*=\"chat-input\"]",
	"[class*=\"input-area\"]",
	"[class*=\"prompt\"] textarea",
	"[class*=\"ds-input\"]",
}

// responseSelectors are CSS selectors for locating assistant response messages.
// The last element matching the first found selector is used as the response.
// Multiple selectors provide resilience against DeepSeek DOM changes.
var responseSelectors = []string{
	"[class*=\"ds-assistant\"]",
	"[class*=\"ds-chatbot-message-assistant\"]",
	"[class*=\"gpt-message\"]",
	"[class*=\"message-assistant\"]",
	"[class*=\"assistant-message\"]",
	"[class*=\"assistant\"]",
}

// =============================================================================
// Provider
// =============================================================================

// Provider implements providers.AIProvider for DeepSeek using go-rod browser
// automation and a session store for persisting authentication state.
type Provider struct {
	store session.SessionStore
}

// New creates a new DeepSeek AIProvider with a file-backed session store at
// the standard path: ~/.llm-proxy/sessions/chat.deepseek.com.json.
func New() *Provider {
	path, err := session.SessionPath(ProviderHost)
	if err != nil {
		// Fallback to manual construction.
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".llm-proxy", "sessions", "chat.deepseek.com.json")
		_ = os.MkdirAll(filepath.Dir(path), 0700)
	}
	return &Provider{
		store: session.NewFileStore(path),
	}
}

// NewWithStore creates a DeepSeek provider with a custom session store.
// This is useful for testing with in-memory or temporary stores.
func NewWithStore(store session.SessionStore) *Provider {
	return &Provider{store: store}
}

// Name returns "deepseek".
func (p *Provider) Name() string { return Name }

// Description returns a human-readable description.
func (p *Provider) Description() string {
	return "DeepSeek — browser-based chat at chat.deepseek.com"
}

// Models returns the list of chat models this provider offers.
func (p *Provider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{
		{
			ID:                "deepseek-chat",
			MaxTokens:         32768,
			SupportsStreaming: true,
		},
		{
			ID:                "deepseek-reasoner",
			MaxTokens:         65536,
			SupportsStreaming: true,
		},
	}
}

// Chat sends a chat completion request to DeepSeek and returns the response.
// It delegates to Prompt with a background context and a generous timeout.
func (p *Provider) Chat(req *providers.ChatRequest) (*providers.ChatResponse, error) {
	if req == nil {
		return nil, &providers.ProviderError{
			Code:    providers.ErrInvalidRequest.Code,
			Message: "deepseek chat: request is nil",
		}
	}

	// Prompt requires a context. Use background with a generous timeout
	// since browser-based chat involves user login and response generation.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return p.Prompt(ctx, *req)
}

// IsSessionValid checks whether a saved session exists and is still usable.
// Returns true only when:
//   - a saved session loads without error
//   - the cookie slice is non-empty
//   - at least one cookie has not expired (Expires == 0 is a session cookie —
//     no explicit expiry, treated as valid).
//
// Missing files, parse errors, empty cookies, and all-cookies-expired all
// return false, so the caller knows to re-authenticate.
func (p *Provider) IsSessionValid() bool {
	cookies, _, err := p.store.Load()
	if err != nil {
		log.Printf("deepseek: IsSessionValid: load error: %v", err)
		return false
	}
	if len(cookies) == 0 {
		return false
	}
	now := float64(time.Now().Unix())
	for _, c := range cookies {
		expires := float64(c.Expires)
		if expires == 0 || expires > now {
			return true // at least one still-valid cookie
		}
	}
	return false
}

// Login opens a visible browser window to chat.deepseek.com and waits for
// the user to log in manually. Once the chat interface is detected (via
// chatDetectSelectors), it captures all cookies and localStorage entries
// from the page context and persists them through the session store.
//
// The browser is launched in non-headless mode so the user can interact
// with the login form. If the context carries a deadline, it is used as
// the timeout; otherwise DefaultLoginTimeout is applied.
//
// Returns:
//   - providers.ErrTimeout if the login exceeds the deadline
//   - providers.ErrAuthFailure if the page cannot be loaded or session
//     capture fails
//   - nil on success (session data is persisted)
func (p *Provider) Login(ctx context.Context, config providers.AuthConfig) error {
	// Determine timeout from the context deadline, or use the default.
	loginTimeout := DefaultLoginTimeout
	if deadline, ok := ctx.Deadline(); ok {
		loginTimeout = time.Until(deadline)
		if loginTimeout <= 0 {
			return &providers.ProviderError{
				Code:    providers.ErrTimeout.Code,
				Message: "login: context already expired",
			}
		}
	}

	log.Printf("deepseek login: launching browser, timeout=%v", loginTimeout)

	// Launch a visible browser so the user can log in manually.
	// Non-headless mode is essential for the manual login flow.
	l := launcher.New().
		Headless(true).
		Set("--no-sandbox").
		Set("--disable-gpu").
		Set("--disable-software-rasterizer").
		Set("--disable-dev-shm-usage")

	loginURL := loginPageURL
	if config.BaseURL != "" {
		loginURL = config.BaseURL
	}

	ctrlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("deepseek login: launch browser: %w", err)
	}

	browser := rod.New().ControlURL(ctrlURL)
	if err := browser.Connect(); err != nil {
		l.Kill()
		return fmt.Errorf("deepseek login: connect browser: %w", err)
	}
	defer browser.Close()

	// Create a new blank page and navigate to the login URL.
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return fmt.Errorf("deepseek login: create page: %w", err)
	}
	defer page.Close()

	if err := page.Navigate(loginURL); err != nil {
		return fmt.Errorf("deepseek login: navigate to %q: %w", loginURL, err)
	}

	// Wait for the page to finish loading.
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("deepseek login: wait for page load: %w", err)
	}
	_ = page.WaitStable(pageStableTimeout)

	// Quick check: is the chat interface already visible?
	// This covers the case where the user still has a valid session cookie
	// in the browser's default profile.
	if detectChatUI(page) {
		log.Print("deepseek login: chat interface already visible — session appears valid, capturing")
		return p.captureSession(browser, page)
	}

	log.Print("deepseek login: waiting for user to log in manually in the browser window...")

	// Poll until the chat interface appears or the deadline expires.
	deadline := time.Now().Add(loginTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &providers.ProviderError{
				Code:    providers.ErrTimeout.Code,
				Message: "login: cancelled or timed out",
			}
		default:
		}

		if detectChatUI(page) {
			log.Print("deepseek login: chat interface detected — capturing session")
			return p.captureSession(browser, page)
		}

		time.Sleep(pollInterval)
	}

	return &providers.ProviderError{
		Code:    providers.ErrTimeout.Code,
		Message: "login: timed out waiting for user to log in manually",
	}
}

// Prompt sends a chat completion request using the authenticated session.
// It manages session validity, browser automation, and retry logic.
//
// Flow:
//  1. Validate the request and build a prompt string from the message list.
//  2. If the saved session is invalid, re-authenticate via Login.
//  3. Launch a browser, inject the saved session (cookies + localStorage),
//     and navigate to chat.deepseek.com.
//  4. Wait for the chat UI to be ready, then type the question into the
//     input element and press Enter.
//  5. Poll for the assistant response element and extract its text.
//  6. On transient failure, retry up to promptMaxRetries times.
//
// Returns a ProviderError with:
//   - ErrInvalidRequest for malformed input
//   - ErrAuthFailure when session management fails
//   - ErrTimeout when the response does not arrive in time
func (p *Provider) Prompt(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	// Validate the request.
	if err := req.Validate(); err != nil {
		return nil, &providers.ProviderError{
			Code:    providers.ErrInvalidRequest.Code,
			Message: "deepseek prompt: " + err.Error(),
		}
	}

	// Build the question text from the message list.
	question := buildPromptText(req.Messages)
	if question == "" {
		return nil, &providers.ProviderError{
			Code:    providers.ErrInvalidRequest.Code,
			Message: "deepseek prompt: no text content in messages",
		}
	}

	log.Printf("deepseek prompt: sending %d-char question (model=%s, %d messages)",
		len(question), req.Model, len(req.Messages))

	var lastErr error
	for attempt := 0; attempt < promptMaxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("deepseek prompt: retry attempt %d/%d", attempt+1, promptMaxRetries)
			select {
			case <-ctx.Done():
				return nil, &providers.ProviderError{
					Code:    providers.ErrTimeout.Code,
					Message: "deepseek prompt: cancelled",
				}
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		result, err := p.promptOnce(ctx, req.Model, question)
		if err == nil {
			log.Printf("deepseek prompt: success (%d chars)", len(result.Choices[0].Message.Content))
			return result, nil
		}

		lastErr = err
		log.Printf("deepseek prompt: attempt %d failed: %v", attempt+1, err)
	}

	return nil, lastErr
}

// promptOnce executes a single prompt attempt without retries.
func (p *Provider) promptOnce(ctx context.Context, model, question string) (*providers.ChatResponse, error) {
	// 1. Ensure we have a valid session.
	if !p.IsSessionValid() {
		log.Print("deepseek prompt: session invalid, re-authenticating via Login")
		if err := p.Login(ctx, providers.AuthConfig{}); err != nil {
			return nil, fmt.Errorf("deepseek prompt: re-auth failed: %w", err)
		}
	}

	// 2. Load session data (cookies + localStorage).
	cookies, localStorage, err := p.store.Load()
	if err != nil {
		return nil, fmt.Errorf("deepseek prompt: load session: %w", err)
	}
	if len(cookies) == 0 {
		return nil, &providers.ProviderError{
			Code:    providers.ErrAuthFailure.Code,
			Message: "deepseek prompt: no session cookies — re-authentication needed",
		}
	}

	// 3. Launch the browser.
	l := launcher.New().
		Headless(true).
		Set("--no-sandbox").
		Set("--disable-gpu").
		Set("--disable-software-rasterizer").
		Set("--disable-dev-shm-usage").
		Set("--disable-blink-features=AutomationControlled")

	ctrlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("deepseek prompt: launch browser: %w", err)
	}

	browser := rod.New().ControlURL(ctrlURL)
	if err := browser.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("deepseek prompt: connect browser: %w", err)
	}
	defer browser.Close()

	// 4. Create a page and restore the session before navigating.
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("deepseek prompt: create page: %w", err)
	}
	defer page.Close()

	// Set cookies BEFORE navigation so the origin is recognised.
	params := cookiesToParams(cookies)
	if err := page.SetCookies(params); err != nil {
		return nil, fmt.Errorf("deepseek prompt: set cookies: %w", err)
	}

	// 5. Navigate to the chat page.
	if err := page.Navigate(loginPageURL); err != nil {
		return nil, fmt.Errorf("deepseek prompt: navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("deepseek prompt: wait load: %w", err)
	}
	_ = page.WaitStable(pageStableTimeout)

	// 6. Wait for the chat UI to be ready.
	if !waitForChatUI(page, responseTimeout) {
		return nil, &providers.ProviderError{
			Code:    providers.ErrTimeout.Code,
			Message: "deepseek prompt: chat interface did not appear",
		}
	}

	// 7. Restore localStorage if available (non-fatal).
	if len(localStorage) > 0 {
		restoreLocalStorageJS(page, localStorage)
	}

	log.Print("deepseek prompt: chat UI ready, typing question...")

	// 8. Locate the input element.
	inputEl := findInputElement(page)
	if inputEl == nil {
		return nil, &providers.ProviderError{
			Code:    providers.ErrAuthFailure.Code,
			Message: "deepseek prompt: could not find input element — page structure may have changed",
		}
	}

	// 9. Focus the input and type the question.
	if err := inputEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		log.Printf("deepseek prompt: click input warning: %v", err)
	}

	// Clear any placeholder/default text first.
	if err := inputEl.SelectAllText(); err == nil {
		_ = inputEl.Input("")
	}

	// Type the question. For form elements (textarea, input), Input()
	// sets the value directly with an input event. For contenteditable
	// elements, it inserts text at the cursor position.
	if err := inputEl.Input(question); err != nil {
		return nil, fmt.Errorf("deepseek prompt: type question: %w", err)
	}

	// 10. Submit by pressing Enter.
	log.Print("deepseek prompt: pressing Enter to submit...")
	if err := page.Keyboard.Press(input.Enter); err != nil {
		return nil, fmt.Errorf("deepseek prompt: press Enter: %w", err)
	}

	// 11. Wait for the response to appear.
	log.Print("deepseek prompt: waiting for response...")
	responseText, err := waitForResponse(page)
	if err != nil {
		return nil, err
	}

	log.Printf("deepseek prompt: received %d-char response", len(responseText))

	// 12. Build and return the ChatResponse.
	return &providers.ChatResponse{
		Model: model,
		Choices: []providers.ChatChoice{
			{
				Index:        0,
				Message:      providers.ChatMessage{Role: "assistant", Content: responseText},
				FinishReason: "stop",
			},
		},
	}, nil
}

// Close releases any resources held by the provider. Currently a no-op since
// browser instances are managed externally via the pool.
func (p *Provider) Close() error {
	return nil
}

// =============================================================================
// Internal helpers
// =============================================================================

// detectChatUI checks the page for any of the known chat interface selectors.
// Returns true if at least one element is found and visible.
func detectChatUI(page *rod.Page) bool {
	for _, sel := range chatDetectSelectors {
		el, err := page.Element(sel)
		if err != nil || el == nil {
			continue
		}
		visible, err := el.Visible()
		if err == nil && visible {
			log.Printf("deepseek login: chat UI detected via selector %q", sel)
			return true
		}
	}
	return false
}

// captureSession reads cookies and localStorage from the current browser
// session and persists them via the configured session store.
func (p *Provider) captureSession(browser *rod.Browser, page *rod.Page) error {
	log.Print("deepseek login: capturing cookies and localStorage")

	// 1. Capture all cookies from the browser context.
	// We use the browser-level GetCookies which returns all cookies
	// for all origins the user visited during login.
	cookies, err := browser.GetCookies()
	if err != nil {
		return fmt.Errorf("deepseek login: get cookies: %w", err)
	}

	// 2. Capture localStorage. Non-fatal on failure — many auth flows
	// rely solely on cookies.
	localStorage, err := captureLocalStorage(page)
	if err != nil {
		log.Printf("deepseek login: warning: could not capture localStorage: %v", err)
		localStorage = nil
	}

	// 3. Persist via the session store.
	if err := p.store.Save(cookies, localStorage); err != nil {
		return fmt.Errorf("deepseek login: save session: %w", err)
	}

	log.Printf("deepseek login: session saved successfully (%d cookies, %d localStorage keys)",
		len(cookies), len(localStorage))

	return nil
}

// captureLocalStorage extracts the page's window.localStorage as a
// map[string]string by evaluating JS in the page context.
func captureLocalStorage(page *rod.Page) (map[string]string, error) {
	eval, err := page.Eval(`() => JSON.stringify(window.localStorage)`)
	if err != nil {
		return nil, fmt.Errorf("eval localStorage: %w", err)
	}

	if eval == nil || eval.Value.Val() == nil {
		return map[string]string{}, nil
	}

	jsonStr := eval.Value.Str()
	if jsonStr == "" || jsonStr == "{}" {
		return map[string]string{}, nil
	}

	var ls map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &ls); err != nil {
		return nil, fmt.Errorf("parse localStorage JSON: %w", err)
	}

	if ls == nil {
		return map[string]string{}, nil
	}

	return ls, nil
}

// =============================================================================
// Prompt helpers
// =============================================================================

// buildPromptText concatenates the message list into a single prompt string.
// It formats each message as "role: content" with newline separation.
func buildPromptText(messages []providers.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}

	// Fast path: single message, just return its content.
	if len(messages) == 1 {
		return messages[0].Content
	}

	// Multi-turn: include role-prefixed messages for context.
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n")
		}
		if m.Role != "" {
			b.WriteString(m.Role)
			b.WriteString(": ")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

// findInputElement locates the chat input element using the inputSelectors
// list. It tries each selector in order and returns the first visible match.
// Returns nil if no visible input element is found.
func findInputElement(page *rod.Page) *rod.Element {
	for _, sel := range inputSelectors {
		el, err := page.Element(sel)
		if err != nil || el == nil {
			continue
		}
		visible, err := el.Visible()
		if err == nil && visible {
			log.Printf("deepseek prompt: input found via selector %q", sel)
			return el
		}
	}
	return nil
}

// findResponseElement locates assistant response elements using the
// responseSelectors list. Returns all matching elements for the first
// selector that has at least one match. Returns nil if no matches.
func findResponseElement(page *rod.Page) []*rod.Element {
	for _, sel := range responseSelectors {
		elements, err := page.Elements(sel)
		if err != nil || len(elements) == 0 {
			continue
		}
		log.Printf("deepseek prompt: found %d response candidate(s) via selector %q", len(elements), sel)
		return elements
	}
	return nil
}

// waitForChatUI polls the page until the chat interface is detected or the
// timeout expires. It uses the same chatDetectSelectors as Login.
func waitForChatUI(page *rod.Page, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if detectChatUI(page) {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

// waitForResponse polls for the assistant response element after submitting
// a question. It handles streaming responses by waiting for the element to
// have non-empty text and then waiting briefly for the stream to finish.
func waitForResponse(page *rod.Page) (string, error) {
	deadline := time.Now().Add(responseTimeout)
	var lastText string

	for time.Now().Before(deadline) {
		elements := findResponseElement(page)
		if len(elements) > 0 {
			// Take the last element — it should be the most recent response.
			lastEl := elements[len(elements)-1]
			text, err := lastEl.Text()
			if err == nil {
				text = strings.TrimSpace(text)
				if text != "" && text != lastText {
					lastText = text
					// The response is still streaming. Wait briefly then
					// check for more content to avoid truncation.
					if len(elements) > 1 || isStreamingFinished(page, lastEl, text) {
						log.Printf("deepseek prompt: response stabilized at %d chars", len(text))
						return text, nil
					}
				}
			}
		}

		// Check for context cancellation.
		select {
		case <-time.After(responsePollInterval):
		default:
		}
	}

	if lastText != "" {
		// We have partial response text — return it even though we timed out.
		log.Printf("deepseek prompt: timed out with partial response (%d chars)", len(lastText))
		return lastText, nil
	}

	return "", &providers.ProviderError{
		Code:    providers.ErrTimeout.Code,
		Message: "deepseek prompt: no response appeared",
	}
}

// isStreamingFinished checks whether the response element has stabilised by
// waiting a short time and comparing the text again. This prevents returning
// a truncated response when the AI is still generating.
func isStreamingFinished(page *rod.Page, lastEl *rod.Element, knownText string) bool {
	time.Sleep(responseStableDelay)
	text, err := lastEl.Text()
	if err != nil {
		return true // Can't read it, return what we have.
	}
	text = strings.TrimSpace(text)
	return text == knownText || len(text) <= len(knownText)+5
}

// cookiesToParams converts a slice of rod NetworkCookie (the type returned
// by session store Load) to NetworkCookieParam (the type needed by
// page.SetCookies). This bridges the two cookie representations.
func cookiesToParams(cookies []*proto.NetworkCookie) []*proto.NetworkCookieParam {
	params := make([]*proto.NetworkCookieParam, len(cookies))
	for i, c := range cookies {
		params[i] = &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			Expires:  c.Expires,
		}
	}
	return params
}

// restoreLocalStorageJS restores page localStorage entries by evaluating
// JavaScript. This is best-effort — failures are logged but not returned.
func restoreLocalStorageJS(page *rod.Page, localStorage map[string]string) {
	for key, value := range localStorage {
		expr := fmt.Sprintf("() => { try { window.localStorage.setItem(%q, %q); return true; } catch(e) { return false; } }", key, value)
		_, err := page.Eval(expr)
		if err != nil {
			log.Printf("deepseek prompt: warning: failed to set localStorage %q: %v", key, err)
		}
	}
	log.Printf("deepseek prompt: restored %d localStorage entries", len(localStorage))
}
