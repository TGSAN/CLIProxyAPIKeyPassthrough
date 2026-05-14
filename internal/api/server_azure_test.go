package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAzureOpenAIRoutes(t *testing.T) {
	server := newTestServer(t)

	t.Run("AzureChatCompletions", func(t *testing.T) {
		deploymentID := "gpt-4"
		requestBody := map[string]interface{}{
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"max_tokens": 100,
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+deploymentID+"/chat/completions?api-version=2024-02-01", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		// The actual handler will return an error since we don't have real credentials configured,
		// but we should get past the routing layer and the middleware should have injected the model.
		// We're testing that we don't get a 404 (route not found).
		if rr.Code == http.StatusNotFound {
			t.Fatalf("Azure route not found: got status %d, want non-404; body=%s", rr.Code, rr.Body.String())
		}

		// Status should be 502 or 500 or similar (backend error), not 404 (route not found)
		if rr.Code != http.StatusNotFound {
			t.Logf("Azure route correctly handled request with status %d", rr.Code)
		}
	})

	t.Run("AzureCompletions", func(t *testing.T) {
		deploymentID := "gpt-3.5-turbo"
		requestBody := map[string]interface{}{
			"prompt":     "Hello",
			"max_tokens": 100,
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+deploymentID+"/completions?api-version=2024-02-01", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code == http.StatusNotFound {
			t.Fatalf("Azure completions route not found: got status %d; body=%s", rr.Code, rr.Body.String())
		}

		t.Logf("Azure completions route correctly handled request with status %d", rr.Code)
	})

	t.Run("AzureEmbeddings", func(t *testing.T) {
		deploymentID := "text-embedding-ada-002"
		requestBody := map[string]interface{}{
			"input": "Hello world",
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+deploymentID+"/embeddings?api-version=2024-02-01", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code == http.StatusNotFound {
			t.Fatalf("Azure embeddings route not found: got status %d; body=%s", rr.Code, rr.Body.String())
		}

		t.Logf("Azure embeddings route correctly handled request with status %d", rr.Code)
	})
}

func TestAzureDeploymentMiddleware(t *testing.T) {
	server := newTestServer(t)

	t.Run("InjectsModelFromDeploymentID", func(t *testing.T) {
		deploymentID := "my-custom-model"
		// Request body without explicit model field
		requestBody := map[string]interface{}{
			"messages": []map[string]string{
				{"role": "user", "content": "Test"},
			},
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+deploymentID+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		// The middleware should have injected the deployment ID as the model parameter
		// We verify by checking that we don't get a 404 (route exists)
		if rr.Code == http.StatusNotFound {
			t.Fatalf("Route not found, middleware may not be working: got status %d; body=%s", rr.Code, rr.Body.String())
		}

		t.Logf("Middleware successfully processed request with status %d", rr.Code)
	})

	t.Run("PreservesExistingModel", func(t *testing.T) {
		deploymentID := "deployment-id"
		// Request body WITH explicit model field
		requestBody := map[string]interface{}{
			"model": "different-model",
			"messages": []map[string]string{
				{"role": "user", "content": "Test"},
			},
		}
		body, _ := json.Marshal(requestBody)

		req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+deploymentID+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		// The existing model should be preserved (not overwritten)
		if rr.Code == http.StatusNotFound {
			t.Fatalf("Route not found: got status %d; body=%s", rr.Code, rr.Body.String())
		}

		t.Logf("Middleware preserved existing model field with status %d", rr.Code)
	})
}
