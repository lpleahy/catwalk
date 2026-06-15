package catwalk

import (
	"slices"
	"testing"
)

// TestChatGPTInKnownProviders verifies the ChatGPT inference provider constant
// is advertised by KnownProviders.
func TestChatGPTInKnownProviders(t *testing.T) {
	if InferenceProviderChatGPT != "chatgpt" {
		t.Errorf("InferenceProviderChatGPT = %q, want %q", InferenceProviderChatGPT, "chatgpt")
	}
	if !slices.Contains(KnownProviders(), InferenceProviderChatGPT) {
		t.Errorf("KnownProviders() does not include %q", InferenceProviderChatGPT)
	}
}
