package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestAzureAPIKeyPassthrough verifies that Azure endpoints properly pass through
// the client's API key to the upstream server when using API_KEY_PASSTHROUGH.
func TestAzureAPIKeyPassthrough(t *testing.T) {
	// Create a fake upstream server that captures the Authorization header
	var capturedAuthHeader string
	var capturedRequestBody string
	var capturedRequestPath string
	var upstreamHitCount atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHitCount.Add(1)
		capturedAuthHeader = r.Header.Get("Authorization")
		capturedRequestPath = r.URL.Path
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedRequestBody = string(bodyBytes)

		// Return a fake successful response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := map[string]interface{}{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Hello!",
					},
					"finish_reason": "stop",
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer upstreamServer.Close()

	clientAPIKey := "client-secret-key-12345"
	clientAPIKey2 := "client-secret-key-67890"

	// Configure the proxy with API_KEY_PASSTHROUGH
	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{clientAPIKey, clientAPIKey2}, // Allow both client API keys
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "azure-test",
				BaseURL: upstreamServer.URL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{
						APIKey: config.APIKeyPassthroughPlaceholder, // Use passthrough
					},
				},
				Models: []config.OpenAICompatibilityModel{
					{
						Name:  "gpt-4",      // The actual model name sent to upstream
						Alias: "azure-test", // The model alias clients use to reference this
					},
				},
			},
		},
	}

	server := newTestServerWithConfig(t, cfg)

	// Register the OpenAI compat executor for the "azure-test" provider
	azureExecutor := executor.NewOpenAICompatExecutor("azure-test", cfg)
	server.handlers.AuthManager.RegisterExecutor(azureExecutor)

	// Register the model in the global registry so it can be found.
	// The client ID must match the auth ID because the scheduler uses auth ID
	// to look up supported models via registry.GetModelsForClient(authID).
	registry.GetGlobalRegistry().RegisterClient(
		"test-azure-compat-auth",
		"azure-test", // Provider name (matches the OpenAI compat name)
		[]*registry.ModelInfo{
			{
				ID:      "azure-test",
				Object:  "model",
				Created: 1234567890,
				OwnedBy: "test",
				Type:    "openai",
			},
		},
	)

	// Manually create Auth entry for the OpenAI compat provider
	// In production, this would be done by the synthesizer/watcher
	authEntry := &auth.Auth{
		ID:       "test-azure-compat-auth",
		Provider: "azure-test",
		Label:    "Azure Test",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"base_url":     upstreamServer.URL,
			"api_key":      config.APIKeyPassthroughPlaceholder,
			"compat_name":  "azure-test",
			"provider_key": "azure-test",
		},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), authEntry); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	t.Run("AzureDeploymentEndpoint_PassthroughAPIKey", func(t *testing.T) {
		capturedAuthHeader = ""
		capturedRequestBody = ""
		capturedRequestPath = ""
		upstreamHitCount.Store(0)

		deploymentID := "azure-test" // This should match the model/provider

		requestBody := map[string]interface{}{
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"max_tokens": 100,
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(
			http.MethodPost,
			"/openai/deployments/"+deploymentID+"/chat/completions?api-version=2024-02-01",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientAPIKey)

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		t.Logf("Response status: %d", rr.Code)
		t.Logf("Response body: %s", rr.Body.String())
		t.Logf("Captured upstream Authorization header: %s", capturedAuthHeader)
		t.Logf("Captured upstream request path: %s", capturedRequestPath)
		t.Logf("Captured upstream request body: %s", capturedRequestBody)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d; body=%s", http.StatusOK, rr.Code, rr.Body.String())
		}

		if upstreamHitCount.Load() != 1 {
			t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHitCount.Load())
		}

		// Verify the upstream received the client's API key
		expectedAuth := "Bearer " + clientAPIKey
		if !strings.Contains(capturedAuthHeader, clientAPIKey) {
			t.Errorf("API key passthrough FAILED for Azure deployment endpoint!\nExpected Authorization: %s\nGot Authorization: %s",
				expectedAuth, capturedAuthHeader)
		}

		if capturedAuthHeader == "Bearer " || capturedAuthHeader == "Bearer" {
			t.Errorf("CRITICAL BUG: Authorization header is 'Bearer' with NO KEY!\nClient sent: %s\nUpstream received: %s",
				expectedAuth, capturedAuthHeader)
		}

		if capturedRequestPath != "/chat/completions" {
			t.Errorf("expected upstream path %q, got %q", "/chat/completions", capturedRequestPath)
		}

		if !strings.Contains(capturedRequestBody, `"messages":[`) {
			t.Errorf("expected upstream request body to preserve messages, got: %s", capturedRequestBody)
		}
	})

	t.Run("AzureDeploymentResponsesEndpoint_PassthroughAPIKey", func(t *testing.T) {
		capturedAuthHeader = ""
		capturedRequestBody = ""
		capturedRequestPath = ""
		upstreamHitCount.Store(0)

		deploymentID := "azure-test"
		requestBody := map[string]interface{}{
			"input": "Hello from responses",
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(
			http.MethodPost,
			"/openai/deployments/"+deploymentID+"/responses?api-version=2025-04-01-preview",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientAPIKey)

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		t.Logf("Response status: %d", rr.Code)
		t.Logf("Response body: %s", rr.Body.String())
		t.Logf("Captured upstream Authorization header: %s", capturedAuthHeader)
		t.Logf("Captured upstream request path: %s", capturedRequestPath)
		t.Logf("Captured upstream request body: %s", capturedRequestBody)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d; body=%s", http.StatusOK, rr.Code, rr.Body.String())
		}

		if upstreamHitCount.Load() != 1 {
			t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHitCount.Load())
		}

		expectedAuth := "Bearer " + clientAPIKey
		if !strings.Contains(capturedAuthHeader, clientAPIKey) {
			t.Errorf("API key passthrough FAILED for Azure deployment responses endpoint!\nExpected Authorization: %s\nGot Authorization: %s",
				expectedAuth, capturedAuthHeader)
		}

		if !strings.Contains(capturedRequestBody, `"model":"`+deploymentID+`"`) {
			t.Errorf("deployment model injection FAILED for Azure deployment responses endpoint!\nExpected request body to include model %q\nGot body: %s",
				deploymentID, capturedRequestBody)
		}

		if capturedRequestPath != "/chat/completions" {
			t.Errorf("expected upstream path %q, got %q", "/chat/completions", capturedRequestPath)
		}

		if !strings.Contains(capturedRequestBody, `"messages":[`) {
			t.Errorf("expected upstream request body to preserve translated messages payload, got: %s", capturedRequestBody)
		}

		if !strings.Contains(capturedRequestBody, `"Hello from responses"`) {
			t.Errorf("expected upstream request body to preserve input content, got: %s", capturedRequestBody)
		}
	})

	t.Run("AzureV1Endpoint_PassthroughAPIKey", func(t *testing.T) {
		capturedAuthHeader = ""
		capturedRequestBody = ""
		capturedRequestPath = ""
		upstreamHitCount.Store(0)

		clientAPIKey := "client-secret-key-67890"

		requestBody := map[string]interface{}{
			"model": "azure-test",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"max_tokens": 100,
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(
			http.MethodPost,
			"/openai/v1/chat/completions",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientAPIKey)

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		t.Logf("Response status: %d", rr.Code)
		t.Logf("Response body: %s", rr.Body.String())
		t.Logf("Captured upstream Authorization header: %s", capturedAuthHeader)
		t.Logf("Captured upstream request path: %s", capturedRequestPath)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d; body=%s", http.StatusOK, rr.Code, rr.Body.String())
		}

		if upstreamHitCount.Load() != 1 {
			t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHitCount.Load())
		}

		// Verify the upstream received the client's API key
		expectedAuth := "Bearer " + clientAPIKey
		if !strings.Contains(capturedAuthHeader, clientAPIKey) {
			t.Errorf("API key passthrough FAILED for Azure /openai/v1 endpoint!\nExpected Authorization: %s\nGot Authorization: %s",
				expectedAuth, capturedAuthHeader)
		}

		if capturedAuthHeader == "Bearer " || capturedAuthHeader == "Bearer" {
			t.Errorf("CRITICAL BUG: Authorization header is 'Bearer' with NO KEY!\nClient sent: %s\nUpstream received: %s",
				expectedAuth, capturedAuthHeader)
		}

		if capturedRequestPath != "/chat/completions" {
			t.Errorf("expected upstream path %q, got %q", "/chat/completions", capturedRequestPath)
		}
	})

	t.Run("AnthropicDeploymentMessagesEndpoint_PassthroughAPIKey", func(t *testing.T) {
		capturedAuthHeader = ""
		capturedRequestBody = ""
		capturedRequestPath = ""
		upstreamHitCount.Store(0)

		deploymentID := "azure-test"
		requestBody := map[string]interface{}{
			"messages": []map[string]string{
				{"role": "user", "content": "Hello from anthropic deployment"},
			},
			"max_tokens": 100,
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(
			http.MethodPost,
			"/anthropic/deployments/"+deploymentID+"/messages?api-version=2025-04-01-preview",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientAPIKey2)

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		t.Logf("Response status: %d", rr.Code)
		t.Logf("Response body: %s", rr.Body.String())
		t.Logf("Captured upstream Authorization header: %s", capturedAuthHeader)
		t.Logf("Captured upstream request path: %s", capturedRequestPath)
		t.Logf("Captured upstream request body: %s", capturedRequestBody)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d; body=%s", http.StatusOK, rr.Code, rr.Body.String())
		}

		if upstreamHitCount.Load() != 1 {
			t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHitCount.Load())
		}

		expectedAuth := "Bearer " + clientAPIKey2
		if !strings.Contains(capturedAuthHeader, clientAPIKey2) {
			t.Errorf("API key passthrough FAILED for Anthropic deployment messages endpoint!\nExpected Authorization: %s\nGot Authorization: %s",
				expectedAuth, capturedAuthHeader)
		}

		if !strings.Contains(capturedRequestBody, `"model":"`+deploymentID+`"`) {
			t.Errorf("deployment model injection FAILED for Anthropic deployment messages endpoint!\nExpected request body to include model %q\nGot body: %s",
				deploymentID, capturedRequestBody)
		}

		if capturedRequestPath != "/chat/completions" {
			t.Errorf("expected upstream path %q, got %q", "/chat/completions", capturedRequestPath)
		}

		if !strings.Contains(capturedRequestBody, `"messages":[`) {
			t.Errorf("expected upstream request body to preserve messages, got: %s", capturedRequestBody)
		}

		if !strings.Contains(capturedRequestBody, `"max_tokens":100`) {
			t.Errorf("expected upstream request body to preserve max_tokens, got: %s", capturedRequestBody)
		}
	})

	t.Run("DeploymentPathOverridesConflictingBodyModel", func(t *testing.T) {
		capturedAuthHeader = ""
		capturedRequestBody = ""
		capturedRequestPath = ""
		upstreamHitCount.Store(0)

		deploymentID := "azure-test"
		requestBody := map[string]interface{}{
			"model": "different-model",
			"messages": []map[string]string{
				{"role": "user", "content": "Use deployment model"},
			},
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(
			http.MethodPost,
			"/openai/deployments/"+deploymentID+"/chat/completions?api-version=2025-04-01-preview",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientAPIKey)

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d; body=%s", http.StatusOK, rr.Code, rr.Body.String())
		}

		if upstreamHitCount.Load() != 1 {
			t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHitCount.Load())
		}

		if !strings.Contains(capturedRequestBody, `"model":"`+deploymentID+`"`) {
			t.Fatalf("expected deployment ID to override body model; got body: %s", capturedRequestBody)
		}

		if strings.Contains(capturedRequestBody, `"model":"different-model"`) {
			t.Fatalf("expected conflicting body model to be removed or overridden; got body: %s", capturedRequestBody)
		}
	})
}

// Helper function to create a test server with custom config
func newTestServerWithConfig(t *testing.T, cfg *config.Config) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	if cfg == nil {
		cfg = &config.Config{}
	}

	cfg.Port = 0
	cfg.AuthDir = authDir
	cfg.Debug = true
	cfg.LoggingToFile = false
	cfg.UsageStatisticsEnabled = false

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}
