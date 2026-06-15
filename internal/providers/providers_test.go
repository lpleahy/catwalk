package providers

import (
	"slices"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
)

func TestValidDefaultModels(t *testing.T) {
	for _, p := range GetAll() {
		t.Run(p.Name, func(t *testing.T) {
			var modelIds []string
			for _, m := range p.Models {
				modelIds = append(modelIds, m.ID)
			}
			if !slices.Contains(modelIds, p.DefaultLargeModelID) {
				t.Errorf("Default large model %q not found in provider %q", p.DefaultLargeModelID, p.Name)
			}
			if !slices.Contains(modelIds, p.DefaultSmallModelID) {
				t.Errorf("Default small model %q not found in provider %q", p.DefaultSmallModelID, p.Name)
			}
		})
	}
}

// TestChatGPTProviderConfig verifies the ChatGPT provider loads from its
// embedded config without error and exposes the expected identity and defaults.
func TestChatGPTProviderConfig(t *testing.T) {
	p := chatGPTProvider()

	if p.ID != catwalk.InferenceProviderChatGPT {
		t.Errorf("ID = %q, want %q", p.ID, catwalk.InferenceProviderChatGPT)
	}
	if p.ID != "chatgpt" {
		t.Errorf("ID = %q, want %q", p.ID, "chatgpt")
	}
	if p.Name != "ChatGPT" {
		t.Errorf("Name = %q, want %q", p.Name, "ChatGPT")
	}
	if p.Type != catwalk.TypeOpenAICompat {
		t.Errorf("Type = %q, want %q", p.Type, catwalk.TypeOpenAICompat)
	}
	if p.APIEndpoint != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("APIEndpoint = %q, want %q", p.APIEndpoint, "https://chatgpt.com/backend-api/codex")
	}
	if p.DefaultLargeModelID != "gpt-5.5" {
		t.Errorf("DefaultLargeModelID = %q, want %q", p.DefaultLargeModelID, "gpt-5.5")
	}
	if p.DefaultSmallModelID != "gpt-5.4-mini" {
		t.Errorf("DefaultSmallModelID = %q, want %q", p.DefaultSmallModelID, "gpt-5.4-mini")
	}
	if len(p.Models) == 0 {
		t.Fatal("Models is empty, want at least one model")
	}

	// The default model IDs must resolve to entries in the models list.
	var modelIDs []string
	for _, m := range p.Models {
		modelIDs = append(modelIDs, m.ID)
	}
	if !slices.Contains(modelIDs, p.DefaultLargeModelID) {
		t.Errorf("DefaultLargeModelID %q not found in models %v", p.DefaultLargeModelID, modelIDs)
	}
	if !slices.Contains(modelIDs, p.DefaultSmallModelID) {
		t.Errorf("DefaultSmallModelID %q not found in models %v", p.DefaultSmallModelID, modelIDs)
	}
}

// TestChatGPTProviderRegistered verifies the ChatGPT provider is wired into the
// registry returned by GetAll.
func TestChatGPTProviderRegistered(t *testing.T) {
	found := false
	for _, p := range GetAll() {
		if p.ID == catwalk.InferenceProviderChatGPT {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ChatGPT provider %q not present in GetAll()", catwalk.InferenceProviderChatGPT)
	}
}
