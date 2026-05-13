package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestMarkResult_APIKeyPassthrough_AuthErrors verifies that when using API_KEY_PASSTHROUGH,
// all errors do not mark the auth as unavailable, since these errors are caused by the
// user's API key, not the server's credential.
func TestMarkResult_APIKeyPassthrough_AuthErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{
			name:       "401 unauthorized with passthrough should not mark unavailable",
			statusCode: http.StatusUnauthorized,
		},
		{
			name:       "402 payment required with passthrough should not mark unavailable",
			statusCode: http.StatusPaymentRequired,
		},
		{
			name:       "403 forbidden with passthrough should not mark unavailable",
			statusCode: http.StatusForbidden,
		},
		{
			name:       "404 not found with passthrough should not mark unavailable",
			statusCode: http.StatusNotFound,
		},
		{
			name:       "429 rate limit with passthrough should not mark unavailable",
			statusCode: http.StatusTooManyRequests,
		},
		{
			name:       "500 server error with passthrough should not mark unavailable",
			statusCode: http.StatusInternalServerError,
		},
		{
			name:       "502 bad gateway with passthrough should not mark unavailable",
			statusCode: http.StatusBadGateway,
		},
		{
			name:       "503 service unavailable with passthrough should not mark unavailable",
			statusCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			model := "test-model"

			// Create auth with API_KEY_PASSTHROUGH placeholder
			auth := &Auth{
				ID:       "passthrough-auth",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key": internalconfig.APIKeyPassthroughPlaceholder,
				},
				ModelStates: make(map[string]*ModelState),
			}

			manager := NewManager(nil, nil, nil)
			manager.auths[auth.ID] = auth

			// Record a failure with the given status code
			result := Result{
				AuthID:   auth.ID,
				Provider: "codex",
				Model:    model,
				Success:  false,
				Error: &Error{
					Code:       "test_error",
					Message:    "test error message",
					HTTPStatus: tt.statusCode,
				},
			}

			manager.MarkResult(ctx, result)

			// Verify the auth state - with passthrough, should NEVER mark unavailable
			state, ok := manager.auths[auth.ID].ModelStates[model]
			if !ok {
				t.Fatalf("expected model state for %s to exist", model)
			}

			if state.Unavailable {
				t.Errorf("expected state.Unavailable = false for status %d with passthrough, got true", tt.statusCode)
			}
			if !state.NextRetryAfter.IsZero() {
				t.Errorf("expected state.NextRetryAfter to be zero for status %d with passthrough, got %v", tt.statusCode, state.NextRetryAfter)
			}
		})
	}
}

// TestMarkResult_NonPassthrough_AuthErrors verifies that without API_KEY_PASSTHROUGH,
// authentication errors correctly mark the auth as unavailable (existing behavior).
func TestMarkResult_NonPassthrough_AuthErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	model := "test-model"

	// Create auth WITHOUT API_KEY_PASSTHROUGH
	auth := &Auth{
		ID:       "regular-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "sk-real-api-key-12345",
		},
		ModelStates: make(map[string]*ModelState),
	}

	manager := NewManager(nil, nil, nil)
	manager.auths[auth.ID] = auth

	// Record a 401 failure
	result := Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "unauthorized",
			Message:    "Invalid API key",
			HTTPStatus: http.StatusUnauthorized,
		},
	}

	manager.MarkResult(ctx, result)

	// Verify the auth IS marked unavailable (existing behavior should not change)
	state, ok := manager.auths[auth.ID].ModelStates[model]
	if !ok {
		t.Fatalf("expected model state for %s to exist", model)
	}

	if !state.Unavailable {
		t.Errorf("expected state.Unavailable = true for non-passthrough auth, got false")
	}
	if state.NextRetryAfter.IsZero() {
		t.Errorf("expected state.NextRetryAfter to be set for non-passthrough auth, got zero")
	}
}

// TestMarkResult_APIKeyPassthrough_Success verifies that successful requests
// properly clear error states even with API_KEY_PASSTHROUGH.
func TestMarkResult_APIKeyPassthrough_Success(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	model := "test-model"

	// Create auth with API_KEY_PASSTHROUGH and existing error state
	auth := &Auth{
		ID:       "passthrough-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": internalconfig.APIKeyPassthroughPlaceholder,
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    false, // Should remain false from previous passthrough 401
				StatusMessage:  "Previous error",
				NextRetryAfter: time.Time{},
			},
		},
	}

	manager := NewManager(nil, nil, nil)
	manager.auths[auth.ID] = auth

	// Record a successful request
	result := Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  true,
	}

	manager.MarkResult(ctx, result)

	// Verify the state is cleared
	state, ok := manager.auths[auth.ID].ModelStates[model]
	if !ok {
		t.Fatalf("expected model state for %s to exist", model)
	}

	if state.Unavailable {
		t.Errorf("expected state.Unavailable = false after success, got true")
	}
	if state.Status != StatusActive {
		t.Errorf("expected state.Status = StatusActive after success, got %v", state.Status)
	}
	if state.StatusMessage != "" {
		t.Errorf("expected state.StatusMessage to be cleared after success, got %q", state.StatusMessage)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Errorf("expected state.NextRetryAfter to be zero after success, got %v", state.NextRetryAfter)
	}
}

// TestAuthUsesAPIKeyPassthrough verifies the helper function correctly identifies passthrough auths.
func TestAuthUsesAPIKeyPassthrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		auth     *Auth
		expected bool
	}{
		{
			name:     "nil auth",
			auth:     nil,
			expected: false,
		},
		{
			name: "auth with passthrough placeholder",
			auth: &Auth{
				Attributes: map[string]string{
					"api_key": internalconfig.APIKeyPassthroughPlaceholder,
				},
			},
			expected: true,
		},
		{
			name: "auth with passthrough placeholder with spaces",
			auth: &Auth{
				Attributes: map[string]string{
					"api_key": "  " + internalconfig.APIKeyPassthroughPlaceholder + "  ",
				},
			},
			expected: true,
		},
		{
			name: "auth with regular api key",
			auth: &Auth{
				Attributes: map[string]string{
					"api_key": "sk-real-key-12345",
				},
			},
			expected: false,
		},
		{
			name: "auth without api_key attribute",
			auth: &Auth{
				Attributes: map[string]string{
					"other": "value",
				},
			},
			expected: false,
		},
		{
			name: "auth with nil attributes",
			auth: &Auth{
				Attributes: nil,
			},
			expected: false,
		},
		{
			name: "auth with empty api_key",
			auth: &Auth{
				Attributes: map[string]string{
					"api_key": "",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := authUsesAPIKeyPassthrough(tt.auth)
			if result != tt.expected {
				t.Errorf("authUsesAPIKeyPassthrough() = %v, want %v", result, tt.expected)
			}
		})
	}
}
