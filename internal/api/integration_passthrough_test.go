package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// TestAPIKeyPassthroughIntegration tests the complete flow of API key passthrough:
// 1. Client sends request with Authorization header
// 2. AuthMiddleware extracts and stores the API key in Gin context
// 3. Handler creates context with embedded Gin context
// 4. ResolveAPIKeyWithPassthrough retrieves the client's API key
func TestAPIKeyPassthroughIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name             string
		authHeader       string
		configuredAPIKey string
		wantResolvedKey  string
	}{
		{
			name:             "Passthrough with Bearer token",
			authHeader:       "Bearer sk-ant-client-key-xyz",
			configuredAPIKey: config.APIKeyPassthroughPlaceholder,
			wantResolvedKey:  "sk-ant-client-key-xyz",
		},
		{
			name:             "Normal API key (not passthrough)",
			authHeader:       "Bearer sk-ant-client-key-abc",
			configuredAPIKey: "sk-ant-configured-key-123",
			wantResolvedKey:  "sk-ant-configured-key-123",
		},
		{
			name:             "Passthrough with no Authorization header",
			authHeader:       "",
			configuredAPIKey: config.APIKeyPassthroughPlaceholder,
			wantResolvedKey:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test request
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			// Create Gin context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			// Run AuthMiddleware
			middleware := AuthMiddleware(nil)
			middleware(c)

			// Simulate handler creating context with embedded Gin context
			// (This is what happens in GetContextWithCancel)
			handlerCtx := context.WithValue(context.Background(), "gin", c)

			// Test ResolveAPIKeyWithPassthrough
			resolvedKey := helps.ResolveAPIKeyWithPassthrough(handlerCtx, tt.configuredAPIKey)

			if resolvedKey != tt.wantResolvedKey {
				t.Errorf("ResolveAPIKeyWithPassthrough() = %v, want %v", resolvedKey, tt.wantResolvedKey)
			}
		})
	}
}
