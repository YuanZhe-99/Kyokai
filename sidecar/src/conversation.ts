// The providers' conversations, as Kyokai sends them (doc/en-us/
// computer-use.md, "Providers"): each keeps the provider's own request and
// only appends to it, so the prefix a provider caches never changes. They
// build the same requests as the engine's adapters did (core/kyokai-core/
// src/ai/{claude,openai,chat}.rs; the golden test holds both to the same
// bodies). Actions are not parsed here: the engine parses and checks them.

/** A screenshot on the wire: a base64 PNG and its size. */
export interface Shot {
	b64: string;
	width: number;
	height: number;
}

/** What became of one action, as the engine answers it. */
export type Outcome =
	| { kind: "ok" }
	| { kind: "image"; shot: Shot }
	| { kind: "text"; text: string }
	| { kind: "failed"; error: string }
	| { kind: "not-executed" }
	| { kind: "paused" };

/** One proposed action: a toolset member, or OpenAI's action object. */
export interface Call {
	id: string;
	name: string;
	input: unknown;
}

export interface Usage {
	input: number;
	cached: number;
	output: number;
}

/** A model turn: its actions in order and how it ended. */
export interface Turn {
	calls: Call[];
	text: string;
	progress: string[];
	flagged: string[];
	usage: Usage;
	/** "actions", "finished", or why it stopped ("refused: …", "incomplete: …"). */
	end: "actions" | "finished" | { refused: string } | { incomplete: string };
}

export type Protocol = "anthropic" | "openai-responses" | "openai-chat";

export interface Conversation {
	readonly protocol: Protocol;
	body(): unknown;
	accept(response: any): Turn;
	/** Answers the last turn; `after` is the screen after the batch. */
	answer(outcomes: Outcome[], after: Shot | undefined, acknowledged: boolean): void;
	followUp(text: string, shot: Shot): void;
	/** What a later sidecar needs to continue the conversation (`restore`):
	 * the provider's own history with every screenshot replaced by a note, or
	 * for OpenAI the last response's id. Taken between runs, never mid-turn. */
	state(): unknown;
	/** The request that asks the model, with its tools off, to summarise the
	 * conversation (compaction); the conversation itself is unchanged. */
	summarize(prompt: string): unknown;
}

/** What compaction asks the model. */
export const SUMMARIZE =
	"Summarize this task so far, so that it can continue later without the history: what the user asked, " +
	"what was done and found, where things stand on the screen, and what is left to do. Plain text, at most " +
	"300 words. Do not act.";

/** A conversation compaction replaced: its summary begins the next one. */
export const summarized = (summary: string, instruction: string) => `Earlier in this task, summarized:\n${summary}\n\nNow: ${instruction}`;

/** What an earlier screenshot becomes in a kept conversation. */
export const GONE = "[an earlier screenshot, not kept]";

const num = (v: any) => (typeof v === "number" ? v : 0);
const at = (v: any, path: string) => path.split(".").reduce((o, k) => o?.[k], v);

// ---- Claude: Messages API with the computer toolset ----

export const CLAUDE_VERSION = "2023-06-01";
export const CLAUDE_BETAS =
	"thinking-display-updates-2026-08-18,server-side-fallback-2026-07-01,context-management-2025-06-27";
const TOOLSET = "computer";
const NOT_EXECUTED = "Not executed: an earlier computer action in this turn failed.";
const PAUSED = "Not executed: the host user paused the task. Take a screenshot before continuing.";
const CLEAR_AT_LEAST = 20_000;
const cache = () => ({ type: "ephemeral", ttl: "1h" });
const claudeImage = (b64: string) => ({ type: "image", source: { type: "base64", media_type: "image/png", data: b64 } });

