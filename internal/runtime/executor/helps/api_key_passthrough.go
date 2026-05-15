package helps

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ResolveAPIKeyWithPassthrough checks if the provided API key is the passthrough placeholder or empty.
// If it is the placeholder or empty/missing, it extracts the client's API key from the Gin context (stored by AuthMiddleware).
// Otherwise, it returns the provided API key as-is.
//
// Parameters:
//   - ctx: The request context (should contain the Gin context with "userApiKey")
//   - configuredAPIKey: The API key from the configuration (may be the placeholder, empty, or a real key)
//
// Returns:
//   - The resolved API key to use for upstream requests
func ResolveAPIKeyWithPassthrough(ctx context.Context, configuredAPIKey string) string {
	// If the configured API key is the passthrough placeholder or empty/missing,
	// try to get the client's API key from the Gin context
	if configuredAPIKey == config.APIKeyPassthroughPlaceholder || strings.TrimSpace(configuredAPIKey) == "" {
		// Extract the client's API key from the Gin context
		ginCtx, ok := ctx.Value("gin").(*gin.Context)
		if !ok || ginCtx == nil {
			// CRITICAL: If we can't get the Gin context when passthrough is configured,
			// return empty string to avoid sending the literal placeholder to upstream.
			// This will cause authentication to fail, which is appropriate since we
			// cannot fulfill the passthrough requirement.
			return ""
		}

		// Get the user's API key that was set by AuthMiddleware
		userAPIKey, exists := ginCtx.Get("userApiKey")
		if !exists {
			// No user API key was captured, return empty to fail auth
			// (better to fail than to send placeholder literal)
			return ""
		}

		// Convert to string and return
		if key, ok := userAPIKey.(string); ok {
			return strings.TrimSpace(key)
		}

		// If the userApiKey wasn't a string, return empty
		return ""
	}

	// The configured API key is not the passthrough placeholder and not empty,
	// return it as-is
	return configuredAPIKey
}
