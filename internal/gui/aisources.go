package gui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// aiTemplate is a provider people can add with its address filled in
// (doc/en-us/computer-use.md, "Providers"). The OpenAI-compatible ones come
// from MyApps-AI's catalog, keeping those whose API takes images and tool
// calls.
type aiTemplate struct {
	id, name string
	// protocol is "anthropic", "openai-responses" or "openai-chat".
	protocol  string
	endpoints []aiEndpoint
	// model is suggested for a new source; empty: the user adds or lists.
	model string
}

// aiEndpoint is one of a template's sites.
type aiEndpoint struct{ label, url string }

func one(url string) []aiEndpoint { return []aiEndpoint{{"", url}} }

// aiTemplates are offered by name; Custom comes last.
var aiTemplates = []aiTemplate{
	{"anthropic", "Claude (Anthropic)", "anthropic", one("https://api.anthropic.com/v1"), "claude-opus-5-5"},
	{"openai", "OpenAI", "openai-responses", one("https://api.openai.com/v1"), "gpt-6.1-sol"},
	{"openrouter", "OpenRouter", "openai-chat", one("https://openrouter.ai/api/v1"), ""},
	{"dashscope", "Alibaba Cloud Model Studio", "openai-chat", []aiEndpoint{
		{"International (Singapore)", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
		{"China (Beijing)", "https://dashscope.aliyuncs.com/compatible-mode/v1"}}, ""},
	{"qianfan", "Baidu Qianfan", "openai-chat", one("https://qianfan.baidubce.com/v2"), ""},
	{"deepinfra", "Deep Infra", "openai-chat", one("https://api.deepinfra.com/v1/openai"), ""},
	{"fireworks", "Fireworks AI", "openai-chat", one("https://api.fireworks.ai/inference/v1"), ""},
	{"githubModels", "GitHub Models", "openai-chat", one("https://models.github.ai/inference"), ""},
	{"gemini", "Google Gemini", "openai-chat", one("https://generativelanguage.googleapis.com/v1beta/openai"), ""},
	{"groq", "Groq", "openai-chat", one("https://api.groq.com/openai/v1"), ""},
	{"huggingface", "Hugging Face Inference Providers", "openai-chat", one("https://router.huggingface.co/v1"), ""},
	{"llamacppServer", "llama.cpp server", "openai-chat", one("http://localhost:8080/v1"), ""},
	{"lmstudio", "LM Studio", "openai-chat", one("http://localhost:1234/v1"), ""},
	{"minimax", "MiniMax", "openai-chat", []aiEndpoint{
		{"api.minimax.io", "https://api.minimax.io/v1"}, {"api.minimaxi.com", "https://api.minimaxi.com/v1"}}, ""},
	{"mistral", "Mistral AI", "openai-chat", one("https://api.mistral.ai/v1"), ""},
	{"moonshot", "Moonshot AI (Kimi)", "openai-chat", []aiEndpoint{
		{"api.moonshot.ai", "https://api.moonshot.ai/v1"}, {"api.moonshot.cn", "https://api.moonshot.cn/v1"}}, ""},
	{"nvidia", "NVIDIA NIM", "openai-chat", one("https://integrate.api.nvidia.com/v1"), ""},
	{"ollama", "Ollama", "openai-chat", one("http://localhost:11434/v1"), ""},
	{"siliconflow", "SiliconFlow", "openai-chat", []aiEndpoint{
		{"api.siliconflow.com", "https://api.siliconflow.com/v1"}, {"api.siliconflow.cn", "https://api.siliconflow.cn/v1"}}, ""},
	{"stepfun", "StepFun", "openai-chat", one("https://api.stepfun.com/v1"), ""},
	{"hunyuan", "Tencent Hunyuan", "openai-chat", one("https://api.hunyuan.cloud.tencent.com/v1"), ""},
	{"together", "Together AI", "openai-chat", one("https://api.together.xyz/v1"), ""},
	{"volcengine", "Volcengine Ark", "openai-chat", one("https://ark.cn-beijing.volces.com/api/v3"), ""},
	{"xai", "xAI", "openai-chat", one("https://api.x.ai/v1"), ""},
	{"zhipu", "Z.ai / BigModel", "openai-chat", []aiEndpoint{
		{"Z.ai", "https://api.z.ai/api/paas/v4"}, {"BigModel", "https://open.bigmodel.cn/api/paas/v4"}}, ""},
	{"custom", "", "openai-chat", one(""), ""},
}

// aiProtocols are the formats a custom source may use, in the order the
// editor offers them.
var aiProtocols = []string{"openai-chat", "anthropic", "openai-responses"}

func templateOf(id string) aiTemplate {
	for _, t := range aiTemplates {
		if t.id == id {
			return t
		}
	}
	return aiTemplates[len(aiTemplates)-1]
}

func templateName(t aiTemplate) string {
	if t.id == "custom" {
		return L.AICustom()
	}
	return t.name
}

// aiEdit is the source being added or changed; its key field is
// write-only.
type aiEdit struct {
	isNew    bool
	src      localapi.AISource
	key      string
	newModel string
	// fetched are the models the source listed; pick is the chosen one.
	fetched []localapi.AIModel
	pick    string
}

// isLocal reports whether a URL names this computer (no key needed).
func isLocal(url string) bool {
	for _, p := range []string{"http://localhost", "http://127.", "http://[::1]"} {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	return false
}

// aiReady reports whether the chosen model's source can be used: it has a
// key, or is a server on this computer.
func aiReady(s *localapi.AISettings) bool {
	if s == nil || s.Model == nil {
		return false
	}
	src := aiSource(s, s.Model.Source)
	return src != nil && (s.Keys[src.ID] || src.Protocol == "openai-chat" && isLocal(src.URL))
}

func aiSource(s *localapi.AISettings, id string) *localapi.AISource {
	for i := range s.Sources {
		if s.Sources[i].ID == id {
			return &s.Sources[i]
		}
	}
	return nil
}

// aiModelLabel names a model for people: OpenRouter's "Vendor: Model",
// with the source when it is not the vendor's own.
func aiModelLabel(src *localapi.AISource, m localapi.AIModel) string {
	name := friendlyModelName(m.ID, m.Name, src.Name)
	if t := templateOf(src.Template); t.protocol == "openai-chat" || t.id == "custom" {
		return name + " · " + src.Name
	}
	return name
}

// aiTaskModel names a task's model by its source on this computer.
func aiTaskModel(s *localapi.AISettings, task *localapi.AITask) string {
	if s != nil {
		if src := aiSource(s, task.Source); src != nil {
			return aiModelLabel(src, localapi.AIModel{ID: task.Model})
		}
	}
	return friendlyModelName(task.Model, "", "")
}

// aiModelChoices are the models tasks may use — every model of every
// source, by its name (aiModelLabel) — and the one chosen's name.
func aiModelChoices(s *localapi.AISettings) (labels []string, refs []localapi.AIModelRef, cur string) {
	for i := range s.Sources {
		src := &s.Sources[i]
		for _, m := range src.Models {
			label := aiModelLabel(src, m)
			labels = append(labels, label)
			refs = append(refs, localapi.AIModelRef{Source: src.ID, ID: m.ID})
			if s.Model != nil && *s.Model == refs[len(refs)-1] {
				cur = label
			}
		}
	}
	return labels, refs, cur
}

// aiProviders are the AI page's settings: Defaults for new tasks (the model
// and when to ask), the providers with their models and key — Add provider
// from a template beside the title, the editor under its row — and who
// else may run tasks here (set in Access).
func (a *App) aiProviders(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	s, edit, busy, pick := a.ai.settings, a.ai.edit, a.busy["ai"], a.ai.template
	a.mu.Unlock()
	if s == nil {
		ui.Row(c.Key("ai-loading")).Padding(18, 0, 0, 0).Children(func() { busyMark(c, L.AISettingsTitle(), busySizeLarge) })
		return
	}
	labels, refs, cur := aiModelChoices(s)
	policies := []string{L.AIApprovalFlaggedOption(), L.AIApprovalEveryOption()}
	policy := policies[0]
	if s.Approval == "every-action" {
		policy = policies[1]
	}
	a.section(c, "ai-defaults", L.SectionAIDefaults(), nil, func(r *rows) {
		r.row(c, "ai-model", tile(navAI), setting{title: L.AIModel(), about: L.AIModelAbout()}, func() {
			if ui.Select(c.Key("ai-model-sel"), &cur, labels).Width(280).Disabled(busy).Changed() {
				if i := slices.Index(labels, cur); i >= 0 {
					ref := refs[i]
					a.saveAISettings(&localapi.AISettings{Model: &ref}, "")
				}
			}
		})
		r.row(c, "ai-policy", tile(rowCheck), setting{title: L.AIApproval(), about: L.AIApprovalAbout()}, func() {
			if ui.Select(c.Key("ai-policy-sel"), &policy, policies).Width(280).Disabled(busy).Changed() {
				a.saveAISettings(&localapi.AISettings{Approval: []string{"flagged", "every-action"}[slices.Index(policies, policy)]}, "")
			}
		})
	})
	names := make([]string, len(aiTemplates))
	for i, tp := range aiTemplates {
		names[i] = templateName(tp)
	}
	if pick == "" {
		pick = names[2] // OpenRouter
	}
	aside := func() {
		if ui.Select(c.Key("ai-template"), &pick, names).Width(200).Disabled(edit != nil).Changed() {
			a.update(func() { a.ai.template = pick })
		}
		if ui.Button(c.Key("ai-add"), L.AIAddSource()).Disabled(busy || edit != nil).Clicked() {
			tp := aiTemplates[max(0, slices.Index(names, pick))]
			a.update(func() { a.ai.edit = newAIEdit(s, tp) })
		}
	}
	a.section(c, "ai-sources", L.AISources(), aside, func(r *rows) {
		if edit != nil && edit.isNew {
			r.block(c, "ai-editor-new", func() { a.aiSourceEditor(c, edit, busy) })
		}
		for i := range s.Sources {
			src := s.Sources[i]
			state, col := L.AINoKeyShort(), t.Warning
			switch {
			case s.Keys[src.ID]:
				state, col = L.AIKeyShort(), t.Success
			case src.Protocol == "openai-chat" && isLocal(src.URL):
				state, col = L.AINoKeyNeeded(), t.TextMuted
			}
			about := fmt.Sprintf("%s · %s", templateName(templateOf(src.Template)), L.AIModelCount(len(src.Models)))
			r.row(c, "ai-src-"+src.ID, letterTile(src.Name), setting{title: src.Name, about: about}, func() {
				ui.Text(c, state).FontSize(sizeSmall).TextColor(textTone(t, col))
				if ui.Button(c.Key("ai-src-edit-"+src.ID), L.AIEditSource()).Disabled(busy || edit != nil).Clicked() {
					a.update(func() { a.ai.edit = &aiEdit{src: src} })
				}
				if ui.Button(c.Key("ai-src-rm-"+src.ID), L.ActionRemove()).Disabled(busy || edit != nil || len(s.Sources) == 1).Clicked() {
					a.removeAISource(src.ID)
				}
			})
			if edit != nil && !edit.isNew && edit.src.ID == src.ID {
				r.block(c, "ai-editor-"+src.ID, func() { a.aiSourceEditor(c, edit, busy) })
			}
		}
	})
	a.mu.Lock()
	protected := a.protected()
	a.mu.Unlock()
	if protected {
		return // Access stays hidden until protected mode is off
	}
	a.section(c, "ai-others", L.SectionOtherDevices(), nil, func(r *rows) {
		r.row(c, "ai-who", tile(navPermissions), setting{title: L.AIWhoMay(), about: L.AIWhoMayAbout()}, func() {
			if ui.Button(c.Key("ai-open-access"), L.NavAccess()).Clicked() {
				a.mu.Lock()
				a.page = pageAccess
				a.mu.Unlock()
			}
		})
	})
}

// newAIEdit starts a new source from a template, with an id no other
// source has.
func newAIEdit(s *localapi.AISettings, tp aiTemplate) *aiEdit {
	id := strings.ToLower(tp.id)
	for n := 2; aiSource(s, id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", strings.ToLower(tp.id), n)
	}
	src := localapi.AISource{ID: id, Name: templateName(tp), Template: tp.id, Protocol: tp.protocol, URL: tp.endpoints[0].url}
	if tp.model != "" {
		src.Models = []localapi.AIModel{{ID: tp.model}}
	}
	return &aiEdit{isNew: true, src: src}
}

// aiSourceEditor edits one source: its name, address, format (custom
// only), key and models.
func (a *App) aiSourceEditor(c *ui.Context, e *aiEdit, busy bool) {
	t := c.Theme()
	f := *e
	keep := func(el ui.Element) {
		if el.Changed() {
			a.update(func() { *a.ai.edit = f })
		}
	}
	tp := templateOf(f.src.Template)
	ui.Column(c.Key("ai-editor")).Gap(gap).FillWidth().Padding(8, 0).Children(func() {
		ui.Text(c, templateName(tp)).Bold()
		ui.Text(c, L.AISourceName())
		keep(ui.TextInput(c.Key("ai-ed-name"), &f.src.Name).FillWidth().Disabled(busy))
		ui.Text(c, L.AISourceAddress())
		if len(tp.endpoints) > 1 {
			labels := make([]string, len(tp.endpoints))
			cur := labels[0]
			for i, ep := range tp.endpoints {
				labels[i] = ep.label
				if ep.url == f.src.URL {
					cur = ep.label
				}
			}
			if ui.Select(c.Key("ai-ed-endpoint"), &cur, labels).Width(300).Disabled(busy).Changed() {
				f.src.URL = tp.endpoints[slices.Index(labels, cur)].url
				a.update(func() { *a.ai.edit = f })
			}
		}
		keep(ui.TextInput(c.Key("ai-ed-url"), &f.src.URL).Placeholder("https://…/v1").FillWidth().Disabled(busy))
		if tp.id == "custom" {
			idx := max(0, slices.Index(aiProtocols, f.src.Protocol))
			ui.Text(c, L.AISourceFormat())
			if ui.Segmented(c.Key("ai-ed-proto"), &idx, L.AIProtocolChat(), L.AIProtocolAnthropic(), L.AIProtocolResponses()).Changed() {
				f.src.Protocol = aiProtocols[idx]
				a.update(func() { *a.ai.edit = f })
			}
		}
		if f.src.Protocol == "openai-chat" {
			ui.Text(c, L.AIChatNote()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		ui.Text(c, L.AIKey())
		keep(ui.TextInput(c.Key("ai-ed-key"), &f.key).Password().FillWidth().Disabled(busy))
		a.mu.Lock()
		stored := a.ai.settings != nil && a.ai.settings.Keys[f.src.ID]
		a.mu.Unlock()
		keyHint := L.AIKeyMissing()
		switch {
		case stored:
			keyHint = L.AIKeyStored()
		case f.src.Protocol == "openai-chat" && isLocal(f.src.URL):
			keyHint = L.AINoKeyNeeded()
		}
		ui.Text(c, keyHint).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Text(c, L.AIModels())
		for i, m := range f.src.Models {
			ui.Row(c.Key("ai-ed-m-" + m.ID)).Gap(gap).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, friendlyModelName(m.ID, m.Name, f.src.Name))
					ui.Text(c, m.ID).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
				})
				if ui.Button(c.Key("ai-ed-m-rm-"+m.ID), L.ActionRemove()).Disabled(busy).Clicked() {
					f.src.Models = slices.Delete(slices.Clone(f.src.Models), i, i+1)
					a.update(func() { *a.ai.edit = f })
				}
			})
		}
		ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
			keep(ui.TextInput(c.Key("ai-ed-new"), &f.newModel).Placeholder(L.AIModelID()).Grow(1).Disabled(busy))
			if ui.Button(c.Key("ai-ed-add"), L.AIAddModel()).Disabled(busy || strings.TrimSpace(f.newModel) == "").Clicked() {
				f.src.Models = addModel(f.src.Models, localapi.AIModel{ID: strings.TrimSpace(f.newModel)})
				f.newModel = ""
				a.update(func() { *a.ai.edit = f })
			}
		})
		// Listing asks the provider; only for a saved source, with its key.
		if !f.isNew {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("ai-ed-list"), L.AIListModels()).Disabled(busy).Clicked() {
					a.listAIModels(f.src.ID)
				}
				if len(f.fetched) > 0 {
					labels := make([]string, len(f.fetched))
					for i, m := range f.fetched {
						labels[i] = friendlyModelName(m.ID, m.Name, f.src.Name)
					}
					pick := f.pick
					if pick == "" {
						pick = labels[0]
					}
					if ui.Select(c.Key("ai-ed-fetched"), &pick, labels).Width(320).Changed() {
						f.pick = pick
						a.update(func() { *a.ai.edit = f })
					}
					if ui.Button(c.Key("ai-ed-fetched-add"), L.AIAddModel()).Disabled(busy).Clicked() {
						f.src.Models = addModel(f.src.Models, f.fetched[max(0, slices.Index(labels, pick))])
						a.update(func() { *a.ai.edit = f })
					}
				}
			})
		}
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key("ai-ed-save"), L.ActionSave()).Disabled(busy || len(f.src.Models) == 0 || strings.TrimSpace(f.src.Name) == "").Clicked() {
				a.saveAISource(f)
			}
			if ui.Button(c.Key("ai-ed-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
				a.update(func() { a.ai.edit = nil })
			}
		})
	})
}