class Claude implements Conversation {
	readonly protocol = "anthropic";
	private request: any;
	private pending: string[] = [];
	constructor(model: string, system: string, task: string, shot: Shot) {
		this.request = {
			model,
			max_tokens: 16000,
			thinking: { type: "adaptive", display: "updates" },
			output_config: { effort: "high" },
			fallbacks: "default",
			system: [{ type: "text", text: system, cache_control: cache() }],
			cache_control: cache(),
			tools: [{ type: "computer_toolset_20260801" }],
			context_management: {
				edits: [
					{
						type: "clear_tool_uses_20250919",
						trigger: { type: "input_tokens", value: 100000 },
						keep: { type: "tool_uses", value: 5 },
						clear_at_least: { type: "input_tokens", value: CLEAR_AT_LEAST },
					},
				],
			},
			messages: [{ role: "user", content: [claudeImage(shot.b64), { type: "text", text: task }] }],
		};
	}
	body() {
		return this.request;
	}
	accept(response: any): Turn {
		if (!Array.isArray(response?.content)) throw new Error("missing field content");
		this.request.messages.push({ role: "assistant", content: response.content });
		const turn: Turn = { calls: [], text: "", progress: [], flagged: [], usage: { input: 0, cached: 0, output: 0 }, end: "finished" };
		for (const b of response.content) {
			if (b.type === "tool_use" && b.toolset_name === TOOLSET) turn.calls.push({ id: b.id, name: b.name, input: b.input ?? {} });
			else if (b.type === "thinking" && b.thinking) turn.progress.push(b.thinking);
			else if (b.type === "text") turn.text += b.text ?? "";
		}
		this.pending = turn.calls.map((c) => c.id);
		const u = response.usage ?? {};
		turn.usage = {
			input: num(u.input_tokens) + num(u.cache_read_input_tokens) + num(u.cache_creation_input_tokens),
			cached: num(u.cache_read_input_tokens),
			output: num(u.output_tokens),
		};
		const stop = response.stop_reason ?? "";
		turn.end = turn.calls.length
			? "actions"
			: stop === "refusal"
				? { refused: response.stop_details?.explanation ?? "" }
				: stop === "max_tokens" || stop === "model_context_window_exceeded"
					? { incomplete: stop }
					: "finished";
		return turn;
	}
	answer(outcomes: Outcome[]) {
		const results = this.pending.map((id, i) => toolResult(id, outcomes[i] ?? { kind: "not-executed" }));
		this.pending = [];
		this.request.messages.push({ role: "user", content: results });
	}
	followUp(text: string, shot: Shot) {
		this.request.messages.push({ role: "user", content: [claudeImage(shot.b64), { type: "text", text }] });
	}
	state() {
		// Thinking is dropped: its signature binds it to the exact history it
		// came from, which no longer holds its screenshots.
		const gone = (b: any): any =>
			b?.type === "image" ? { type: "text", text: GONE } : b?.type === "tool_result" && Array.isArray(b.content) ? { ...b, content: b.content.map(gone) } : b;
		return {
			messages: this.request.messages.map((m: any) => ({
				role: m.role,
				content: Array.isArray(m.content) ? m.content.filter((b: any) => b.type !== "thinking" && b.type !== "redacted_thinking").map(gone) : m.content,
			})),
		};
	}
	summarize(prompt: string) {
		const body = structuredClone(this.request);
		body.messages.push({ role: "user", content: [{ type: "text", text: prompt }] });
		body.tool_choice = { type: "none" };
		return body;
	}
	static restore(model: string, system: string, state: any): Claude {
		const c = new Claude(model, system, "", { b64: "", width: 0, height: 0 });
		c.request.messages = structuredClone(state.messages);
		return c;
	}
}

function toolResult(id: string, o: Outcome) {
	const [content, error]: [unknown, boolean] =
		o.kind === "ok"
			? [[{ type: "text", text: "OK" }], false]
			: o.kind === "image"
				? [[claudeImage(o.shot.b64)], false]
				: o.kind === "text"
					? [[{ type: "text", text: o.text }], false]
					: o.kind === "failed"
						? [[{ type: "text", text: o.error }], true]
						: o.kind === "not-executed"
							? [NOT_EXECUTED, true]
							: [PAUSED, true];
	const r: any = { type: "tool_result", tool_use_id: id, toolset_name: TOOLSET, content };
	if (error) r.is_error = true;
	return r;
}

// ---- OpenAI: Responses API with the computer tool ----

const dataUrl = (b64: string) => `data:image/png;base64,${b64}`;

