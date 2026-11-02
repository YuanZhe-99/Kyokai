// kyokai-agent: Pi's agent loop for one AI task, driven by Kyokai's engine
// over JSON lines (doc/en-us/architecture.md, "The agent sidecar"). The
// engine starts it per task and stays the only thing that acts: every
// action goes to the engine (`act`) and its outcome comes back (`result`).
// Pi runs with everything of its own off: sessions and settings in memory,
// no model catalog, cache warming, attribution, compaction or retries of
// its own, images never resized, no built-in tools; the provider requests
// are Kyokai's (conversation.ts).

import { type AssistantMessage, createAssistantMessageEventStream, type SimpleStreamOptions } from "@earendil-works/pi-ai";
import {
	createAgentSession,
	createExtensionRuntime,
	ModelRuntime,
	type ResourceLoader,
	SessionManager,
	SettingsManager,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { CLAUDE_BETAS, CLAUDE_VERSION, type Conversation, open, type Outcome, type Shot, type Turn } from "./conversation";

/** The protocol version this sidecar speaks. */
export const VERSION = 1;

const send = (m: unknown) => process.stdout.write(`${JSON.stringify(m)}\n`);

// ---- The engine's lines ----

type Start = {
	task: number;
	protocol: "anthropic" | "openai-responses" | "openai-chat";
	url: string;
	model: string;
	system: string;
	instruction: string;
	shot: Shot;
	tools: unknown;
};
type Result = { id: string; outcome: Outcome; shot?: Shot; acknowledged?: boolean };

let onStart: (m: Start) => void = () => {};
let onSteer: (text: string) => void = () => {};
let onFollowUp: (text: string, shot: Shot) => void = () => {};
let onAbort: () => void = () => {};
const waiting = new Map<string, (m: any) => void>();

let buf = "";
process.stdin.on("data", (d) => {
	buf += d.toString();
	for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
		const line = buf.slice(0, i);
		buf = buf.slice(i + 1);
		if (!line.trim()) continue;
		const m = JSON.parse(line);
		switch (m.t) {
			case "start":
				onStart(m);
				break;
			case "steer":
				onSteer(m.text);
				break;
			case "follow-up":
				onFollowUp(m.text, m.shot);
				break;
			case "abort":
				onAbort();
				break;
			case "key":
				waiting.get("key")?.(m);
				break;
			case "result":
				waiting.get(`r:${m.id}`)?.(m);
				break;
		}
	}
});
// The engine gone: the sidecar ends with it, keeping nothing, so no task
// resumes on its own.
process.stdin.on("end", () => process.exit(0));

const ask = <T>(key: string, msg: unknown) =>
	new Promise<T>((resolve) => {
		waiting.set(key, (m) => {
			waiting.delete(key);
			resolve(m);
		});
		send(msg);
	});

// ---- Requests to the provider ----

const RETRIES = 3;
const TIMEOUT_MS = 300_000;
const backoff = (attempt: number, retryAfter?: number) => (retryAfter !== undefined ? Math.min(retryAfter, 30) : 2 ** attempt) * 1000;

/** Sends one request, retrying overload and server errors, as the engine's
 * client did; errors never carry the key. */
