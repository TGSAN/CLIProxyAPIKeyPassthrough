package executor

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// codexSessionTranslateInstallationID is the fixed installation identity sent
	// upstream alongside the translated session UUID.
	codexSessionTranslateInstallationID = "00000000-0000-0004-0000-000000000000"
	// codexSessionTranslateWindowID is the fixed window identity ("<installation-id>:0").
	codexSessionTranslateWindowID = codexSessionTranslateInstallationID + ":0"
)

// codexSessionTranslateContextKey carries the translated session UUID from the body
// translation stage in cacheHelper to the later header stage of the same request.
type codexSessionTranslateContextKey struct{}

func withCodexSessionTranslatedSession(ctx context.Context, sessionUUID string) context.Context {
	if ctx == nil || sessionUUID == "" {
		return ctx
	}
	return context.WithValue(ctx, codexSessionTranslateContextKey{}, sessionUUID)
}

func codexSessionTranslatedSession(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(codexSessionTranslateContextKey{}).(string)
	return value
}

// codexSessionUUIDTranslateEnabled reports whether the selected credential enables
// session-uuid-translate for its upstream requests.
func codexSessionUUIDTranslateEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeCodexSessionUUIDTranslate]), "true")
}

// codexSessionTranslateClientHeaders returns the explicit client headers or falls back
// to the Gin request headers carried on the context.
func codexSessionTranslateClientHeaders(ctx context.Context, headers http.Header) http.Header {
	if headers != nil {
		return headers
	}
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header
	}
	return nil
}

// codexSessionTranslateRawID resolves the downstream session identity, preferring the
// client body's prompt_cache_key, then explicit client session headers.
func codexSessionTranslateRawID(payload []byte, headers http.Header) string {
	if value := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); value != "" {
		return value
	}
	return codexSessionTranslateRawHeaderID(headers)
}

// codexSessionTranslateRawHeaderID resolves a session identity from client headers in
// the documented priority order.
func codexSessionTranslateRawHeaderID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, name := range []string{"Session-Id", "Session_id", "X-Client-Request-Id", "X-Claude-Code-Session-Id", "X-Request-Id", "X-Conversation-ID"} {
		if value := headerValueCaseInsensitive(headers, name); value != "" {
			return value
		}
	}
	// Generic X-*-Session-Id: sorted key order keeps the choice deterministic.
	var generic []string
	for key := range headers {
		normalized := strings.ToUpper(strings.TrimSpace(key))
		if strings.HasPrefix(normalized, "X-") && strings.HasSuffix(normalized, "-SESSION-ID") && normalized != "X-CLAUDE-CODE-SESSION-ID" {
			generic = append(generic, key)
		}
	}
	sort.Strings(generic)
	for _, key := range generic {
		for _, value := range headers[key] {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// codexSessionUUIDv7TimeWindow truncates mapped UUIDv7 timestamps: upstream validates
// the embedded time, so mapped values carry the real clock time rounded down to this
// window. A raw value stays reproducible within one window; across windows the value
// rotates, which aligns with upstream prompt-cache TTL expiry.
// ponytail: fixed 10m window; add config knob only if rotation measurably hurts cache hits.
const codexSessionUUIDv7TimeWindow = 10 * time.Minute

func codexSessionUUIDv7(raw string) string {
	return codexSessionUUIDv7At(raw, time.Now())
}

// codexSessionUUIDv7At is codexSessionUUIDv7 with an explicit clock for tests.
func codexSessionUUIDv7At(raw string, now time.Time) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, errParse := uuid.Parse(raw); errParse == nil && parsed.Version() == uuid.Version(7) && parsed.Variant() == uuid.RFC4122 {
		return parsed.String()
	}
	mapped := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:session-uuid-v7:"+raw))
	windowMS := int64(codexSessionUUIDv7TimeWindow / time.Millisecond)
	timestamp := uint64(now.UnixMilli() / windowMS * windowMS)
	mapped[0] = byte(timestamp >> 40)
	mapped[1] = byte(timestamp >> 32)
	mapped[2] = byte(timestamp >> 24)
	mapped[3] = byte(timestamp >> 16)
	mapped[4] = byte(timestamp >> 8)
	mapped[5] = byte(timestamp)
	mapped[6] = (mapped[6] & 0x0F) | 0x70
	mapped[8] = (mapped[8] & 0x3F) | 0x80
	return mapped.String()
}

// codexSessionTranslateSession maps a resolved downstream session identity to its
// translated UUIDv7 and logs the result for debugging.
func codexSessionTranslateSession(ctx context.Context, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sessionUUID := codexSessionUUIDv7(raw)
	helps.LogWithRequestID(ctx).Infof("codex session-uuid-translate: downstream session %q translated to session UUID %s", raw, sessionUUID)
	return sessionUUID
}

// codexSessionTranslateConversationRoot derives a stable conversation identity from the
// original payload: the ordered contents of the first two user messages and the first
// assistant message (type=message items from input/messages, nested request included),
// joined with the downstream API key. It returns "" when the conversation does not yet
// contain at least two user messages and one assistant message.
func codexSessionTranslateConversationRoot(ctx context.Context, payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	items := gjson.GetBytes(payload, "input")
	if !items.IsArray() {
		items = gjson.GetBytes(payload, "messages")
	}
	if !items.IsArray() {
		nested := gjson.GetBytes(payload, "request")
		if items = nested.Get("input"); !items.IsArray() {
			items = nested.Get("messages")
		}
	}
	if !items.IsArray() {
		return ""
	}
	var contents []string
	var userCount, assistantCount int
	for _, item := range items.Array() {
		if itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String())); itemType != "" && itemType != "message" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		content := strings.TrimSpace(item.Get("content").Raw)
		if content == "" {
			continue
		}
		switch role {
		case "user":
			if userCount < 2 {
				contents = append(contents, content)
				userCount++
			}
		case "assistant":
			if assistantCount == 0 {
				contents = append(contents, content)
				assistantCount++
			}
		}
		if userCount == 2 && assistantCount == 1 {
			break
		}
	}
	if userCount != 2 || assistantCount != 1 {
		return ""
	}
	contents = append(contents, helps.APIKeyFromContext(ctx))
	return "conversation-root:" + strings.Join(contents, "\x00")
}

