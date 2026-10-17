package gui

// Model names as people read them: OpenRouter's "Vendor: Model", derived
// from a raw id by rule so new models need no update. Ported from
// MyApps-AI's model_naming.dart (same author; doc/en-us/gui.md, "AI").
//
// Rules, in order: drop an `org/` prefix (kept as a vendor clue), an Ollama
// `:latest` tag and dates; turn preview/beta/experimental into a suffix;
// merge adjacent one- or two-digit numbers into a version (`5-5` → `5.5`,
// `qwen2-5` → `Qwen2.5`); upper-case sizes and short version marks (`8b` →
// `8B`, `a22b` → `A22B`, `r1` → `R1`; `o3` stays); apply brand casing; keep
// casing the id already has.

import (
	"regexp"
	"strconv"
	"strings"
)

// orgVendors are vendors by `org/` prefix as Hugging Face and OpenRouter
// write them; an empty vendor is a hosting org, no vendor.
var orgVendors = map[string]string{
	"openai": "OpenAI", "anthropic": "Anthropic", "google": "Google", "meta-llama": "Meta", "meta": "Meta",
	"facebook": "Meta", "mistralai": "Mistral", "mistral": "Mistral", "qwen": "Qwen", "alibaba": "Qwen",
	"deepseek": "DeepSeek", "deepseek-ai": "DeepSeek", "x-ai": "xAI", "xai": "xAI", "moonshotai": "MoonshotAI",
	"moonshot": "MoonshotAI", "z-ai": "Z.ai", "zai-org": "Z.ai", "zhipuai": "Z.ai", "thudm": "Z.ai",
	"minimax": "MiniMax", "minimaxai": "MiniMax", "bytedance": "ByteDance", "bytedance-seed": "ByteDance",
	"baidu": "Baidu", "tencent": "Tencent", "stepfun-ai": "StepFun", "stepfun": "StepFun",
	"microsoft": "Microsoft", "nvidia": "NVIDIA", "cohere": "Cohere", "coherelabs": "Cohere",
	"ibm-granite": "IBM", "ibm": "IBM", "amazon": "Amazon", "perplexity": "Perplexity", "liquid": "Liquid",
	"nousresearch": "Nous", "allenai": "Ai2", "inclusionai": "inclusionAI",
	"bartowski": "", "unsloth": "", "lmstudio-community": "",
}

// prefixVendors are vendors by the leading letters of the model name.
var prefixVendors = map[string]string{
	"gpt": "OpenAI", "chatgpt": "OpenAI", "o1": "OpenAI", "o3": "OpenAI", "o4": "OpenAI", "davinci": "OpenAI",
	"text-embedding": "OpenAI", "whisper": "OpenAI", "dall-e": "OpenAI", "claude": "Anthropic",
	"gemini": "Google", "gemma": "Google", "qwen": "Qwen", "qwq": "Qwen", "qvq": "Qwen", "deepseek": "DeepSeek",
	"glm": "Z.ai", "chatglm": "Z.ai", "kimi": "MoonshotAI", "moonshot": "MoonshotAI", "llama": "Meta",
	"mistral": "Mistral", "mixtral": "Mistral", "ministral": "Mistral", "codestral": "Mistral",
	"magistral": "Mistral", "devstral": "Mistral", "pixtral": "Mistral", "voxtral": "Mistral", "grok": "xAI",
	"minimax": "MiniMax", "abab": "MiniMax", "doubao": "ByteDance", "seed": "ByteDance", "ernie": "Baidu",
	"hunyuan": "Tencent", "step": "StepFun", "phi": "Microsoft", "nemotron": "NVIDIA", "command": "Cohere",
	"aya": "Cohere", "sonar": "Perplexity", "granite": "IBM", "nova": "Amazon", "smollm": "Hugging Face",
}

// brands is the casing of known words; anything else is capitalized.
var brands = map[string]string{
	"gpt": "GPT", "chatgpt": "ChatGPT", "glm": "GLM", "chatglm": "ChatGLM", "deepseek": "DeepSeek",
	"minimax": "MiniMax", "qwen": "Qwen", "qwq": "QwQ", "qvq": "QVQ", "smollm": "SmolLM", "oss": "OSS",
	"it": "IT", "vl": "VL", "tts": "TTS", "asr": "ASR", "ocr": "OCR", "ai": "AI", "moe": "MoE", "hd": "HD",
	"api": "API", "gguf": "GGUF", "awq": "AWQ", "gptq": "GPTQ", "fp8": "FP8", "fp16": "FP16", "bf16": "BF16",
	"f16": "F16", "mlx": "MLX", "qat": "QAT", "dpo": "DPO", "sft": "SFT", "rl": "RL",
}

