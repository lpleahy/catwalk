// Package main implements a build-time tool to fetch ChatGPT (Codex) models
// and generate the Catwalk provider configuration.
//
// It is intended to be run by a maintainer with a live ChatGPT/Codex login.
// The token is sourced (in order) from the --token flag, the
// CHATGPT_ACCESS_TOKEN environment variable, or the Codex auth file at
// $CODEX_HOME/auth.json (defaulting to ~/.codex/auth.json).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
)

const (
	defaultAPIEndpoint   = "https://chatgpt.com/backend-api/codex"
	defaultClientVersion = "0.130.0"

	// Curated provider-level fields. The /models response has no pricing or
	// default-model information, so these stay hand-maintained here.
	defaultLargeModelID = "gpt-5.5"
	defaultSmallModelID = "gpt-5.4-mini"

	configPath = "internal/providers/configs/chatgpt.json"
)

// apiModel mirrors a single entry of the ChatGPT /models response.
type apiModel struct {
	Slug                     string   `json:"slug"`
	DisplayName              string   `json:"display_name"`
	ContextWindow            int64    `json:"context_window"`
	MaxContextWindow         int64    `json:"max_context_window"`
	SupportedReasoningLevels []string `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string   `json:"default_reasoning_level"`
	InputModalities          []string `json:"input_modalities"`
}

// modelsResponse is the top-level shape of the /models response.
type modelsResponse struct {
	Models []apiModel `json:"models"`
}

// codexAuth mirrors the relevant fields of $CODEX_HOME/auth.json.
type codexAuth struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func main() {
	var (
		token         = flag.String("token", "", "ChatGPT access token (overrides env and auth.json)")
		apiEndpoint   = flag.String("api-endpoint", defaultAPIEndpoint, "ChatGPT backend API endpoint")
		clientVersion = flag.String("client-version", defaultClientVersion, "Codex client version")
	)
	flag.Parse()

	if err := run(*token, *apiEndpoint, *clientVersion); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(token, apiEndpoint, clientVersion string) error {
	accessToken, accountID, err := resolveCredentials(token)
	if err != nil {
		return err
	}

	apiModels, err := fetchModels(apiEndpoint, clientVersion, accessToken, accountID)
	if err != nil {
		return err
	}

	models := modelsToCatwalk(apiModels)

	provider := catwalk.Provider{
		Name:                "ChatGPT",
		ID:                  catwalk.InferenceProviderChatGPT,
		Type:                catwalk.TypeOpenAICompat,
		APIEndpoint:         apiEndpoint,
		DefaultLargeModelID: defaultLargeModelID,
		DefaultSmallModelID: defaultSmallModelID,
		Models:              models,
	}

	data, err := json.MarshalIndent(provider, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to marshal json: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("unable to write %s: %w", configPath, err)
	}

	fmt.Printf("Generated %s with %d models\n", configPath, len(models))
	return nil
}

// resolveCredentials returns the access token and account ID, sourcing them
// from the provided flag value, the CHATGPT_ACCESS_TOKEN environment variable,
// or the Codex auth file (in that order).
func resolveCredentials(flagToken string) (token, accountID string, err error) {
	if flagToken != "" {
		// A flag-provided token still benefits from an account ID if one is
		// available on disk, but the token itself wins.
		auth, _ := readCodexAuth()
		return flagToken, auth.Tokens.AccountID, nil
	}

	if envToken := os.Getenv("CHATGPT_ACCESS_TOKEN"); envToken != "" {
		auth, _ := readCodexAuth()
		return envToken, auth.Tokens.AccountID, nil
	}

	auth, err := readCodexAuth()
	if err != nil {
		return "", "", fmt.Errorf("no ChatGPT access token available: pass --token, set CHATGPT_ACCESS_TOKEN, or log in with codex (%w)", err)
	}
	if auth.Tokens.AccessToken == "" {
		return "", "", fmt.Errorf("no ChatGPT access token available: pass --token, set CHATGPT_ACCESS_TOKEN, or log in with codex (auth file at %s has no tokens.access_token)", codexAuthPath())
	}
	return auth.Tokens.AccessToken, auth.Tokens.AccountID, nil
}

// codexAuthPath returns the path to the Codex auth file, respecting CODEX_HOME
// and falling back to ~/.codex.
func codexAuthPath() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	return filepath.Join(home, "auth.json")
}

func readCodexAuth() (codexAuth, error) {
	var auth codexAuth
	data, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return auth, fmt.Errorf("unable to read %s: %w", codexAuthPath(), err)
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return auth, fmt.Errorf("unable to parse %s: %w", codexAuthPath(), err)
	}
	return auth, nil
}

// fetchModels requests the /models endpoint and returns the decoded models.
func fetchModels(apiEndpoint, clientVersion, accessToken, accountID string) ([]apiModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(apiEndpoint, "/") + "/models"
	q := url.Values{}
	q.Set("client_version", clientVersion)
	endpoint += "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/"+clientVersion)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to make models request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read models response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from models endpoint: %d: %s", resp.StatusCode, body)
	}

	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("unable to unmarshal json: %w", err)
	}
	return mr.Models, nil
}

// modelsToCatwalk maps the ChatGPT /models entries to catwalk.Model values.
// Pricing, DefaultMaxTokens, and provider-level defaults are curated elsewhere
// and are intentionally left at their zero values here.
func modelsToCatwalk(apiModels []apiModel) []catwalk.Model {
	models := make([]catwalk.Model, 0, len(apiModels))
	for _, m := range apiModels {
		models = append(models, modelToCatwalk(m))
	}
	return models
}

func modelToCatwalk(m apiModel) catwalk.Model {
	contextWindow := m.ContextWindow
	if contextWindow == 0 {
		contextWindow = m.MaxContextWindow
	}

	return catwalk.Model{
		ID:                     m.Slug,
		Name:                   m.DisplayName,
		ContextWindow:          contextWindow,
		CanReason:              len(m.SupportedReasoningLevels) > 0,
		ReasoningLevels:        m.SupportedReasoningLevels,
		DefaultReasoningEffort: m.DefaultReasoningLevel,
		SupportsImages:         slices.Contains(m.InputModalities, "image"),
	}
}