async function post(start: Start, body: unknown, key: string | null, signal?: AbortSignal): Promise<any> {
	const path = start.protocol === "anthropic" ? "/messages" : start.protocol === "openai-responses" ? "/responses" : "/chat/completions";
	const url = `${start.url.replace(/\/+$/, "")}${path}`;
	const headers: Record<string, string> = { "content-type": "application/json" };
	if (start.protocol === "anthropic") {
		headers["anthropic-version"] = CLAUDE_VERSION;
		headers["anthropic-beta"] = CLAUDE_BETAS;
		if (key) headers["x-api-key"] = key;
	} else if (key) headers.authorization = `Bearer ${key}`;
	for (let attempt = 0; ; attempt++) {
		const timeout = AbortSignal.timeout(TIMEOUT_MS);
		let res: Response;
		try {
			res = await fetch(url, { method: "POST", headers, body: JSON.stringify(body), signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
		} catch (e: any) {
			if (signal?.aborted) throw e;
			if (timeout.aborted) throw new Error("the provider did not answer in time");
			if (attempt < RETRIES) {
				await Bun.sleep(backoff(attempt + 1));
				continue;
			}
			throw new Error("the provider could not be reached");
		}
		const doc: any = await res.json().catch(() => null);
		if (res.ok) return doc;
		if ([408, 429, 500, 502, 503, 504, 529].includes(res.status) && attempt < RETRIES) {
			const after = Number(res.headers.get("retry-after"));
			await Bun.sleep(backoff(attempt + 1, Number.isFinite(after) && after > 0 ? after : undefined));
			continue;
		}
		if (res.status === 401 || res.status === 403) throw new Error("the provider did not accept the API key");
		throw new Error(`the provider answered ${res.status}: ${doc?.error?.message ?? ""}`);
	}
}

// ---- The task ----

const start = await new Promise<Start>((resolve) => {
	onStart = resolve;
	send({ t: "hello", v: VERSION });
});

const conv: Conversation = open(start.protocol, start.model, start.system, start.instruction, start.shot, start.tools);
/** Whether the last turn waits for its answer, which the next request carries. */
let answered = true;
/** The outcomes of the last turn's actions, by call id, from the engine. */
const outcomes = new Map<string, Result>();
let lastCalls: string[] = [];
let followUpShot: Shot | undefined;
let lastTurn: Turn | undefined;

/** The provider stream Pi's loop calls for each model turn: it answers the
 * last turn (or adds the follow-up), sends the conversation's request, and
 * hands Pi the actions as calls of the one `act` tool. */
function stream(model: any, _context: unknown, options?: SimpleStreamOptions) {
	const out = createAssistantMessageEventStream();
	(async () => {
		const msg: AssistantMessage = {
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
			if (!answered) {
				const results = lastCalls.map((id) => outcomes.get(id));
				const after = [...results].reverse().find((r) => r?.shot)?.shot;
				conv.answer(
					results.map((r) => r?.outcome ?? { kind: "not-executed" }),
					after,
					results.some((r) => r?.acknowledged),
				);
				answered = true;
				outcomes.clear();
			} else if (followUpShot) {
				// A reply after the task finished: Pi has the text; the
				// conversation takes it with the screen as it is now.
				const last: any = _context && (_context as any).messages?.at(-1);
				const text = typeof last?.content === "string" ? last.content : (last?.content?.find((c: any) => c.type === "text")?.text ?? "");
				conv.followUp(text, followUpShot);
				followUpShot = undefined;
			}
			const key = (await ask<{ key: string | null }>("key", { t: "key?" })).key;
			const response = await post(start, conv.body(), key, options?.signal);
			const turn = conv.accept(response);
			lastTurn = turn;
			send({ t: "turn", text: turn.text, progress: turn.progress, flagged: turn.flagged, usage: turn.usage });
			msg.usage.input = turn.usage.input;
			msg.usage.cacheRead = turn.usage.cached;
			msg.usage.output = turn.usage.output;
			msg.usage.totalTokens = turn.usage.input + turn.usage.output;
			out.push({ type: "start", partial: msg });
			if (turn.text) {
				msg.content.push({ type: "text", text: turn.text });
				out.push({ type: "text_start", contentIndex: 0, partial: msg });
				out.push({ type: "text_end", contentIndex: 0, content: turn.text, partial: msg });
			}
			lastCalls = turn.calls.map((c) => c.id);
			turn.calls.forEach((c, i) => {
				const call = { type: "toolCall", id: c.id, name: "act", arguments: { name: c.name, input: c.input, last: i === turn.calls.length - 1 } } as any;
				msg.content.push(call);
				const index = msg.content.length - 1;
				out.push({ type: "toolcall_start", contentIndex: index, partial: msg });
				out.push({ type: "toolcall_end", contentIndex: index, toolCall: call, partial: msg });
			});
			answered = turn.calls.length === 0;
			if (typeof turn.end === "object") {
				msg.stopReason = "error";
				msg.errorMessage = "refused" in turn.end ? `the model declined the task${turn.end.refused ? `: ${turn.end.refused}` : ""}` : `the model's answer was cut off (${turn.end.incomplete})`;
				out.push({ type: "error", reason: "error", error: msg });
			} else {
				msg.stopReason = turn.calls.length ? "toolUse" : "stop";
				out.push({ type: "done", reason: msg.stopReason, message: msg });
			}
		} catch (e: any) {
			msg.stopReason = options?.signal?.aborted ? "aborted" : "error";
			msg.errorMessage = String(e?.message ?? e);
			out.push({ type: "error", reason: msg.stopReason, error: msg });
		}
		out.end();
	})();
	return out;
}

/** Kyokai's one tool: hands each action to the engine and waits for its
 * outcome. Sequential, so actions run in the model's order. */
const act = {
	name: "act",
	label: "act",
	description: "One computer action, run by the host.",
	parameters: Type.Object({}, { additionalProperties: true }),
	executionMode: "sequential" as const,
	async execute(id: string, args: any, signal?: AbortSignal) {
		if (signal?.aborted) throw new Error("aborted");
		const r = await ask<Result>(`r:${id}`, { t: "act", id, name: args.name, input: args.input, last: Boolean(args.last) });
		outcomes.set(id, r);
		const content: any[] = [{ type: "text", text: r.outcome.kind }];
		return { content, details: undefined };
	},
};

const modelRuntime = await ModelRuntime.create({ credentials: undefined, modelsPath: null, allowModelNetwork: false, refreshOnCreate: false } as any);
modelRuntime.registerProvider("kyokai", {
	baseUrl: start.url,
	apiKey: "from-the-engine",
	api: "kyokai" as any,
	models: [{ id: start.model, name: start.model, reasoning: true, input: ["text", "image"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1_000_000, maxTokens: 16_000 }],
	streamSimple: stream as any,
} as any);
const model = modelRuntime.getModel("kyokai", start.model);
if (!model) throw new Error("the model is not registered");

const resourceLoader = {
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
} as unknown as ResourceLoader;

const cwd = process.cwd();
const { session } = await createAgentSession({
	cwd,
	agentDir: cwd,
	model,
	thinkingLevel: "off",
	modelRuntime,
	resourceLoader,
	tools: ["act"],
	customTools: [act] as any,
	sessionManager: SessionManager.inMemory(cwd),
	settingsManager: SettingsManager.inMemory({
		compaction: { enabled: false },
		retry: { enabled: false },
		images: { autoResize: false, blockImages: false },
		cacheWarming: "off",
		enableInstallTelemetry: false,
	} as any),
} as any);
// Whether the engine stopped the run in progress.
let aborted = false;
onAbort = () => {
	aborted = true;
	session.agent.abort();
};
onSteer = (text) => (session as any).steer?.(text);

/** Runs one prompt to its end and tells the engine how it ended. */
async function run(text: string) {
	aborted = false;
	try {
		await session.prompt(text, { expandPromptTemplates: false } as any);
		const last: any = session.messages.at(-1);
		if (aborted || last?.stopReason === "aborted") send({ t: "aborted" });
		else if (last?.stopReason === "error") send({ t: "failed", error: last.errorMessage ?? "the task failed" });
		else send({ t: "done", text: lastTurn?.text ?? "" });
	} catch (e: any) {
		send(aborted ? { t: "aborted" } : { t: "failed", error: String(e?.message ?? e) });
	}
}

onFollowUp = (text, shot) => {
	followUpShot = shot;
	void run(text);
};
await run(start.instruction);
