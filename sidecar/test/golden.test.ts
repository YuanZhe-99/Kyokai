// The sidecar's conversations build the same requests as the engine's Rust
// adapters did: the scenario and the bodies in core/kyokai-core/src/ai/
// fixtures/golden (written by the Rust test `golden_requests`).

import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { open, omitted, type Outcome, type Protocol } from "../src/conversation";

const fixtures = join(import.meta.dir, "../../core/kyokai-core/src/ai/fixtures");
const json = (rel: string) => JSON.parse(readFileSync(join(fixtures, rel), "utf8"));
const scenario = json("golden/scenario.json");
const shot = (key: string) => ({ b64: scenario[key], width: 1280, height: 720 });

for (const protocol of ["anthropic", "openai-responses", "openai-chat"] as Protocol[]) {
	test(`${protocol}: the same requests as the engine's adapter`, () => {
		const golden = json(`golden/${protocol}.json`);
		const r = scenario.responses[protocol];
		const response = typeof r === "string" ? json(r) : r;
		const c = open(protocol, scenario.model, scenario.system, scenario.task, shot("shot"), golden[0].tools);
		const bodies: unknown[] = [structuredClone(c.body())];
		const turn = c.accept(response);
		const outcomes: Outcome[] = turn.calls.map((_, i) =>
			i === 0 ? { kind: "ok" } : i === 1 ? { kind: "image", shot: shot("after") } : i === 2 ? { kind: "failed", error: "no" } : { kind: "not-executed" },
		);
		c.answer(outcomes, shot("after"), false);
		bodies.push(structuredClone(c.body()));
		c.followUp(scenario.follow_up, shot("follow_shot"));
		bodies.push(structuredClone(c.body()));
		expect(bodies).toEqual(golden);
	});
}

test("screenshots leave the chat history five at a time", () => {
	const counts = Array.from({ length: 21 }, (_, n) => omitted(n));
	expect(counts.slice(0, 9)).toEqual(Array(9).fill(0));
	expect([counts[9], counts[12], counts[13], counts[20]]).toEqual([5, 5, 10, 15]);
});

test("a Claude refusal ends the task with the classifier's explanation", () => {
	const c = open("anthropic", "m", "sys", "t", shot("shot"), null);
	const turn = c.accept(json("docs/claude_response_refusal.json"));
	expect(turn.end).toEqual({ refused: "Declined by a safety classifier." });
	expect(turn.calls).toEqual([]);
});

test("only the computer toolset's tool calls are actions", () => {
	const c = open("anthropic", "m", "sys", "t", shot("shot"), null);
	const turn = c.accept({ id: "m", content: [{ type: "tool_use", id: "x", name: "screenshot", input: {} }] });
	expect(turn.calls).toEqual([]);
});

test("OpenAI's safety checks are acknowledged only once the host user approved them", () => {
	for (const acknowledged of [false, true]) {
		const c = open("openai-responses", "m", "sys", "t", shot("shot"), null);
		const turn = c.accept(json("docs/openai_response_legacy.json"));
		expect(turn.flagged.length).toBe(1);
		expect(turn.calls.map((x) => x.name)).toEqual(["openai"]);
		c.answer([{ kind: "ok" }], shot("after"), acknowledged);
		const body: any = c.body();
		expect(body.previous_response_id).toBeTruthy();
		const ack = body.input[0].acknowledged_safety_checks;
		if (acknowledged) expect(ack[0].id).toBe("cu_sc_1");
		else expect(ack).toBeUndefined();
	}
});
