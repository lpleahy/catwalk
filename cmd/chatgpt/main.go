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
	"encoding/base64"
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
	Slug                     string           `json:"slug"`
	DisplayName              string           `json:"display_name"`
	ContextWindow            int64            `json:"context_window"`
	MaxContextWindow         int64            `json:"max_context_window"`
	SupportedReasoningLevels []reasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string           `json:"default_reasoning_level"`
	InputModalities          []string         `json:"input_modalities"`
}

type reasoningLevel string

func (r *reasoningLevel) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*r = reasoningLevel(value)
		return nil
	}

	var object struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		Name   string `json:"name"`
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*r = reasoningLevel(cmpOr(object.ID, object.Slug, object.Name, object.Effort))
	return nil
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

type crushConfig struct {
	Providers map[string]struct {
		APIKey string `json:"api_key"`
		OAuth  struct {
			AccessToken string `json:"access_token"`
		} `json:"oauth"`
	} `json:"providers"`
}

type chatGPTAccessClaims struct {
	Auth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func main() {
	var (
		token             = flag.String("token", "", "ChatGPT access token (overrides env and auth.json)")
		apiEndpoint       = flag.String("api-endpoint", defaultAPIEndpoint, "ChatGPT backend API endpoint")
		clientVersion     = flag.String("client-version", defaultClientVersion, "Codex client version")
		defaultLargeModel = flag.String("default-large-model", defaultLargeModelID, "Default large ChatGPT model ID")
		defaultSmallModel = flag.String("default-small-model", defaultSmallModelID, "Default small ChatGPT model ID")
	)
	flag.Parse()

	if err := run(*token, *apiEndpoint, *clientVersion, *defaultLargeModel, *defaultSmallModel); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(token, apiEndpoint, clientVersion, defaultLargeModel, defaultSmallModel string) error {
	accessToken, accountID, err := resolveCredentials(token)
	if err != nil {
		return err
	}

	apiModels, err := fetchModels(apiEndpoint, clientVersion, accessToken, accountID)
	if err != nil {
		return err
	}

	models := modelsToCatwalk(apiModels)
	if !hasModel(models, defaultLargeModel) {
		return fmt.Errorf("default large model %q was not returned by the models endpoint", defaultLargeModel)
	}
	if !hasModel(models, defaultSmallModel) {
		return fmt.Errorf("default small model %q was not returned by the models endpoint", defaultSmallModel)
	}

	provider := catwalk.Provider{
		Name:                "ChatGPT",
		ID:                  catwalk.InferenceProviderChatGPT,
		Type:                catwalk.TypeOpenAICompat,
		APIEndpoint:         apiEndpoint,
		DefaultLargeModelID: defaultLargeModel,
		DefaultSmallModelID: defaultSmallModel,
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
// Crush's persisted OAuth config, or the Codex auth file (in that order).
func resolveCredentials(flagToken string) (token, accountID string, err error) {
	if flagToken != "" {
		return flagToken, accountIDFromToken(flagToken), nil
	}

	if envToken := os.Getenv("CHATGPT_ACCESS_TOKEN"); envToken != "" {
		return envToken, accountIDFromToken(envToken), nil
	}

	if token, accountID := readCrushChatGPTToken(); token != "" {
		return token, accountID, nil
	}

	auth, err := readCodexAuth()
	if err != nil {
		return "", "", fmt.Errorf("no ChatGPT access token available: pass --token, set CHATGPT_ACCESS_TOKEN, log in with crush, or log in with codex (%w)", err)
	}
	if auth.Tokens.AccessToken == "" {
		return "", "", fmt.Errorf("no ChatGPT access token available: pass --token, set CHATGPT_ACCESS_TOKEN, log in with crush, or log in with codex (auth file at %s has no tokens.access_token)", codexAuthPath())
	}
	return auth.Tokens.AccessToken, cmpOr(auth.Tokens.AccountID, accountIDFromToken(auth.Tokens.AccessToken)), nil
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

func readCrushChatGPTToken() (token, accountID string) {
	for _, path := range crushConfigPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg crushConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		provider, ok := cfg.Providers["chatgpt"]
		if !ok {
			continue
		}

		token = cmpOr(provider.OAuth.AccessToken, provider.APIKey)
		if token != "" {
			return token, accountIDFromToken(token)
		}
	}
	return "", ""
}

func crushConfigPaths() []string {
	var paths []string
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		paths = append(paths, filepath.Join(xdgData, "crush", "crush.json"))
	}
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		paths = append(paths, filepath.Join(xdgConfig, "crush", "crush.json"))
	}
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths,
			filepath.Join(home, ".local", "share", "crush", "crush.json"),
			filepath.Join(home, ".config", "crush", "crush.json"),
		)
	}
	return paths
}

func accountIDFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims chatGPTAccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}

func cmpOr(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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

func hasModel(models []catwalk.Model, modelID string) bool {
	return slices.ContainsFunc(models, func(model catwalk.Model) bool {
		return model.ID == modelID
	})
}

func modelToCatwalk(m apiModel) catwalk.Model {
	contextWindow := m.ContextWindow
	if contextWindow == 0 {
		contextWindow = m.MaxContextWindow
	}

	var reasoningLevels []string
	for _, level := range m.SupportedReasoningLevels {
		if level != "" {
			reasoningLevels = append(reasoningLevels, string(level))
		}
	}

	return catwalk.Model{
		ID:                     m.Slug,
		Name:                   m.DisplayName,
		ContextWindow:          contextWindow,
		DefaultMaxTokens:       defaultMaxTokens(contextWindow),
		CanReason:              len(reasoningLevels) > 0,
		ReasoningLevels:        reasoningLevels,
		DefaultReasoningEffort: m.DefaultReasoningLevel,
		SupportsImages:         slices.Contains(m.InputModalities, "image"),
	}
}

func defaultMaxTokens(contextWindow int64) int64 {
	if contextWindow >= 272000 {
		return 32000
	}
	if contextWindow > 0 {
		return 16000
	}
	return 0
}
