package executor

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestNormalizeCodexParallelToolCallsForTools_DropsWhenToolsMissing(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","parallel_tool_calls":true,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools are missing: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCallsForTools_DropsWhenToolsEmpty(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[],"parallel_tool_calls":false,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools are empty: %s", string(out))
	}
	if !gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("tools should be preserved: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCallsForTools_PreservesWhenToolsPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":true,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should be preserved when tools are present: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCalls_APIKeyDefaultsFalseWhenClientOmits(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}
	client := []byte(`{"model":"gpt-5.4","input":"hi"}`)
	// Translators force true before normalization; API-key auth must override it.
	for _, body := range []string{
		`{"model":"gpt-5.4","input":"hi"}`,
		`{"model":"gpt-5.4","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":true,"input":"hi"}`,
	} {
		out := normalizeCodexParallelToolCalls([]byte(body), nil, auth, client)
		parallelToolCalls := gjson.GetBytes(out, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
			t.Fatalf("parallel_tool_calls should be pinned to false for API-key auth: %s", string(out))
		}
	}
}

func TestNormalizeCodexParallelToolCalls_APIKeyUsesClientValue(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}
	client := []byte(`{"model":"gpt-5.4","input":"hi","parallel_tool_calls":true}`)
	body := []byte(`{"model":"gpt-5.4","parallel_tool_calls":false,"input":"hi"}`)

	out := normalizeCodexParallelToolCalls(body, nil, auth, client)

	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() {
		t.Fatalf("client parallel_tool_calls=true should win: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCalls_NonAPIKeyOmitsWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"lookup"}],"input":"hi"}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"access_token": "oauth-token"}}

	out := normalizeCodexParallelToolCalls(body, nil, auth, body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should remain absent for OAuth auth: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCalls_ResponsesLiteMetadataForcesFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hi"}`)

	out := normalizeCodexParallelToolCalls(body, nil, nil, nil)

	parallelToolCalls := gjson.GetBytes(out, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCalls_ResponsesLiteHeaderForcesFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","parallel_tool_calls":true,"input":"hi"}`)
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	out := normalizeCodexParallelToolCalls(body, headers, nil, nil)

	parallelToolCalls := gjson.GetBytes(out, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", string(out))
	}
}
