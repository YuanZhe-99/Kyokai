// Spike 5 (PLAN.md, 0.7.0, "Pi as the agent backend"): Pi's agent loop in a
// sidecar that owns nothing but the loop. Kyokai's engine (here the harness)
// starts it, answers its key requests and its actions over stdin/stdout as
// JSON lines, and stays the only thing that acts. The provider is Kyokai's
// own (an `openai-chat` stream registered with Pi), so Pi's built-in
// adapters, catalog, cache warming, attribution and files are never used.
//
// engine → sidecar: {t:"start", url, model, system, instruction, shot},
//                   {t:"key", key}, {t:"result", id, text?, shot?}, {t:"abort"}
// sidecar → engine: {t:"hello", v:1, pid}, {t:"key?"}, {t:"act", id, name, args},
//                   {t:"turn", text, usage}, {t:"done"|"aborted"|"failed", ...},
//                   {t:"net", host} for every request it makes

import {
	type AssistantMessage,
	collapseSystemMessages,
	createAssistantMessageEventStream,
	getCurrentSystemPrompt,
	getCurrentTools,
	type SimpleStreamOptions,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import {
	createAgentSession,
	createExtensionRuntime,
	ModelRuntime,
	type ResourceLoader,
	SessionManager,
	SettingsManager,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const send = (m: unknown) => process.stdout.write(`${JSON.stringify(m)}\n`);

// Every request the sidecar makes is reported: the spike checks that only the
// provider is ever contacted.
const realFetch = globalThis.fetch;
globalThis.fetch = (async (input: any, init?: any) => {
	const url = typeof input === "string" ? input : input.url;
	send({ t: "net", host: new URL(url).host });
	return realFetch(input, init);
}) as typeof fetch;

// Lines from the engine, handed to whoever waits for them.
const waiting = new Map<string, (m: any) => void>();
let onStart: (m: any) => void;
let aborter: () => void = () => {};
const started = new Promise<any>((r) => (onStart = r));
let buf = "";
process.stdin.on("data", (d) => {
	buf += d.toString();
	for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
		const line = buf.slice(0, i);
		buf = buf.slice(i + 1);
		if (!line.trim()) continue;
		const m = JSON.parse(line);
		if (m.t === "start") onStart(m);
		else if (m.t === "abort") aborter();
		else if (m.t === "key") waiting.get("key")?.(m);
		else if (m.t === "result") waiting.get(m.id)?.(m);
	}
});
// The engine gone (killed, crashed): the sidecar ends with it, keeping
// nothing, so no task can resume on its own.
process.stdin.on("end", () => process.exit(0));
const ask = (key: string, msg: unknown) =>
	new Promise<any>((r) => {
		waiting.set(key, (m) => {
			waiting.delete(key);
			r(m);
		});
		send(msg);
	});

// Kyokai's openai-chat provider: the request is built here, byte for byte as
// Kyokai wants it (images passed through, no resizing).
function chatStream(model: any, context: TranscriptContext, options?: SimpleStreamOptions) {
	const stream = createAssistantMessageEventStream();
	const transcript = collapseSystemMessages(context);
	const system = getCurrentSystemPrompt(transcript.messages);
	const tools = getCurrentTools(transcript.messages);
	(async () => {
		const out: AssistantMessage = {
			role: "assistant",
			content: [],
			api: model.api,
			provider: model.provider,
			model: model.id,
			usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
			stopReason: "pending",
			timestamp: Date.now(),
		} as AssistantMessage;
		try {
			const messages: any[] = [{ role: "system", content: system }];
			for (const m of transcript.messages as any[]) {
				const parts = (c: any[]) =>
					c.map((p) =>
						p.type === "image"
							? { type: "image_url", image_url: { url: `data:${p.mimeType};base64,${p.data}` } }
							: { type: "text", text: p.text },
					);
				if (m.role === "user") messages.push({ role: "user", content: typeof m.content === "string" ? m.content : parts(m.content) });
				else if (m.role === "assistant") {
					const calls = m.content.filter((c: any) => c.type === "toolCall");
					messages.push({
						role: "assistant",
						content: m.content.filter((c: any) => c.type === "text").map((c: any) => c.text).join("\n"),
						...(calls.length ? { tool_calls: calls.map((c: any) => ({ id: c.id, type: "function", function: { name: c.name, arguments: JSON.stringify(c.arguments) } })) } : {}),
					});
				} else if (m.role === "toolResult") {
					const text = m.content.filter((c: any) => c.type === "text").map((c: any) => c.text).join("\n") || "done";
					messages.push({ role: "tool", tool_call_id: m.toolCallId, content: text });
					const images = m.content.filter((c: any) => c.type === "image");
					if (images.length) messages.push({ role: "user", content: [{ type: "text", text: "The screen now:" }, ...parts(images)] });
				}
			}
			const body = {
				model: model.id,
				messages,
				tools: tools.map((t: any) => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } })),
				tool_choice: "auto",
			};
			const res = await fetch(`${model.baseUrl}/chat/completions`, {
				method: "POST",
				headers: { "content-type": "application/json", authorization: `Bearer ${options?.apiKey ?? ""}` },
				body: JSON.stringify(body),
				signal: options?.signal,
			});
			if (!res.ok) throw new Error(`the provider answered ${res.status}`);
			const json: any = await res.json();
			stream.push({ type: "start", partial: out });
			const msg = json.choices?.[0]?.message ?? {};
			if (msg.content) {
				out.content.push({ type: "text", text: msg.content });
				stream.push({ type: "text_start", contentIndex: 0, partial: out });
				stream.push({ type: "text_end", contentIndex: 0, content: msg.content, partial: out });
			}
			for (const c of msg.tool_calls ?? []) {
				const call = { type: "toolCall", id: c.id, name: c.function.name, arguments: JSON.parse(c.function.arguments || "{}") } as any;
				out.content.push(call);
				const i = out.content.length - 1;
				stream.push({ type: "toolcall_start", contentIndex: i, partial: out });
				stream.push({ type: "toolcall_end", contentIndex: i, toolCall: call, partial: out });
			}
			const u = json.usage ?? {};
			out.usage.input = u.prompt_tokens ?? 0;
			out.usage.cacheRead = u.prompt_tokens_details?.cached_tokens ?? 0;
			out.usage.output = u.completion_tokens ?? 0;
			out.usage.totalTokens = out.usage.input + out.usage.output;
			out.stopReason = msg.tool_calls?.length ? "toolUse" : "stop";
			send({ t: "turn", text: msg.content ?? "", usage: out.usage });
			stream.push({ type: "done", reason: out.stopReason, message: out });
		} catch (e: any) {
			out.stopReason = options?.signal?.aborted ? "aborted" : "error";
			out.errorMessage = String(e?.message ?? e);
			stream.push({ type: "error", reason: out.stopReason, error: out });
		}
		stream.end();
	})();
	return stream;
}