func addModel(models []localapi.AIModel, m localapi.AIModel) []localapi.AIModel {
	if slices.ContainsFunc(models, func(x localapi.AIModel) bool { return x.ID == m.ID }) {
		return models
	}
	return append(slices.Clone(models), m)
}

// saveAISource stores the edited source (and its key when one was typed);
// a new source's first model becomes the one tasks use.
func (a *App) saveAISource(f aiEdit) {
	a.mu.Lock()
	s := a.ai.settings
	a.mu.Unlock()
	if s == nil {
		return
	}
	src := f.src
	src.Name, src.URL = strings.TrimSpace(src.Name), strings.TrimSpace(src.URL)
	sources := slices.Clone(s.Sources)
	if i := slices.IndexFunc(sources, func(x localapi.AISource) bool { return x.ID == src.ID }); i >= 0 {
		sources[i] = src
	} else {
		sources = append(sources, src)
	}
	change := &localapi.AISettings{Sources: sources}
	// The chosen model must still exist; a new source's first model is
	// chosen.
	if f.isNew || s.Model == nil || s.Model.Source == src.ID && !slices.ContainsFunc(src.Models, func(m localapi.AIModel) bool { return m.ID == s.Model.ID }) {
		change.Model = &localapi.AIModelRef{Source: src.ID, ID: src.Models[0].ID}
	}
	a.saveAISettings(change, f.key, src.ID)
}