// codexSessionTranslateUUID resolves the downstream session identity for a credential
// with session-uuid-translate enabled and maps it to its translated UUIDv7.
// Resolution order: downstream signals, then the conversation-root message hash (first
// two user + first assistant message plus the downstream API key), then derivedID (the
// request's own execution / conversation-derived identity), then a fresh random UUIDv7.
// derivedID must never be a per-credential constant, otherwise every conversation
// would share one session UUID.
func codexSessionTranslateUUID(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, derivedID string, clientHeaders http.Header) string {
	if !codexSessionUUIDTranslateEnabled(auth) {
		return ""
	}
	raw := codexSessionTranslateRawID(payload, clientHeaders)
	if raw == "" {
		raw = codexSessionTranslateConversationRoot(ctx, payload)
	}
	if raw == "" {
		raw = strings.TrimSpace(derivedID)
	}
	if raw != "" {
		return codexSessionTranslateSession(ctx, raw)
	}
	fresh, errNew := uuid.NewV7()
	if errNew != nil {
		helps.LogWithRequestID(ctx).Warnf("codex session-uuid-translate: no downstream session found and UUIDv7 generation failed: %v", errNew)
		return ""
	}
	helps.LogWithRequestID(ctx).Infof("codex session-uuid-translate: no downstream session found, generated session UUID %s", fresh.String())
	return fresh.String()
}

// codexSessionTranslateTurnMetadata rewrites the session-bound fields of a Codex
// turn-metadata JSON string so it agrees with the translated session UUID.
func codexSessionTranslateTurnMetadata(rawTurnMetadata string, sessionUUID string) string {
	if rawTurnMetadata == "" || !gjson.Valid(rawTurnMetadata) {
		return rawTurnMetadata
	}
	updated := rawTurnMetadata
	if gjson.Get(rawTurnMetadata, "prompt_cache_key").Exists() {
		updated, _ = sjson.Set(updated, "prompt_cache_key", sessionUUID)
	}
	if gjson.Get(rawTurnMetadata, "window_id").Exists() {
		updated, _ = sjson.Set(updated, "window_id", codexSessionTranslateWindowID)
	}
	return updated
}

// applyCodexSessionTranslateBody stamps the translated session identity onto every
// session-bound body field: prompt_cache_key becomes the translated session UUID, the
// client_metadata installation/window IDs become the fixed identities, and an existing
// client_metadata turn-metadata string is rewritten to match. Upstream cross-checks
// these values, so leaving a forwarded original in place would be rejected.
func applyCodexSessionTranslateBody(body []byte, sessionUUID string) []byte {
	if len(body) == 0 || sessionUUID == "" {
		return body
	}
	body = helps.SetStringIfDifferent(body, "prompt_cache_key", sessionUUID)
	body, _ = sjson.SetBytes(body, "client_metadata.x-codex-installation-id", codexSessionTranslateInstallationID)
	body, _ = sjson.SetBytes(body, "client_metadata.x-codex-window-id", codexSessionTranslateWindowID)
	if turnMetadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata"); turnMetadata.Exists() && turnMetadata.Type == gjson.String {
		if updated := codexSessionTranslateTurnMetadata(turnMetadata.String(), sessionUUID); updated != turnMetadata.String() {
			body, _ = sjson.SetBytes(body, "client_metadata.x-codex-turn-metadata", updated)
		}
	}
	return body
}

// applyCodexSessionTranslateHeaders forces every session-bound upstream header to the
// translated session identity: X-Client-Request-Id, session-id and X-Codex-Window-Id
// are always set; forwarded Thread-Id, Conversation_id and X-Codex-Turn-Metadata are
// rewritten only when present so no stale original identity survives.
func applyCodexSessionTranslateHeaders(headers http.Header, sessionUUID string) {
	if headers == nil || sessionUUID == "" {
		return
	}
	headers.Set("X-Client-Request-Id", sessionUUID)
	setCodexSessionHeaderCasePreserved(headers, "Session-Id", sessionUUID)
	headers.Set("X-Codex-Window-Id", codexSessionTranslateWindowID)
	if headerValueCaseInsensitive(headers, "Thread-Id") != "" {
		setHeaderCasePreserved(headers, "Thread-Id", sessionUUID)
	}
	if headerValueCaseInsensitive(headers, "Conversation_id") != "" {
		setHeaderCasePreserved(headers, "Conversation_id", sessionUUID)
	}
	if rawTurnMetadata := headerValueCaseInsensitive(headers, "X-Codex-Turn-Metadata"); rawTurnMetadata != "" {
		setHeaderCasePreserved(headers, "X-Codex-Turn-Metadata", codexSessionTranslateTurnMetadata(rawTurnMetadata, sessionUUID))
	}
}

// applyCodexSessionTranslateHeadersFromContext applies the header stage using the
// session UUID the body stage stashed on the request context. Callers invoke it last
// so the translated identity wins over client-forwarded and identity-confuse values.
func applyCodexSessionTranslateHeadersFromContext(r *http.Request) {
	if r == nil {
		return
	}
	applyCodexSessionTranslateHeaders(r.Header, codexSessionTranslatedSession(r.Context()))
}