// suffixWords are shown in parentheses at the end.
var suffixWords = map[string]string{
	"preview": "Preview", "exp": "Experimental", "experimental": "Experimental", "beta": "Beta", "alpha": "Alpha",
}

var (
	quantRe       = regexp.MustCompile(`(?i)i?q\d(?:_[0-9a-z]+)+`)
	numberRe      = regexp.MustCompile(`^\d+$`)
	sizeRe        = regexp.MustCompile(`(?i)^\d+(?:\.\d+)?[bmkt]$`)
	multiSizeRe   = regexp.MustCompile(`(?i)^\d+x\d+(?:\.\d+)?b$`)
	activeSizeRe  = regexp.MustCompile(`(?i)^[ae]\d+(?:\.\d+)?b$`)
	letterVerRe   = regexp.MustCompile(`(?i)^[a-z]\d+(?:\.\d+)*$`)
	leadingRe     = regexp.MustCompile(`(?i)^([a-z]+)(\d.*)$`)
	splitRe       = regexp.MustCompile(`[-_ ]+`)
	lettersRe     = regexp.MustCompile(`(?i)^[a-z]+$`)
	letterDigitRe = regexp.MustCompile(`(?i)[a-z]\d+$`)
	yearRe        = regexp.MustCompile(`^20\d\d$`)
	prefixEndRe   = regexp.MustCompile(`[-_.\d ]`)
)

// modelVendorOf is the vendor of a model id: by `org/` prefix, then by the
// longest known name prefix ending at a separator, a digit or the end;
// "" when unknown.
func modelVendorOf(id string) string {
	segments := strings.Split(strings.TrimSpace(id), "/")
	for i := len(segments) - 2; i >= 0; i-- {
		if v := orgVendors[strings.ToLower(segments[i])]; v != "" {
			return v
		}
	}
	base := strings.ToLower(strings.SplitN(segments[len(segments)-1], ":", 2)[0])
	best, bestLen := "", 0
	for prefix, vendor := range prefixVendors {
		if !strings.HasPrefix(base, prefix) || len(prefix) <= bestLen {
			continue
		}
		if len(base) == len(prefix) || prefixEndRe.MatchString(base[len(prefix):len(prefix)+1]) {
			best, bestLen = vendor, len(prefix)
		}
	}
	return best
}

// friendlyModelName names a raw model id for people: "Vendor: Name", or
// "Name" when no vendor is known. A name the provider listed wins over the
// rules; vendorHint (the source's name) is used when no rule finds one.
func friendlyModelName(id, listedName, vendorHint string) string {
	who := modelVendorOf(id)
	if who == "" {
		who = strings.TrimSpace(vendorHint)
	}
	what := strings.TrimSpace(listedName)
	if what == "" {
		what = modelNameOf(id)
	}
	if who == "" || strings.HasPrefix(what, who+": ") {
		return what
	}
	return who + ": " + what
}

// modelNameOf is the model part of a friendly name.
func modelNameOf(id string) string {
	base := strings.TrimSpace(id)
	base = base[strings.LastIndex(base, "/")+1:]
	// Ollama tags: `qwen2.5:7b` → size; `:latest` → nothing.
	if colon := strings.Index(base, ":"); colon > 0 {
		tag := base[colon+1:]
		base = base[:colon]
		if tag != "" && !strings.EqualFold(tag, "latest") {
			base += "-" + tag
		}
	}
	// Protect quantizations such as q4_k_m from being split on `_` (only
	// where they start a word: no letter or digit before them).
	base = protectQuant(base)
	var tokens []string
	for _, t := range splitRe.Split(base, -1) {
		if t != "" {
			tokens = append(tokens, strings.ReplaceAll(t, "\x00", "_"))
		}
	}
	// File names repeat the org: `Qwen_Qwen3.5-0.8B` → `Qwen3.5-0.8B`.
	if len(tokens) > 1 && lettersRe.MatchString(tokens[0]) &&
		strings.HasPrefix(strings.ToLower(tokens[1]), strings.ToLower(tokens[0])) && len(tokens[1]) > len(tokens[0]) {
		tokens = tokens[1:]
	}
	tokens = dropDates(tokens)
	var kept, suffixes []string
	for _, t := range tokens {
		lower := strings.ToLower(t)
		if s, ok := suffixWords[lower]; ok {
			if !contains(suffixes, s) {
				suffixes = append(suffixes, s)
			}
			continue
		}
		if lower != "latest" {
			kept = append(kept, t)
		}
	}
	words := mergeVersions(kept)
	var out strings.Builder
	for i, t := range words {
		w := caseWord(t)
		if i > 0 {
			// OpenRouter keeps GPT's hyphen: `GPT-4o Mini`, `GPT-OSS 120B`.
			if prev := caseWord(words[i-1]); prev == "GPT" || prev == "ChatGPT" {
				out.WriteByte('-')
			} else {
				out.WriteByte(' ')
			}
		}
		out.WriteString(w)
	}
	for _, s := range suffixes {
		out.WriteString(" (" + s + ")")
	}
	if out.Len() == 0 {
		return id
	}
	return out.String()
}