// removeAISource drops a source (and its key); the chosen model moves to
// the first remaining source's first model when it was the removed one's.
func (a *App) removeAISource(id string) {
	a.mu.Lock()
	s := a.ai.settings
	a.mu.Unlock()
	sources := slices.DeleteFunc(slices.Clone(s.Sources), func(x localapi.AISource) bool { return x.ID == id })
	if len(sources) == 0 {
		return
	}
	change := &localapi.AISettings{Sources: sources}
	if s.Model == nil || s.Model.Source == id {
		change.Model = &localapi.AIModelRef{Source: sources[0].ID, ID: sources[0].Models[0].ID}
	}
	a.saveAISettings(change, "")
}

// saveAISettings sends a change, then the key typed for a source.
// Side effects: the engine stores the key in its secrets store.
func (a *App) saveAISettings(change *localapi.AISettings, key string, source ...string) {
	a.update(func() { a.busy["ai"] = true })
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAISettingsSet, AISettings: change})
		if err == nil && key != "" && len(source) == 1 {
			resp, err = a.requestWith(localapi.Request{Type: localapi.RequestAIKeySet, AIKey: &localapi.AIKey{Source: source[0], Key: &key}})
		}
		a.update(func() {
			delete(a.busy, "ai")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil || resp.AISettings == nil:
				a.setToast(L.EngineDown(), true)
			default:
				a.ai.settings, a.ai.edit = resp.AISettings, nil
				a.setToast(L.AISaved(), false)
			}
		})
	}()
}

// listAIModels asks a source for its models (only on this request).
func (a *App) listAIModels(source string) {
	a.update(func() { a.busy["ai"] = true })
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAIModels, Source: source})
		a.update(func() {
			delete(a.busy, "ai")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			case a.ai.edit != nil:
				a.ai.edit.fetched, a.ai.edit.pick = resp.AIModels, ""
				if len(resp.AIModels) == 0 {
					a.setToast(L.AINoModelsListed(), true)
				}
			}
		})
	}()
}