// One Kyokai action per tool call, executed by the engine; the screenshot
// comes back as an image result, unchanged.
const action = (name: string, description: string, parameters: any) => ({
	name,
	label: name,
	description,
	parameters,
	executionMode: "sequential" as const,
	async execute(id: string, args: any, signal?: AbortSignal) {
		if (signal?.aborted) throw new Error("aborted");
		const r = await ask(id, { t: "act", id, name, args });
		const content: any[] = [];
		if (r.text) content.push({ type: "text", text: r.text });
		if (r.shot) content.push({ type: "image", data: r.shot, mimeType: "image/png" });
		return { content, details: undefined };
	},
});
const point = Type.Array(Type.Integer(), { minItems: 2, maxItems: 2, description: "[x, y] in screenshot pixels" });
const tools = [
	action("left_click", "Click the left mouse button.", Type.Object({ coordinate: point })),
	action("screenshot", "Take a screenshot of the screen.", Type.Object({})),
];

const t0 = performance.now();
send({ t: "hello", v: 1, pid: process.pid });
const start = await started;

const modelRuntime = await ModelRuntime.create({
	credentials: undefined as any,
	modelsPath: null,
	allowModelNetwork: false,
	refreshOnCreate: false,
} as any);
modelRuntime.registerProvider("kyokai-chat", {
	baseUrl: start.url,
	apiKey: "from-the-engine",
	api: "kyokai-chat" as any,
	models: [{ id: start.model, name: start.model, reasoning: false, input: ["text", "image"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 200000, maxTokens: 8000 }],
	streamSimple: chatStream as any,
} as any);
const model = modelRuntime.getModel("kyokai-chat", start.model);
if (!model) throw new Error("model not registered");

const resourceLoader: ResourceLoader = {
	getExtensions: () => ({ extensions: [], errors: [], runtime: createExtensionRuntime() }),
	getSkills: () => ({ skills: [], diagnostics: [] }),
	getPrompts: () => ({ prompts: [], diagnostics: [] }),
	getThemes: () => ({ themes: [], diagnostics: [] }),
	getAgentsFiles: () => ({ agentsFiles: [] }),
	getSystemPrompt: () => start.system,
	getSystemPromptSource: () => undefined,
	getAppendSystemPrompt: () => [],
	getAppendSystemPromptSources: () => [],
	extendResources: () => {},
	reload: async () => {},
} as any;

const cwd = process.cwd();
const { session } = await createAgentSession({
	cwd,
	agentDir: cwd,
	model,
	thinkingLevel: "off",
	modelRuntime,
	resourceLoader,
	// Only Kyokai's actions: an allow-list of exactly them (`noTools: "all"`
	// removes custom tools too).
	tools: tools.map((t) => t.name),
	customTools: tools as any,
	sessionManager: SessionManager.inMemory(cwd),
	settingsManager: SettingsManager.inMemory({
		compaction: { enabled: false },
		retry: { enabled: false },
		images: { autoResize: false, blockImages: false },
		cacheWarming: "off",
		enableInstallTelemetry: false,
	} as any),
} as any);
// The key comes over the pipe for each request, never from env or files.
(session.agent as any).getApiKey = async () => (await ask("key", { t: "key?" })).key;
aborter = () => session.agent.abort();
send({ t: "ready", ms: Math.round(performance.now() - t0), rss: process.memoryUsage().rss });

try {
	await session.prompt(start.instruction, {
		expandPromptTemplates: false,
		images: [{ type: "image", data: start.shot, mimeType: "image/png" }],
	} as any);
	const last: any = session.messages.at(-1);
	send({ t: last?.stopReason === "aborted" ? "aborted" : "done", text: last?.content?.find((c: any) => c.type === "text")?.text ?? "", rss: process.memoryUsage().rss });
} catch (e: any) {
	send({ t: "failed", error: String(e?.message ?? e) });
}
session.dispose();
process.exit(0);