class OpenAI implements Conversation {
	readonly protocol = "openai-responses";
	private previous: string | undefined;
	private input: any[];
	private pending: { callId: string; n: number; checks: any[] }[] = [];
	constructor(
		private model: string,
		private system: string,
		task: string,
		shot: Shot,
	) {
		this.input = [
			{
				role: "user",
				content: [
					{ type: "input_text", text: task },
					{ type: "input_image", image_url: dataUrl(shot.b64), detail: "high" },
				],
			},
		];
	}
	body() {
		const body: any = {
			model: this.model,
			instructions: this.system,
			tools: [{ type: "computer" }],
			truncation: "disabled",
			input: this.input,
		};
		if (this.previous) body.previous_response_id = this.previous;
		return body;
	}
	accept(response: any): Turn {
		if (!Array.isArray(response?.output)) throw new Error("missing field output");
		this.previous = response.id;
		this.input = [];
		this.pending = [];
		const turn: Turn = { calls: [], text: "", progress: [], flagged: [], usage: { input: 0, cached: 0, output: 0 }, end: "finished" };
		let refusal: string | undefined;
		for (const item of response.output) {
			if (item.type === "computer_call") {
				const actions: any[] = Array.isArray(item.actions) && item.actions.length ? item.actions : item.action ? [item.action] : [];
				const checks: any[] = Array.isArray(item.pending_safety_checks) ? item.pending_safety_checks : [];
				for (const c of checks) turn.flagged.push(c.message ?? c.code ?? "");
				this.pending.push({ callId: item.call_id, n: actions.length, checks });
				actions.forEach((a, i) => turn.calls.push({ id: `${item.call_id}#${i}`, name: "openai", input: a }));
			} else if (item.type === "message") {
				for (const part of item.content ?? []) {
					if (typeof part.text === "string") turn.text += part.text;
					if (typeof part.refusal === "string") refusal = part.refusal;
				}
			}
		}
		const u = response.usage ?? {};
		turn.usage = { input: num(u.input_tokens), cached: num(at(u, "input_tokens_details.cached_tokens")), output: num(u.output_tokens) };
		const incomplete = response.incomplete_details?.reason;
		turn.end = turn.calls.length ? "actions" : refusal !== undefined ? { refused: refusal } : incomplete ? { incomplete } : "finished";
		return turn;
	}
	answer(outcomes: Outcome[], after: Shot | undefined, acknowledged: boolean) {
		const notes: string[] = [];
		let start = 0;
		for (const { callId, n, checks } of this.pending) {
			for (let k = start; k < start + n && k < outcomes.length; k++) {
				const o = outcomes[k];
				const i = k + 1;
				if (o.kind === "failed") notes.push(`Action ${i} failed: ${o.error}`);
				else if (o.kind === "not-executed") notes.push(`Action ${i} was not executed.`);
				else if (o.kind === "paused") notes.push(`Action ${i} was not executed: the host user paused the task.`);
				else if (o.kind === "text") notes.push(`Action ${i}: ${o.text}`);
			}
			start += n;
			const item: any = {
				type: "computer_call_output",
				call_id: callId,
				output: { type: "computer_screenshot", image_url: dataUrl(after?.b64 ?? "") },
			};
			if (acknowledged && checks.length) item.acknowledged_safety_checks = checks;
			this.input.push(item);
		}
		this.pending = [];
		if (notes.length) this.input.push({ role: "user", content: [{ type: "input_text", text: notes.join("\n") }] });
	}
	followUp(text: string, shot: Shot) {
		this.input.push({
			role: "user",
			content: [
				{ type: "input_text", text },
				{ type: "input_image", image_url: dataUrl(shot.b64), detail: "high" },
			],
		});
	}
	state() {
		// The provider keeps the history; the last response names it.
		return { previous: this.previous };
	}
	summarize(prompt: string) {
		const body: any = this.body();
		body.input = [...body.input, { role: "user", content: [{ type: "input_text", text: prompt }] }];
		body.tool_choice = "none";
		return body;
	}
	static restore(model: string, system: string, state: any): OpenAI {
		const c = new OpenAI(model, system, "", { b64: "", width: 0, height: 0 });
		c.previous = typeof state.previous === "string" ? state.previous : undefined;
		c.input = [];
		return c;
	}
}

// ---- OpenAI-compatible chat completions with Kyokai's function tools ----

const KEEP_IMAGES = 3;
const DROP_BATCH = 5;
const chatImage = (b64: string) => ({ type: "image_url", image_url: { url: `data:image/png;base64,${b64}` } });

/** How many of `total` screenshots, oldest first, a request leaves out. */
export function omitted(total: number): number {
	if (total <= KEEP_IMAGES + DROP_BATCH) return 0;
	return Math.floor((total - KEEP_IMAGES) / DROP_BATCH) * DROP_BATCH;
}

