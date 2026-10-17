package gui

import "testing"

// TestFriendlyModelName: MyApps-AI's cases (model_naming_test.dart) give
// the same names here.
func TestFriendlyModelName(t *testing.T) {
	cases := []struct{ id, want string }{
		{"gpt-4o", "OpenAI: GPT-4o"},
		{"gpt-4o-mini", "OpenAI: GPT-4o Mini"},
		{"gpt-4o-2024-08-06", "OpenAI: GPT-4o"},
		{"gpt-4.1-nano", "OpenAI: GPT-4.1 Nano"},
		{"gpt-4-turbo-preview", "OpenAI: GPT-4 Turbo (Preview)"},
		{"gpt-5", "OpenAI: GPT-5"},
		{"gpt-5-mini", "OpenAI: GPT-5 Mini"},
		{"gpt-oss-120b", "OpenAI: GPT-OSS 120B"},
		{"chatgpt-4o-latest", "OpenAI: ChatGPT-4o"},
		{"o1", "OpenAI: o1"},
		{"o3-mini", "OpenAI: o3 Mini"},
		{"o4-mini-high", "OpenAI: o4 Mini High"},
		{"openai/gpt-4o", "OpenAI: GPT-4o"},
		{"gpt-7-5-turbo", "OpenAI: GPT-7.5 Turbo"},
		{"claude-opus-6", "Anthropic: Claude Opus 6"},
		{"claude-opus-5-5", "Anthropic: Claude Opus 5.5"},
		{"claude-3-5-sonnet-20241022", "Anthropic: Claude 3.5 Sonnet"},
		{"claude-3-7-sonnet-latest", "Anthropic: Claude 3.7 Sonnet"},
		{"claude-sonnet-4-5", "Anthropic: Claude Sonnet 4.5"},
		{"claude-haiku-4-5-20251001", "Anthropic: Claude Haiku 4.5"},
		{"claude-fable-5-1", "Anthropic: Claude Fable 5.1"},
		{"anthropic/claude-sonnet-4", "Anthropic: Claude Sonnet 4"},
		{"claude-3-opus-20240229", "Anthropic: Claude 3 Opus"},
		{"gemini-2.5-pro", "Google: Gemini 2.5 Pro"},
		{"gemini-2.5-pro-preview", "Google: Gemini 2.5 Pro (Preview)"},
		{"gemini-2.5-flash-preview-05-20", "Google: Gemini 2.5 Flash (Preview)"},
		{"gemini-2.0-flash-exp", "Google: Gemini 2.0 Flash (Experimental)"},
		{"gemini-2.0-flash-lite", "Google: Gemini 2.0 Flash Lite"},
		{"gemini-3-pro", "Google: Gemini 3 Pro"},
		{"models/gemini-1.5-flash-8b", "Google: Gemini 1.5 Flash 8B"},
		{"gemma-3-27b-it", "Google: Gemma 3 27B IT"},
		{"gemma-4-e2b-it", "Google: Gemma 4 E2B IT"},
		{"google/gemma-3n-e4b-it", "Google: Gemma 3n E4B IT"},
		{"qwen3-235b-a22b-instruct-2507", "Qwen: Qwen3 235B A22B Instruct 2507"},
		{"qwen3-30b-a3b", "Qwen: Qwen3 30B A3B"},
		{"qwen-max", "Qwen: Qwen Max"},
		{"qwen-plus-latest", "Qwen: Qwen Plus"},
		{"qwen2.5:7b", "Qwen: Qwen2.5 7B"},
		{"qwen2-5-coder-32b-instruct", "Qwen: Qwen2.5 Coder 32B Instruct"},
		{"qwen3-coder-480b-a35b-instruct", "Qwen: Qwen3 Coder 480B A35B Instruct"},
		{"qwq-32b", "Qwen: QwQ 32B"},
		{"qwen-vl-max", "Qwen: Qwen VL Max"},
		{"Qwen/Qwen3-8B", "Qwen: Qwen3 8B"},
		{"qwen3.5:0.8b", "Qwen: Qwen3.5 0.8B"},
		{"qwen4-max", "Qwen: Qwen4 Max"},
		{"deepseek-chat", "DeepSeek: DeepSeek Chat"},
		{"deepseek-reasoner", "DeepSeek: DeepSeek Reasoner"},
		{"deepseek-r1", "DeepSeek: DeepSeek R1"},
		{"deepseek-r1-0528", "DeepSeek: DeepSeek R1 0528"},
		{"deepseek-v3-1", "DeepSeek: DeepSeek V3.1"},
		{"deepseek-ai/DeepSeek-V3.2-Exp", "DeepSeek: DeepSeek V3.2 (Experimental)"},
		{"deepseek-r1:14b", "DeepSeek: DeepSeek R1 14B"},
		{"deepseek-v4", "DeepSeek: DeepSeek V4"},
		{"llama-3.1-8b-instant", "Meta: Llama 3.1 8B Instant"},
		{"llama-3-3-70b-versatile", "Meta: Llama 3.3 70B Versatile"},
		{"meta-llama/Llama-3.1-8B-Instruct", "Meta: Llama 3.1 8B Instruct"},
		{"llama3.2:3b", "Meta: Llama3.2 3B"},
		{"llama-4-maverick-17b-128e-instruct", "Meta: Llama 4 Maverick 17B 128e Instruct"},
		{"mistral-large-latest", "Mistral: Mistral Large"},
		{"mistral-small-2503", "Mistral: Mistral Small 2503"},
		{"mixtral-8x7b-32768", "Mistral: Mixtral 8x7B 32768"},
		{"codestral-latest", "Mistral: Codestral"},
		{"ministral-8b-latest", "Mistral: Ministral 8B"},
		{"magistral-medium-2506", "Mistral: Magistral Medium 2506"},
		{"mistralai/devstral-small", "Mistral: Devstral Small"},
		{"grok-4", "xAI: Grok 4"},
		{"grok-3-mini-beta", "xAI: Grok 3 Mini (Beta)"},
		{"x-ai/grok-code-fast-1", "xAI: Grok Code Fast 1"},
		{"glm-4.6", "Z.ai: GLM 4.6"},
		{"glm-4-5-air", "Z.ai: GLM 4.5 Air"},
		{"glm-4.5v", "Z.ai: GLM 4.5v"},
		{"kimi-k2-0905-preview", "MoonshotAI: Kimi K2 0905 (Preview)"},
		{"kimi-k2-turbo-preview", "MoonshotAI: Kimi K2 Turbo (Preview)"},
		{"moonshot-v1-8k", "MoonshotAI: Moonshot V1 8K"},
		{"MiniMax-M2", "MiniMax: MiniMax M2"},
		{"minimax-m1", "MiniMax: MiniMax M1"},
		{"doubao-seed-1-6-250615", "ByteDance: Doubao Seed 1.6 250615"},
		{"doubao-1-5-pro-32k", "ByteDance: Doubao 1.5 Pro 32K"},
		{"ernie-4.5-turbo-128k", "Baidu: Ernie 4.5 Turbo 128K"},
		{"hunyuan-turbos-latest", "Tencent: Hunyuan Turbos"},
		{"step-2-16k", "StepFun: Step 2 16K"},
		{"phi-4", "Microsoft: Phi 4"},
		{"phi3:mini", "Microsoft: Phi3 Mini"},
		{"nvidia/llama-3.1-nemotron-70b-instruct", "NVIDIA: Llama 3.1 Nemotron 70B Instruct"},
		{"command-r-plus", "Cohere: Command R Plus"},
		{"command-a-03-2025", "Cohere: Command A"},
		{"sonar-pro", "Perplexity: Sonar Pro"},
		{"smollm2:135m", "Hugging Face: SmolLM2 135M"},
		{"bartowski/Qwen_Qwen3.5-0.8B-GGUF", "Qwen: Qwen3.5 0.8B GGUF"},
		{"Qwen_Qwen3.5-2B-Q4_K_M", "Qwen: Qwen3.5 2B Q4_K_M"},
		{"my-finetune-v2", "My Finetune V2"},
	}
	for _, c := range cases {
		if got := friendlyModelName(c.id, "", ""); got != c.want {
			t.Errorf("%s\n  want %s\n  got  %s", c.id, c.want, got)
		}
	}
	// A listed name wins over the rules; a hint names an unknown vendor.
	if got := friendlyModelName("my-model", "", "Ollama"); got != "Ollama: My Model" {
		t.Errorf("hint: %q", got)
	}
	if got := friendlyModelName("gpt-4o", "", "Azure"); got != "OpenAI: GPT-4o" {
		t.Errorf("a known vendor beats the hint: %q", got)
	}
	if got := friendlyModelName("anthropic/claude-opus-5-5", "Anthropic: Claude Opus 5.5", ""); got != "Anthropic: Claude Opus 5.5" {
		t.Errorf("listed name: %q", got)
	}
	if v := modelVendorOf("steps-to-success"); v != "" {
		t.Errorf("steps-to-success: %q", v)
	}
	if v := modelVendorOf("unsloth/gemma-3-4b"); v != "Google" {
		t.Errorf("unsloth/gemma: %q", v)
	}
}
