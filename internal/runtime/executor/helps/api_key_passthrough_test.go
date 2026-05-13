package helps

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestResolveAPIKeyWithPassthrough(t *testing.T) {
	tests := []struct {
		name             string
		configuredAPIKey string
		userAPIKey       string
		wantAPIKey       string
	}{
		{
			name:             "passthrough with user key",
			configuredAPIKey: config.APIKeyPassthroughPlaceholder,
			userAPIKey:       "sk-ant-real-client-key-12345",
			wantAPIKey:       "sk-ant-real-client-key-12345",
		},
		{
			name:             "passthrough without user key",
			configuredAPIKey: config.APIKeyPassthroughPlaceholder,
			userAPIKey:       "",
			wantAPIKey:       "",
		},
		{
			name:             "normal API key (not passthrough)",
			configuredAPIKey: "sk-ant-configured-key-67890",
			userAPIKey:       "sk-ant-real-client-key-12345",
			wantAPIKey:       "sk-ant-configured-key-67890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a Gin context with a user API key
			ginCtx, _ := gin.CreateTestContext(nil)
			if tt.userAPIKey != "" {
				ginCtx.Set("userApiKey", tt.userAPIKey)
			}

			// Create a context with the Gin context embedded
			ctx := context.WithValue(context.Background(), "gin", ginCtx)

			// Test the passthrough resolution
			resolvedKey := ResolveAPIKeyWithPassthrough(ctx, tt.configuredAPIKey)

			if resolvedKey != tt.wantAPIKey {
				t.Errorf("ResolveAPIKeyWithPassthrough() = %v, want %v", resolvedKey, tt.wantAPIKey)
			}
		})
	}
}
