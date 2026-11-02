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