// protectQuant replaces the underscores of quantization marks that start a
// word with NUL, so splitting keeps them whole.
func protectQuant(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range quantRe.FindAllStringIndex(s, -1) {
		if m[0] > 0 && isAlnum(s[m[0]-1]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(strings.ReplaceAll(s[m[0]:m[1]], "_", "\x00"))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// dropDates removes date stamps: `20241022`, `2024-10-22`, a month-day pair
// such as `05-20` and a trailing month-year such as `03-2025`; four-digit
// version tags such as `2507` or `0528` stay.
func dropDates(tokens []string) []string {
	var out []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if numberRe.MatchString(t) && len(t) == 8 && strings.HasPrefix(t, "20") {
			continue
		}
		if numberRe.MatchString(t) && len(t) == 4 && strings.HasPrefix(t, "20") && i+2 < len(tokens) && isMonthDay(tokens[i+1], tokens[i+2]) {
			i += 2
			continue
		}
		if i+2 == len(tokens) && len(t) == 2 && yearRe.MatchString(tokens[i+1]) {
			if m, err := strconv.Atoi(t); err == nil && m >= 1 && m <= 12 {
				break
			}
		}
		if i+1 < len(tokens) && len(t) == 2 && isMonthDay(t, tokens[i+1]) && (i+2 == len(tokens) || !numberRe.MatchString(tokens[i+2])) {
			i++
			continue
		}
		out = append(out, t)
	}
	return out
}

// isMonthDay reports whether m and d read as a two-digit month and day.
func isMonthDay(m, d string) bool {
	if len(m) != 2 || len(d) != 2 {
		return false
	}
	month, err1 := strconv.Atoi(m)
	day, err2 := strconv.Atoi(d)
	return err1 == nil && err2 == nil && month >= 1 && month <= 12 && day >= 1 && day <= 31
}

// mergeVersions joins split version numbers: two adjacent one- or two-digit
// numbers become `a.b` (not three or more, which are ambiguous); a word
// ending in a digit followed by a one-digit number becomes `word.n`.
func mergeVersions(tokens []string) []string {
	small := func(t string) bool { return numberRe.MatchString(t) && len(t) <= 2 }
	var out []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		var next, after string
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		if i+2 < len(tokens) {
			after = tokens[i+2]
		}
		before := ""
		if len(out) > 0 {
			before = out[len(out)-1]
		}
		if next != "" && small(t) && small(next) && (after == "" || !small(after)) && (before == "" || !small(before)) {
			out = append(out, t+"."+next)
			i++
			continue
		}
		if next != "" && !numberRe.MatchString(t) && letterDigitRe.MatchString(t) && numberRe.MatchString(next) && len(next) == 1 && (after == "" || !small(after)) {
			out = append(out, t+"."+next)
			i++
			continue
		}
		out = append(out, t)
	}
	return out
}

// caseWord cases one word.
func caseWord(t string) string {
	lower := strings.ToLower(t)
	if b, ok := brands[lower]; ok {
		return b
	}
	if loc := quantRe.FindStringIndex(t); loc != nil && loc[0] == 0 && loc[1] == len(t) {
		return strings.ToUpper(t)
	}
	if sizeRe.MatchString(t) || activeSizeRe.MatchString(t) {
		return strings.ToUpper(t)
	}
	if multiSizeRe.MatchString(t) {
		return lower[:len(lower)-1] + "B"
	}
	if letterVerRe.MatchString(t) {
		if strings.HasPrefix(lower, "o") {
			return lower
		}
		return strings.ToUpper(t[:1]) + t[1:]
	}
	// Keep a word that already has capitals (`DeepSeek`, `Llama`).
	if t != lower {
		return t
	}
	if m := leadingRe.FindStringSubmatch(t); m != nil {
		letters := strings.ToLower(m[1])
		if b, ok := brands[letters]; ok {
			return b + m[2]
		}
		return capitalize(letters) + m[2]
	}
	if t != "" && t[0] >= '0' && t[0] <= '9' {
		return t
	}
	return capitalize(t)
}

func capitalize(t string) string {
	if t == "" {
		return t
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
