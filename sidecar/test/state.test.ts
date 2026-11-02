// Kept conversations (conversation.ts, `state` and `restore`): what each
// provider's conversation keeps between runs, and that a restored one
// continues where it stood.

import { expect, test } from "bun:test";
import { GONE, open, restore } from "../src/conversation";

const SHOT = { b64: "U0hPVDA=", width: 1280, height: 720 };
const NEXT = { b64: "U0hPVDI=", width: 1280, height: 720 };

test("Claude keeps its messages without screenshots or thinking", () => {
	const c = open("anthropic", "claude-x", "sys", "Save the file", SHOT, null);
	c.accept({
		content: [
			{ type: "thinking", thinking: "plan", signature: "sig" },
			{ type: "tool_use", id: "t1", name: "screenshot", input: {}, toolset_name: "computer" },
		],
		stop_reason: "tool_use",
		usage: {},
	});
	c.answer([{ kind: "image", shot: NEXT }], undefined, false);
	c.accept({ content: [{ type: "text", text: "Saved." }], stop_reason: "end_turn", usage: {} });
	const state: any = c.state();
	const text = JSON.stringify(state);
	expect(text).not.toContain(SHOT.b64);
	expect(text).not.toContain(NEXT.b64);
	expect(text).not.toContain("thinking");
	expect(state.messages[0].content[0]).toEqual({ type: "text", text: GONE });
	expect(state.messages[2].content[0].content[0]).toEqual({ type: "text", text: GONE });

	const again = restore("anthropic", "claude-x", "sys", state, null);
	again.followUp("Now close it", SHOT);
	const body: any = again.body();
	expect(body.messages.slice(0, 4)).toEqual(state.messages);
	expect(body.messages[4].content[1]).toEqual({ type: "text", text: "Now close it" });
	expect(body.system[0].text).toBe("sys");
});

test("OpenAI keeps only the last response's id", () => {
	const c = open("openai-responses", "gpt-x", "sys", "Save the file", SHOT, null);
	c.accept({ id: "resp_1", output: [{ type: "message", content: [{ text: "Saved." }] }], usage: {} });
	expect(c.state()).toEqual({ previous: "resp_1" });
	const again = restore("openai-responses", "gpt-x", "sys", c.state(), null);
	again.followUp("Now close it", SHOT);
	const body: any = again.body();
	expect(body.previous_response_id).toBe("resp_1");
	expect(body.input.length).toBe(1);
	expect(body.input[0].content[0].text).toBe("Now close it");
});

test("thinking sets Claude's effort and OpenAI's reasoning, and a restore may change the model", () => {
	const usual: any = open("anthropic", "claude-x", "sys", "t", SHOT, null).body();
	expect(usual.output_config).toEqual({ effort: "high" });
	const claude: any = restore("anthropic", "claude-y", "sys", { messages: [] }, null, "low").body();
	expect([claude.model, claude.output_config.effort]).toEqual(["claude-y", "low"]);
	expect((open("openai-responses", "gpt-x", "sys", "t", SHOT, null).body() as any).reasoning).toBeUndefined();
	expect((open("openai-responses", "gpt-x", "sys", "t", SHOT, null, "max").body() as any).reasoning).toEqual({ effort: "high" });
	expect((open("openai-chat", "m", "sys", "t", SHOT, [], "max").body() as any).reasoning).toBeUndefined();
});

test("a chat conversation keeps its messages without screenshots", () => {
	const c = open("openai-chat", "m", "sys", "Save the file", SHOT, []);
	c.accept({ choices: [{ finish_reason: "stop", message: { role: "assistant", content: "Saved." } }] });
	const state: any = c.state();
	expect(JSON.stringify(state)).not.toContain(SHOT.b64);
	const again = restore("openai-chat", "m", "new sys", state, []);
	again.followUp("Now close it", SHOT);
	const body: any = again.body();
	expect(body.messages[0]).toEqual({ role: "system", content: "new sys" });
	expect(body.messages[1].content[1]).toEqual({ type: "text", text: GONE });
	expect(body.messages[3].content[0].text).toBe("Now close it");
});

test("element lists follow their screenshots and leave with them", () => {
	const LIST = 'Elements on screen (window "Save As"):\n1 button "Save" [10,10,40,20]';
	const listed = { ...NEXT, elements: LIST };
	const isList = (p: any) => p?.text === LIST || p?.text === LIST.replace("Save As", "First");
	const first = { ...SHOT, elements: LIST.replace("Save As", "First") };

	// Claude: beside the screenshot in the first message and in a tool result.
	const c = open("anthropic", "claude-x", "sys", "Save", first, null);
	expect(c.body()).toMatchObject({ messages: [{ content: [{ type: "image" }, { text: first.elements }, { text: "Save" }] }] });
	c.accept({ content: [{ type: "tool_use", id: "t1", name: "screenshot", input: {}, toolset_name: "computer" }], stop_reason: "tool_use", usage: {} });
	c.answer([{ kind: "image", shot: listed }], undefined, false);
	expect((c.body() as any).messages[2].content[0].content[1].text).toBe(LIST);
	expect(JSON.stringify(c.state())).not.toContain("Elements on screen");

	// OpenAI: after the screenshot output, as text.
	const o = open("openai-responses", "gpt-x", "sys", "Save", first, null);
	expect((o.body() as any).input[0].content[2]).toEqual({ type: "input_text", text: first.elements });
	o.accept({ id: "r1", output: [{ type: "computer_call", call_id: "c1", action: { type: "screenshot" } }] });
	o.answer([{ kind: "ok" }], listed, false);
	expect((o.body() as any).input[1]).toEqual({ role: "user", content: [{ type: "input_text", text: LIST }] });

	// Chat: after the screen, dropped with it, and gone from the kept state.
	const ch = open("openai-chat", "m", "sys", "Save", first, []);
	for (let i = 0; i < 9; i++) {
		ch.accept({ choices: [{ finish_reason: "tool_calls", message: { content: "", tool_calls: [{ id: `c${i}`, function: { name: "screenshot", arguments: "{}" } }] } }] });
		ch.answer([{ kind: "ok" }], listed, false);
	}
	const parts = (ch.body() as any).messages.flatMap((m: any) => (Array.isArray(m.content) ? m.content : []));
	const images = parts.filter((p: any) => p.type === "image_url").length;
	expect(parts.filter(isList).length).toBe(images);
	expect(JSON.stringify(ch.state())).not.toContain("Elements on screen");
});
