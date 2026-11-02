// The sidecar as a process: a stand-in engine and provider drive one task
// through it (doc/en-us/architecture.md, "The agent sidecar"): the order of
// messages, the key only over the pipe, the answer carrying the outcomes
// and the screen after the batch, abort during an action, and nothing
// contacted but the provider.

import { expect, test } from "bun:test";
import { join } from "node:path";

const KEY = "sk-test-not-a-real-key";
const SHOT = { b64: "U0hPVDA=", width: 1280, height: 720 };
const AFTER = { b64: "U0hPVDE=", width: 1280, height: 720 };

function provider(script: unknown[]) {
	const bodies: any[] = [];
	const auth: (string | null)[] = [];
	const server = Bun.serve({
		port: 0,
		async fetch(req) {
			bodies.push(await req.json());
			auth.push(req.headers.get("authorization"));
			return Response.json(script[Math.min(bodies.length - 1, script.length - 1)]);
		},
	});
	return { url: `http://127.0.0.1:${server.port}/v1`, bodies, auth, stop: () => server.stop(true) };
}

const chatCall = (calls: [string, string, unknown][]) => ({
	choices: [
		{
			finish_reason: "tool_calls",
			message: { role: "assistant", content: "Working.", tool_calls: calls.map(([id, name, args]) => ({ id, type: "function", function: { name, arguments: JSON.stringify(args) } })) },
		},
	],
	usage: { prompt_tokens: 100, prompt_tokens_details: { cached_tokens: 80 }, completion_tokens: 10 },
});
const chatDone = { choices: [{ finish_reason: "stop", message: { role: "assistant", content: "Saved." } }], usage: { prompt_tokens: 200, completion_tokens: 3 } };

/** Runs the sidecar against `url`; `onAct` answers each action. */
async function drive(url: string, onAct: (m: any, write: (m: unknown) => void) => Promise<void> | void, start: object = {}) {
	const p = Bun.spawn(["bun", join(import.meta.dir, "../src/main.ts")], { stdin: "pipe", stdout: "pipe", stderr: "pipe" });
	const write = (m: unknown) => {
		p.stdin.write(`${JSON.stringify(m)}\n`);
		p.stdin.flush();
	};
	const seen: any[] = [];
	const reader = p.stdout.getReader();
	let buf = "";
	for (;;) {
		const { value, done } = await reader.read();
		if (done) break;
		buf += new TextDecoder().decode(value);
		for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
			const m = JSON.parse(buf.slice(0, i));
			buf = buf.slice(i + 1);
			seen.push(m);
			if (m.t === "hello") write({ t: "start", task: 1, protocol: "openai-chat", url, model: "m", system: "sys", instruction: "Save the file", shot: SHOT, tools: [], ...start });
			else if (m.t === "key?") write({ t: "key", key: KEY });
			else if (m.t === "act") await onAct(m, write);
			else if (m.t === "done" || m.t === "failed" || m.t === "aborted") p.stdin.end();
		}
	}
	await p.exited;
	return seen;
}

test("a task runs through the sidecar, every action through the engine", async () => {
	const prov = provider([chatCall([["c1", "left_click", { coordinate: [640, 425] }], ["c2", "key", { text: "ctrl+s" }]]), chatDone]);
	const seen = await drive(prov.url, (m, write) =>
		write({ t: "result", id: m.id, outcome: m.last ? { kind: "failed", error: "no" } : { kind: "ok" }, shot: m.last ? AFTER : undefined }),
	);
	prov.stop();
	expect(seen.map((m) => m.t)).toEqual(["hello", "key?", "turn", "act", "act", "key?", "turn", "state", "done"]);
	expect(seen[0].v).toBe(2);
	const acts = seen.filter((m) => m.t === "act");
	expect(acts.map((a) => [a.name, a.input, a.last])).toEqual([
		["left_click", { coordinate: [640, 425] }, false],
		["key", { text: "ctrl+s" }, true],
	]);
	expect(seen[2].usage).toEqual({ input: 100, cached: 80, output: 10 });
	expect(prov.auth).toEqual([`Bearer ${KEY}`, `Bearer ${KEY}`]);
	// The answer: each call's outcome, then the screen after the batch.
	const answer = prov.bodies[1].messages;
	expect(answer.filter((m: any) => m.role === "tool").map((m: any) => m.content)).toEqual(["done", "error: no"]);
	expect(answer.at(-1).content.at(-1).image_url.url).toBe(`data:image/png;base64,${AFTER.b64}`);
	expect(seen.at(-1)).toEqual({ t: "done", text: "Saved." });
	// The conversation it kept: every screenshot replaced by a note.
	const kept = JSON.stringify(seen.find((m) => m.t === "state").state);
	expect(kept).not.toContain(SHOT.b64);
	expect(kept).not.toContain(AFTER.b64);
	expect(kept).toContain("an earlier screenshot, not kept");
});

test("a kept conversation continues with the reply and a fresh screen", async () => {
	const prov = provider([chatDone]);
	const resume = { messages: [{ role: "system", content: "old" }, { role: "user", content: [{ type: "text", text: "Open the file" }, { type: "text", text: "[an earlier screenshot, not kept]" }] }, { role: "assistant", content: "Opened." }] };
	const seen = await drive(prov.url, () => {}, { instruction: "Now save it", resume });
	prov.stop();
	expect(seen.map((m) => m.t)).toEqual(["hello", "key?", "turn", "state", "done"]);
	const sent = prov.bodies[0].messages;
	// Today's system prompt, the kept history, then the reply with the screen.
	expect(sent[0]).toEqual({ role: "system", content: "sys" });
	expect(sent.slice(1, 3)).toEqual(resume.messages.slice(1));
	expect(sent[3].content[0].text).toBe("Now save it");
	expect(sent[3].content[1].image_url.url).toBe(`data:image/png;base64,${SHOT.b64}`);
});

test("an unreadable kept conversation fails the task", async () => {
	const prov = provider([chatDone]);
	const seen = await drive(prov.url, () => {}, { resume: { nothing: true } });
	prov.stop();
	expect(prov.bodies.length).toBe(0);
	expect(seen.at(-1)?.t).not.toBe("done");
});

test("an abort during an action ends the task with nothing more sent", async () => {
	const prov = provider([chatCall([["c1", "left_click", { coordinate: [1, 1] }]]), chatDone]);
	const seen = await drive(prov.url, async (m, write) => {
		write({ t: "abort" });
		await Bun.sleep(200);
		write({ t: "result", id: m.id, outcome: { kind: "ok" } });
	});
	prov.stop();
	expect(prov.bodies.length).toBe(1);
	expect(seen.at(-1).t).toBe("aborted");
});
