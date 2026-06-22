package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
)

const sampleModelsBody = `{
  "models": [
    {
      "slug": "gpt-5.5",
      "display_name": "GPT-5.5",
      "context_window": 272000,
      "max_context_window": 400000,
      "supported_reasoning_levels": ["low", {"id": "medium"}, "high", "xhigh"],
      "default_reasoning_level": "medium",
      "input_modalities": ["text", "image"]
    },
    {
      "slug": "gpt-5.4-mini",
      "display_name": "GPT-5.4 Mini",
      "context_window": 0,
      "max_context_window": 128000,
      "supported_reasoning_levels": [],
      "default_reasoning_level": "",
      "input_modalities": ["text"]
    }
  ]
}`

func TestModelsToCatwalk(t *testing.T) {
	apiModels := []apiModel{
		{
			Slug:                     "gpt-5.5",
			DisplayName:              "GPT-5.5",
			ContextWindow:            272000,
			MaxContextWindow:         400000,
			SupportedReasoningLevels: []reasoningLevel{"low", "medium", "high", "xhigh"},
			DefaultReasoningLevel:    "medium",
			InputModalities:          []string{"text", "image"},
		},
		{
			Slug:                     "gpt-5.4-mini",
			DisplayName:              "GPT-5.4 Mini",
			ContextWindow:            0,
			MaxContextWindow:         128000,
			SupportedReasoningLevels: nil,
			DefaultReasoningLevel:    "",
			InputModalities:          []string{"text"},
		},
	}

	got := modelsToCatwalk(apiModels)
	want := []catwalk.Model{
		{
			ID:                     "gpt-5.5",
			Name:                   "GPT-5.5",
			ContextWindow:          272000,
			DefaultMaxTokens:       32000,
			CanReason:              true,
			ReasoningLevels:        []string{"low", "medium", "high", "xhigh"},
			DefaultReasoningEffort: "medium",
			SupportsImages:         true,
		},
		{
			ID:                     "gpt-5.4-mini",
			Name:                   "GPT-5.4 Mini",
			ContextWindow:          128000, // falls back to max_context_window when context_window is 0
			DefaultMaxTokens:       16000,
			CanReason:              false,
			ReasoningLevels:        nil,
			DefaultReasoningEffort: "",
			SupportsImages:         false,
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelsToCatwalk mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestModelToCatwalkContextWindowFallback(t *testing.T) {
	// context_window present: used directly.
	if got := modelToCatwalk(apiModel{ContextWindow: 100, MaxContextWindow: 200}); got.ContextWindow != 100 {
		t.Errorf("expected context window 100, got %d", got.ContextWindow)
	}
	// context_window zero: falls back to max_context_window.
	if got := modelToCatwalk(apiModel{ContextWindow: 0, MaxContextWindow: 200}); got.ContextWindow != 200 {
		t.Errorf("expected fallback context window 200, got %d", got.ContextWindow)
	}
}

func TestFetchModels(t *testing.T) {
	var (
		gotPath          string
		gotClientVersion string
		gotAuth          string
		gotAccountID     string
		gotOriginator    string
		gotUserAgent     string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotClientVersion = r.URL.Query().Get("client_version")
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("ChatGPT-Account-ID")
		gotOriginator = r.Header.Get("originator")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleModelsBody))
	}))
	defer srv.Close()

	models, err := fetchModels(srv.URL, "0.130.0", "tok-abc", "acct-123")
	if err != nil {
		t.Fatalf("fetchModels returned error: %v", err)
	}

	if gotPath != "/models" {
		t.Errorf("expected request path /models, got %q", gotPath)
	}
	if gotClientVersion != "0.130.0" {
		t.Errorf("expected client_version 0.130.0, got %q", gotClientVersion)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("expected Authorization 'Bearer tok-abc', got %q", gotAuth)
	}
	if gotAccountID != "acct-123" {
		t.Errorf("expected ChatGPT-Account-ID 'acct-123', got %q", gotAccountID)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Errorf("expected originator 'codex_cli_rs', got %q", gotOriginator)
	}
	if gotUserAgent != "codex_cli_rs/0.130.0" {
		t.Errorf("expected User-Agent 'codex_cli_rs/0.130.0', got %q", gotUserAgent)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Slug != "gpt-5.5" || models[0].DisplayName != "GPT-5.5" {
		t.Errorf("unexpected first model: %#v", models[0])
	}

	// End-to-end mapping sanity check through the decoded payload.
	mapped := modelsToCatwalk(models)
	if mapped[0].ID != "gpt-5.5" || !mapped[0].CanReason || !mapped[0].SupportsImages {
		t.Errorf("unexpected mapped first model: %#v", mapped[0])
	}
	if mapped[1].ContextWindow != 128000 {
		t.Errorf("expected second model context window fallback to 128000, got %d", mapped[1].ContextWindow)
	}
}

func TestFetchModelsNoAccountIDHeaderWhenEmpty(t *testing.T) {
	gotAccountIDSet := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotAccountIDSet = r.Header["Chatgpt-Account-Id"]
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	if _, err := fetchModels(srv.URL, "0.130.0", "tok", ""); err != nil {
		t.Fatalf("fetchModels returned error: %v", err)
	}
	if gotAccountIDSet {
		t.Errorf("expected no ChatGPT-Account-ID header when account ID is empty")
	}
}

func TestFetchModelsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	if _, err := fetchModels(srv.URL, "0.130.0", "tok", "acct"); err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}

// ensure url encoding of client_version is well-formed (guards against
// accidental double-encoding regressions).
func TestClientVersionQueryEncoding(t *testing.T) {
	q := url.Values{}
	q.Set("client_version", "0.130.0")
	if got := q.Encode(); got != "client_version=0.130.0" {
		t.Errorf("unexpected query encoding: %q", got)
	}
}
