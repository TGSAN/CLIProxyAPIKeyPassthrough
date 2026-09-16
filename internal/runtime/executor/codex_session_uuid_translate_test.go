package executor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func translateTestAuth(enabled bool) *cliproxyauth.Auth {
	attrs := map[string]string{}
	if enabled {
		attrs[cliproxyauth.AttributeCodexSessionUUIDTranslate] = "true"
	}
	return &cliproxyauth.Auth{ID: "codex-api-key", Provider: "codex", Attributes: attrs}
}

func TestCodexSessionUUIDv7PassesThroughValidUUIDv7(t *testing.T) {
	t.Parallel()

	const valid = "019d2233-e240-7162-992d-38df0a2a0e0d"
	if got := codexSessionUUIDv7(valid); got != valid {
		t.Fatalf("codexSessionUUIDv7(%q) = %q, want unchanged", valid, got)
	}
}

func TestCodexSessionUUIDv7MapsNonV7Deterministically(t *testing.T) {
	t.Parallel()

	const raw = "my-session-123"
	first := codexSessionUUIDv7(raw)
	second := codexSessionUUIDv7(raw)
	if first != second {
		t.Fatalf("mapping not deterministic: %q vs %q", first, second)
	}
	parsed, errParse := uuid.Parse(first)
	if errParse != nil {
		t.Fatalf("mapped value %q is not a UUID: %v", first, errParse)
	}
	if parsed.Version() != uuid.Version(7) || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("mapped value %q is not UUIDv7 (version=%d variant=%d)", first, parsed.Version(), parsed.Variant())
	}
	// UUIDv4 inputs (wrong version bits) must be remapped, not passed through.
	if got := codexSessionUUIDv7("66666666-7777-4888-8999-aaaaaaaaaaaa"); got == "66666666-7777-4888-8999-aaaaaaaaaaaa" {
		t.Fatal("UUIDv4 input unexpectedly passed through unchanged")
	}
}

func TestCodexSessionUUIDv7EmbedsCurrentTimestamp(t *testing.T) {
	t.Parallel()

	fixed := time.UnixMilli(1758060000123).UTC()
	mapped := uuid.MustParse(codexSessionUUIDv7At("raw-session", fixed))

	embedded := int64(binary.BigEndian.Uint64(append([]byte{0, 0}, mapped[:6]...)))
	if want := fixed.UnixMilli() / 600000 * 600000; embedded != want {
		t.Fatalf("embedded timestamp = %d, want %d", embedded, want)
	}
	if mapped.Version() != uuid.Version(7) || mapped.Variant() != uuid.RFC4122 {
		t.Fatalf("mapped value %q is not UUIDv7", mapped)
	}

	// Stable inside one window, rotates into the next window.
	if again := codexSessionUUIDv7At("raw-session", fixed.Add(9*time.Minute+59*time.Second)); again != mapped.String() {
		t.Fatalf("value rotated inside window: %q vs %q", again, mapped)
	}
	if rotated := codexSessionUUIDv7At("raw-session", fixed.Add(10*time.Minute)); rotated == mapped.String() {
		t.Fatal("value did not rotate across windows")
	}
}
func TestCodexSessionTranslateRawIDPrecedence(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Session-Id", "header-session")
	headers.Set("X-Client-Request-Id", "request-session")
	headers.Set("X-Conversation-ID", "conversation-session")

	if got := codexSessionTranslateRawID([]byte(`{"prompt_cache_key":"body-session"}`), headers); got != "body-session" {
		t.Fatalf("body prompt_cache_key precedence = %q, want body-session", got)
	}
	if got := codexSessionTranslateRawID(nil, headers); got != "header-session" {
		t.Fatalf("Session-Id precedence = %q, want header-session", got)
	}

	onlyGeneric := http.Header{"X-Foo-Session-Id": {"generic-session"}}
	if got := codexSessionTranslateRawHeaderID(onlyGeneric); got != "generic-session" {
		t.Fatalf("X-*-Session-Id fallback = %q, want generic-session", got)
	}

	requestID := http.Header{"X-Request-Id": {"request-only"}}
	if got := codexSessionTranslateRawHeaderID(requestID); got != "request-only" {
		t.Fatalf("X-Request-Id fallback = %q, want request-only", got)
	}

	if got := codexSessionTranslateRawID(nil, http.Header{}); got != "" {
		t.Fatalf("empty headers = %q, want empty", got)
	}
}

