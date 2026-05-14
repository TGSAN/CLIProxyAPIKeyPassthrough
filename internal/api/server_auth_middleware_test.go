package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkAccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestExtractAPIKeyFromRequest(t *testing.T) {
	tests := []struct {
		name     string
		setupReq func() *http.Request
		want     string
	}{
		{
			name: "Bearer token in Authorization header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer sk-ant-api-key-123")
				return req
			},
			want: "sk-ant-api-key-123",
		},
		{
			name: "API key in X-Api-Key header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-Api-Key", "sk-ant-api-key-456")
				return req
			},
			want: "sk-ant-api-key-456",
		},
		{
			name: "API key in X-Goog-Api-Key header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-Goog-Api-Key", "google-api-key-789")
				return req
			},
			want: "google-api-key-789",
		},
		{
			name: "API key in query parameter",
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/?key=query-api-key-101", nil)
			},
			want: "query-api-key-101",
		},
		{
			name: "No API key",
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			want: "",
		},
		{
			name: "Authorization header without Bearer prefix",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "sk-ant-direct-key-202")
				return req
			},
			want: "sk-ant-direct-key-202",
		},
		{
			name: "Bearer token with extra whitespace",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer  sk-ant-spaces-303  ")
				return req
			},
			want: "sk-ant-spaces-303",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupReq()
			got := extractAPIKeyFromRequest(req)
			if got != tt.want {
				t.Errorf("extractAPIKeyFromRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthMiddleware_SetsUserAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		setupReq       func() *http.Request
		manager        interface{} // nil or actual manager
		wantUserAPIKey string
		wantAborted    bool
	}{
		{
			name: "Sets userApiKey from Authorization header when manager is nil",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/v1/messages", nil)
				req.Header.Set("Authorization", "Bearer sk-ant-test-key-1")
				return req
			},
			manager:        nil,
			wantUserAPIKey: "sk-ant-test-key-1",
			wantAborted:    false,
		},
		{
			name: "Sets userApiKey from X-Api-Key header when manager is nil",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/v1/messages", nil)
				req.Header.Set("X-Api-Key", "sk-ant-test-key-2")
				return req
			},
			manager:        nil,
			wantUserAPIKey: "sk-ant-test-key-2",
			wantAborted:    false,
		},
		{
			name: "No userApiKey set when no API key provided",
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/v1/messages", nil)
			},
			manager:        nil,
			wantUserAPIKey: "",
			wantAborted:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = tt.setupReq()

			middleware := AuthMiddleware(nil)
			middleware(c)

			userAPIKey, exists := c.Get("userApiKey")
			if tt.wantUserAPIKey == "" {
				if exists {
					t.Errorf("Expected userApiKey to not be set, but got: %v", userAPIKey)
				}
			} else {
				if !exists {
					t.Errorf("Expected userApiKey to be set to %v, but it was not set", tt.wantUserAPIKey)
				} else if userAPIKey.(string) != tt.wantUserAPIKey {
					t.Errorf("userApiKey = %v, want %v", userAPIKey, tt.wantUserAPIKey)
				}
			}

			if tt.wantAborted != c.IsAborted() {
				t.Errorf("IsAborted() = %v, want %v", c.IsAborted(), tt.wantAborted)
			}
		})
	}
}

// TestAuthMiddleware_PreservesClientAPIKeyForPassthrough tests that when authentication
// succeeds, the middleware should NOT overwrite the client's original API key.
// This is critical for API key passthrough mode to work correctly.
func TestAuthMiddleware_PreservesClientAPIKeyForPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a real access manager with a mock provider
	manager := sdkAccess.NewManager()
	mockProvider := &mockProvider{
		authenticateFunc: func(ctx context.Context, req *http.Request) (*sdkAccess.Result, *sdkAccess.AuthError) {
			// Simulate successful authentication with a different principal
			return &sdkAccess.Result{
				Principal: "authenticated-principal-different-from-client-key",
				Provider:  "test-provider",
			}, nil
		},
	}
	manager.SetProviders([]sdkAccess.Provider{mockProvider})

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	clientAPIKey := "sk-client-original-key-12345"
	req.Header.Set("Authorization", "Bearer "+clientAPIKey)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	middleware := AuthMiddleware(manager)
	middleware(c)

	// The userApiKey should still be the client's original API key,
	// NOT the authentication principal
	userAPIKey, exists := c.Get("userApiKey")
	if !exists {
		t.Fatalf("Expected userApiKey to be set")
	}

	if userAPIKey.(string) != clientAPIKey {
		t.Errorf("userApiKey = %v, want %v (client's original key should be preserved for passthrough)",
			userAPIKey, clientAPIKey)
	}

	// Verify authentication succeeded (request not aborted)
	if c.IsAborted() {
		t.Errorf("Request should not be aborted when authentication succeeds")
	}
}

// Mock provider for testing
type mockProvider struct {
	authenticateFunc func(ctx context.Context, req *http.Request) (*sdkAccess.Result, *sdkAccess.AuthError)
}

func (m *mockProvider) Authenticate(ctx context.Context, req *http.Request) (*sdkAccess.Result, *sdkAccess.AuthError) {
	if m.authenticateFunc != nil {
		return m.authenticateFunc(ctx, req)
	}
	return nil, sdkAccess.NewInvalidCredentialError()
}

func (m *mockProvider) Identifier() string {
	return "mock-provider"
}