class Chat implements Conversation {
	readonly protocol = "openai-chat";
	private messages: any[];
	private calls: string[] = [];
	constructor(
		private model: string,
		system: string,
		task: string,
		shot: Shot,
		private tools: unknown,
	) {
		this.messages = [
			{ role: "system", content: system },
			{ role: "user", content: [{ type: "text", text: task }, chatImage(shot.b64)] },
		];
	}
	body() {
		const messages = structuredClone(this.messages);
		const total = messages.flatMap((m: any) => (Array.isArray(m.content) ? m.content : [])).filter((p: any) => p.type === "image_url").length;
		const omit = omitted(total);
		let seen = 0;
		for (const m of messages) {
			if (!Array.isArray(m.content)) continue;
			for (let i = 0; i < m.content.length; i++) {
				if (m.content[i].type === "image_url" && ++seen <= omit) m.content[i] = { type: "text", text: "[an earlier screenshot, omitted]" };
			}
		}
		return { model: this.model, messages, tools: this.tools, tool_choice: "auto" };
	}
	accept(response: any): Turn {
		const choice = response?.choices?.[0];
		const message = choice?.message;
		if (!message || typeof message !== "object") throw new Error("missing field choices[0].message");
		const text =
			typeof message.content === "string"
				? message.content
				: Array.isArray(message.content)
					? message.content.map((p: any) => p.text).filter((t: any) => typeof t === "string").join("\n")
					: "";
		const turn: Turn = { calls: [], text, progress: [], flagged: [], usage: { input: 0, cached: 0, output: 0 }, end: "finished" };
		this.calls = [];
		for (const call of Array.isArray(message.tool_calls) ? message.tool_calls : []) {
			const id = call.id ?? "";
			let args: unknown = call.function?.arguments;
			if (typeof args === "string") {
				try {
					args = args.trim() ? JSON.parse(args) : {};
				} catch {
					// The engine refuses an unreadable call as it refuses a bad one.
					args = { __unreadable: args };
				}
			}
			turn.calls.push({ id, name: call.function?.name ?? "", input: args ?? {} });
			this.calls.push(id);
		}
		const kept: any = { role: "assistant", content: text };
		if (Array.isArray(message.tool_calls)) kept.tool_calls = message.tool_calls;
		this.messages.push(kept);
		const u = response.usage ?? {};
		turn.usage = { input: num(u.prompt_tokens), cached: num(at(u, "prompt_tokens_details.cached_tokens")), output: num(u.completion_tokens) };
		turn.end = turn.calls.length
			? "actions"
			: choice.finish_reason === "length"
				? { incomplete: "the answer was cut off" }
				: choice.finish_reason === "content_filter"
					? { refused: text }
					: "finished";
		return turn;
	}
	answer(outcomes: Outcome[], after: Shot | undefined) {
		const images: any[] = [];
		this.calls.forEach((id, i) => {
			const o = outcomes[i] ?? { kind: "not-executed" };
			const content =
				o.kind === "ok"
					? "done"
					: o.kind === "image"
						? (images.push(chatImage(o.shot.b64)), "the image follows")
						: o.kind === "text"
							? o.text
							: o.kind === "failed"
								? `error: ${o.error}`
								: o.kind === "not-executed"
									? "not executed: an earlier action failed"
									: "not executed: the user paused the task";
			this.messages.push({ role: "tool", tool_call_id: id, content });
		});
		this.calls = [];
		images.push({ type: "text", text: "The screen now:" }, chatImage(after?.b64 ?? ""));
		this.messages.push({ role: "user", content: images });
	}
	followUp(text: string, shot: Shot) {
		this.messages.push({ role: "user", content: [{ type: "text", text }, chatImage(shot.b64)] });
	}
	state() {
		return {
			messages: this.messages.map((m: any) =>
				Array.isArray(m.content) ? { ...m, content: m.content.map((p: any) => (p.type === "image_url" ? { type: "text", text: GONE } : p)) } : m,
			),
		};
	}
	summarize(prompt: string) {
		const body: any = this.body();
		body.messages.push({ role: "user", content: prompt });
		body.tool_choice = "none";
		return body;
	}
	static restore(model: string, system: string, state: any, tools: unknown): Chat {
		const c = new Chat(model, system, "", { b64: "", width: 0, height: 0 }, tools);
		// The system prompt is today's; the rest is the kept history.
		c.messages = [{ role: "system", content: system }, ...structuredClone(state.messages).filter((m: any) => m.role !== "system")];
		return c;
	}
}

/** Continues a kept conversation with `instruction` and the screen now:
 * one compaction replaced (`{summary}`) starts afresh from its summary. */
export function resume(protocol: Protocol, model: string, system: string, state: any, instruction: string, shot: Shot, tools: unknown): Conversation {
	if (typeof state?.summary === "string") return open(protocol, model, system, summarized(state.summary, instruction), shot, tools);
	const conv = restore(protocol, model, system, state, tools);
	conv.followUp(instruction, shot);
	return conv;
}

/** A conversation kept by `state`, to continue with a follow-up.
 * Notes: the state comes from the engine, which got it from an earlier
 * sidecar; a malformed one fails the task. */
export function restore(protocol: Protocol, model: string, system: string, state: any, tools: unknown): Conversation {
	if (!state || typeof state !== "object" || (protocol !== "openai-responses" && !Array.isArray(state.messages))) throw new Error("the kept conversation is unreadable");
	switch (protocol) {
		case "anthropic":
			return Claude.restore(model, system, state);
		case "openai-responses":
			return OpenAI.restore(model, system, state);
		case "openai-chat":
			return Chat.restore(model, system, state, tools);
	}
}

/** A new conversation for a task. */
export function open(protocol: Protocol, model: string, system: string, task: string, shot: Shot, tools: unknown): Conversation {
	switch (protocol) {
		case "anthropic":
			return new Claude(model, system, task, shot);
		case "openai-responses":
			return new OpenAI(model, system, task, shot);
		case "openai-chat":
			return new Chat(model, system, task, shot, tools);
	}
}
