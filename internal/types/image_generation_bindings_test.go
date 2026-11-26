package types

import (
	"reflect"
	"testing"
)

func TestCompileInboundProtocolBindings_ImageGeneration(t *testing.T) {
	tests := []struct {
		name          string
		profile       string
		wantName      string
		wantPath      string
		wantAuth      string
		wantCompatAPI string
	}{
		{
			name:     "openai_compatible",
			profile:  ProviderProfileOpenAICompatible,
			wantName: "openai-images",
			wantPath: defaultOpenAICompatiblePathPrefix,
			wantAuth: "bearer",
		},
		{
			name:          "glm",
			profile:       ProviderProfileGLM,
			wantName:      "glm-images",
			wantPath:      defaultGLMPathPrefix,
			wantAuth:      "bearer",
			wantCompatAPI: "glm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings, defaultBinding, err := CompileInboundProtocolBindings(
				tt.profile,
				[]string{InboundProtocolOpenAI},
				[]string{"image_generation"},
				ClientProtocolBindingOptions{},
			)
			if err != nil {
				t.Fatalf("CompileInboundProtocolBindings: %v", err)
			}
			if len(bindings) != 1 {
				t.Fatalf("bindings=%+v want exactly one image binding", bindings)
			}

			binding := bindings[0]
			if binding.Name != tt.wantName {
				t.Fatalf("binding name=%q want %q", binding.Name, tt.wantName)
			}
			if defaultBinding != tt.wantName {
				t.Fatalf("default binding=%q want %q", defaultBinding, tt.wantName)
			}
			if binding.OutboundProtocol != "openai" {
				t.Fatalf("outbound protocol=%q want openai", binding.OutboundProtocol)
			}
			if binding.PathPrefix != tt.wantPath {
				t.Fatalf("path prefix=%q want %q", binding.PathPrefix, tt.wantPath)
			}
			if binding.AuthScheme != tt.wantAuth {
				t.Fatalf("auth scheme=%q want %q", binding.AuthScheme, tt.wantAuth)
			}
			if binding.TranslatorKey != "passthrough" {
				t.Fatalf("translator=%q want passthrough", binding.TranslatorKey)
			}
			if binding.Bridge != "" {
				t.Fatalf("bridge=%q want empty for same-wire passthrough", binding.Bridge)
			}
			if binding.CompatAPIType != tt.wantCompatAPI {
				t.Fatalf("compat api type=%q want %q", binding.CompatAPIType, tt.wantCompatAPI)
			}

			wantAccepts := []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"image_generation"},
				RequestContract: "image_generations",
			}}
			if !reflect.DeepEqual(binding.Accepts, wantAccepts) {
				t.Fatalf("accepts=%+v want %+v", binding.Accepts, wantAccepts)
			}

			t.Logf("binding=%+v", binding)
		})
	}
}
