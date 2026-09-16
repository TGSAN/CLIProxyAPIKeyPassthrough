package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

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
