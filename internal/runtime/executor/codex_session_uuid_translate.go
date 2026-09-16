package executor

import (
	"context"
	"net/http"
	"sort"
	"strings"

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

// codexSessionUUIDv7 maps an arbitrary session identity onto a deterministic UUIDv7.
// Values that are already UUIDv7 pass through unchanged. Other values are hash-mapped
// and stamped with the UUIDv7 version/variant bits: collisions are allowed, but the
// result is always inferable from the original value alone, without any lookup table.
func codexSessionUUIDv7(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, errParse := uuid.Parse(raw); errParse == nil && parsed.Version() == uuid.Version(7) && parsed.Variant() == uuid.RFC4122 {
		return parsed.String()
	}
	mapped := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:session-uuid-v7:"+raw))
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

// codexSessionTranslateUUID resolves the downstream session identity for a credential
// with session-uuid-translate enabled and maps it to its translated UUIDv7. When no
// downstream signal exists it falls back to the executor-derived session identity
// (fallbackID), and only then generates a fresh random UUIDv7, so an enabled
// credential always injects a session UUID.
func codexSessionTranslateUUID(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, fallbackID string, clientHeaders http.Header) string {
	if !codexSessionUUIDTranslateEnabled(auth) {
		return ""
	}
	raw := codexSessionTranslateRawID(payload, clientHeaders)
	if raw == "" {
		raw = strings.TrimSpace(fallbackID)
	}
	if raw == "" {
		fresh, errNew := uuid.NewV7()
		if errNew != nil {
			helps.LogWithRequestID(ctx).Warnf("codex session-uuid-translate: no downstream session found and UUIDv7 generation failed: %v", errNew)
			return ""
		}
		helps.LogWithRequestID(ctx).Infof("codex session-uuid-translate: no downstream session found, generated session UUID %s", fresh.String())
		return fresh.String()
	}
	return codexSessionTranslateSession(ctx, raw)
}

// applyCodexSessionTranslateBody stamps the translated session identity onto the
// upstream body: prompt_cache_key becomes the translated session UUID and the
// client_metadata Codex identity fields become the fixed installation/window IDs.
func applyCodexSessionTranslateBody(body []byte, sessionUUID string) []byte {
	if len(body) == 0 || sessionUUID == "" {
		return body
	}
	body = helps.SetStringIfDifferent(body, "prompt_cache_key", sessionUUID)
	body, _ = sjson.SetBytes(body, "client_metadata.x-codex-installation-id", codexSessionTranslateInstallationID)
	body, _ = sjson.SetBytes(body, "client_metadata.x-codex-window-id", codexSessionTranslateWindowID)
	return body
}

// applyCodexSessionTranslateHeaders forces the upstream X-Client-Request-Id,
// session-id and X-Codex-Window-Id headers to the translated session UUID.
func applyCodexSessionTranslateHeaders(headers http.Header, sessionUUID string) {
	if headers == nil || sessionUUID == "" {
		return
	}
	headers.Set("X-Client-Request-Id", sessionUUID)
	setCodexSessionHeaderCasePreserved(headers, "Session-Id", sessionUUID)
	headers.Set("X-Codex-Window-Id", codexSessionTranslateWindowID)
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