func TestApplyCodexSessionTranslate(t *testing.T) {
	t.Parallel()

	auth := translateTestAuth(true)
	headers := http.Header{}
	headers.Set("X-Client-Request-Id", "019d2233-e240-7162-992d-38df0a2a0e0d")

	body := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"legacy-key","client_metadata":{"x-codex-installation-id":"install-1"}}`)
	sessionUUID := codexSessionTranslateUUID(context.Background(), auth, []byte(`{"prompt_cache_key":"legacy-key"}`), "", headers)
	if sessionUUID == "" {
		t.Fatal("translated session UUID is empty")
	}
	if parsed, errParse := uuid.Parse(sessionUUID); errParse != nil || parsed.Version() != uuid.Version(7) {
		t.Fatalf("translated session %q is not UUIDv7", sessionUUID)
	}

	body = applyCodexSessionTranslateBody(body, sessionUUID)
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != sessionUUID {
		t.Fatalf("prompt_cache_key = %q, want %q", got, sessionUUID)
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != codexSessionTranslateInstallationID {
		t.Fatalf("client_metadata.x-codex-installation-id = %q, want %q", got, codexSessionTranslateInstallationID)
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(); got != codexSessionTranslateWindowID {
		t.Fatalf("client_metadata.x-codex-window-id = %q, want %q", got, codexSessionTranslateWindowID)
	}

	applyCodexSessionTranslateHeaders(headers, sessionUUID)
	if got := headers.Get("X-Client-Request-Id"); got != sessionUUID {
		t.Fatalf("X-Client-Request-Id = %q, want %q", got, sessionUUID)
	}
	if got := codexSessionHeaderValue(headers); got != sessionUUID {
		t.Fatalf("session header = %q, want %q", got, sessionUUID)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != codexSessionTranslateWindowID {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", got, codexSessionTranslateWindowID)
	}
}

func TestApplyCodexSessionTranslateDisabledKeepsRequestUnchanged(t *testing.T) {
	t.Parallel()

	auth := translateTestAuth(false)
	body := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"legacy-key"}`)
	if got := codexSessionTranslateUUID(context.Background(), auth, body, "", http.Header{}); got != "" {
		t.Fatalf("disabled translate returned %q, want empty", got)
	}
	unchanged := applyCodexSessionTranslateBody(body, "")
	if string(unchanged) != string(body) {
		t.Fatalf("body changed without session UUID: %s", unchanged)
	}
}

func TestCodexSessionTranslateUUIDFallbacksWhenNoDownstreamSignal(t *testing.T) {
	t.Parallel()

	auth := translateTestAuth(true)

	// Derived executor identity is used when the client sent no session signal.
	derived := codexSessionTranslateUUID(context.Background(), auth, nil, "derived-session-id", http.Header{})
	if parsed, errParse := uuid.Parse(derived); errParse != nil || parsed.Version() != uuid.Version(7) {
		t.Fatalf("derived fallback %q is not UUIDv7: %v", derived, errParse)
	}

	// With neither downstream signal nor derived identity, a fresh UUIDv7 is generated.
	fresh := codexSessionTranslateUUID(context.Background(), auth, nil, "", http.Header{})
	if parsed, errParse := uuid.Parse(fresh); errParse != nil || parsed.Version() != uuid.Version(7) {
		t.Fatalf("generated fallback %q is not UUIDv7: %v", fresh, errParse)
	}
	if again := codexSessionTranslateUUID(context.Background(), auth, nil, "", http.Header{}); again == fresh {
		t.Fatal("random fallback unexpectedly deterministic across requests")
	}
}

