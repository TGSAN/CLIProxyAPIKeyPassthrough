package helps

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ResolveAPIKeyWithPassthrough checks if the provided API key is the passthrough placeholder.
// If it is, it extracts the client's API key from the Gin context (stored by AuthMiddleware).
// Otherwise, it returns the provided API key as-is.
//
// Parameters:
//   - ctx: The request context (should contain the Gin context with "userApiKey")
//   - configuredAPIKey: The API key from the configuration (may be the placeholder)
//
// Returns:
//   - The resolved API key to use for upstream requests
func ResolveAPIKeyWithPassthrough(ctx context.Context, configuredAPIKey string) string {
	// If the configured API key is not the passthrough placeholder, return it as-is
	if configuredAPIKey != config.APIKeyPassthroughPlaceholder {
		return configuredAPIKey
	}

	// Extract the client's API key from the Gin context
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		// If we can't get the Gin context, return empty string
		// This will likely cause authentication to fail, which is appropriate
		return ""
	}

	// Get the user's API key that was set by AuthMiddleware
	userAPIKey, exists := ginCtx.Get("userApiKey")
	if !exists {
		return ""
	}

	// Convert to string and return
	if key, ok := userAPIKey.(string); ok {
		return strings.TrimSpace(key)
	}

	return ""
}