func translateTestContextWithAPIKey(apiKey string) context.Context {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", apiKey)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestCodexSessionTranslateConversationRoot(t *testing.T) {
	t.Parallel()

	auth := translateTestAuth(true)
	payload := []byte(`{"model":"gpt-5-codex","input":[
		{"type":"message","role":"user","content":"first prompt"},
		{"type":"message","role":"assistant","content":"first answer"},
		{"type":"message","role":"user","content":"second prompt"},
		{"type":"message","role":"assistant","content":"second answer"},
		{"type":"message","role":"user","content":"third prompt"}
	]}`)

	ctxA := translateTestContextWithAPIKey("downstream-key-a")
	ctxB := translateTestContextWithAPIKey("downstream-key-b")

	first := codexSessionTranslateUUID(ctxA, auth, payload, "", http.Header{})
	if parsed, errParse := uuid.Parse(first); errParse != nil || parsed.Version() != uuid.Version(7) {
		t.Fatalf("conversation-root UUID %q is not UUIDv7: %v", first, errParse)
	}

	// Exact root value: first two user + first assistant contents, in order, with the key.
	wantRoot := "conversation-root:" + strings.Join([]string{`"first prompt"`, `"first answer"`, `"second prompt"`, "downstream-key-a"}, "\x00")
	if gotRoot := codexSessionTranslateConversationRoot(ctxA, payload); gotRoot != wantRoot {
		t.Fatalf("conversation root = %q, want %q", gotRoot, wantRoot)
	}

	// Same conversation + same key: stable across requests (later turns included).
	if repeat := codexSessionTranslateUUID(ctxA, auth, payload, "", http.Header{}); repeat != first {
		t.Fatalf("conversation-root UUID changed: %q vs %q", first, repeat)
	}

	// Same conversation, different downstream API key: different session UUID.
	if other := codexSessionTranslateUUID(ctxB, auth, payload, "", http.Header{}); other == first {
		t.Fatal("different API keys produced the same conversation-root UUID")
	}

	// Explicit downstream session signal still wins over the conversation root.
	headers := http.Header{}
	headers.Set("Session-Id", "explicit-session")
	if got := codexSessionTranslateUUID(ctxA, auth, payload, "", headers); got != codexSessionUUIDv7("explicit-session") {
		t.Fatalf("explicit session precedence = %q, want %q", got, codexSessionUUIDv7("explicit-session"))
	}
}

func TestCodexSessionTranslateConversationRootNeedsTwoUserOneAssistant(t *testing.T) {
	t.Parallel()

	ctx := translateTestContextWithAPIKey("downstream-key")

	short := []byte(`{"input":[{"type":"message","role":"user","content":"a"},{"type":"message","role":"assistant","content":"b"}]}`)
	if got := codexSessionTranslateConversationRoot(ctx, short); got != "" {
		t.Fatalf("conversation root with one user message = %q, want empty", got)
	}

	noAssistant := []byte(`{"input":[{"type":"message","role":"user","content":"a"},{"type":"message","role":"user","content":"b"}]}`)
	if got := codexSessionTranslateConversationRoot(ctx, noAssistant); got != "" {
		t.Fatalf("conversation root without assistant message = %q, want empty", got)
	}

	messages := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	if got := codexSessionTranslateConversationRoot(ctx, messages); got == "" {
		t.Fatal("chat-completions messages shape produced no conversation root")
	}
}

func TestApplyCodexSessionTranslateBodyIsValidJSON(t *testing.T) {
	t.Parallel()

	body := applyCodexSessionTranslateBody([]byte(`{"model":"gpt-5-codex"}`), codexSessionUUIDv7("raw-session"))
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(body, &decoded); errUnmarshal != nil {
		t.Fatalf("translated body is not valid JSON: %v", errUnmarshal)
	}
	metadata, ok := decoded["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata missing or not an object: %s", body)
	}
	if metadata["x-codex-installation-id"] != codexSessionTranslateInstallationID {
		t.Fatalf("x-codex-installation-id = %v, want %q", metadata["x-codex-installation-id"], codexSessionTranslateInstallationID)
	}
	if metadata["x-codex-window-id"] != codexSessionTranslateWindowID {
		t.Fatalf("x-codex-window-id = %v, want %q", metadata["x-codex-window-id"], codexSessionTranslateWindowID)
	}
}
