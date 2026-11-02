// Spike 5's harness: plays Kyokai's engine and a provider (a local
// openai-chat stand-in) around the sidecar, and checks the spike's questions:
// screenshots reach the provider byte for byte, the key comes only over the
// pipe, abort during a tool stops the task, nothing but the provider is
// contacted, nothing is written; and it reports start-up time and memory.
//
//   bun harness.ts [path to a compiled sidecar]   (default: bun sidecar.ts)

import { existsSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";

const KEY = "sk-spike-not-a-real-key";
// Two distinct PNGs (any bytes would do: they must arrive unchanged).
const png = (n: number) => Buffer.from(new Uint8Array(4096).map((_, i) => (i * 31 + n) & 255)).toString("base64");
const FIRST = png(1);
const AFTER = png(2);

function provider(script: any[], delayMs = 0) {
	const bodies: any[] = [];
	const headers: Record<string, string>[] = [];
	const server = Bun.serve({
		port: 0,
		async fetch(req) {
			bodies.push(await req.json());
			headers.push(Object.fromEntries(req.headers));
			if (delayMs) await Bun.sleep(delayMs);
			return Response.json(script[Math.min(bodies.length - 1, script.length - 1)]);
		},
	});
	return { url: `http://127.0.0.1:${server.port}`, bodies, headers, stop: () => server.stop(true) };
}

const call = (name: string, args: unknown) => ({
	choices: [{ finish_reason: "tool_calls", message: { role: "assistant", content: "", tool_calls: [{ id: `c-${name}`, type: "function", function: { name, arguments: JSON.stringify(args) } }] } }],
	usage: { prompt_tokens: 1200, prompt_tokens_details: { cached_tokens: 1000 }, completion_tokens: 20 },
});
const finish = { choices: [{ finish_reason: "stop", message: { role: "assistant", content: "Saved." } }], usage: { prompt_tokens: 2400, prompt_tokens_details: { cached_tokens: 2200 }, completion_tokens: 5 } };

async function run(cmd: string[], url: string, opts: { abortOnAction?: boolean } = {}) {
	const cwd = mkdtempSync(join(tmpdir(), "pi-spike-"));
	const t0 = performance.now();
	const p = Bun.spawn(cmd, { cwd, stdin: "pipe", stdout: "pipe", stderr: "pipe", env: { PATH: process.env.PATH ?? "", SYSTEMROOT: process.env.SYSTEMROOT ?? "" } });
	const events: any[] = [];
	const reader = p.stdout.getReader();
	let buf = "";
	let helloMs = 0;
	const write = (m: unknown) => {
		p.stdin.write(`${JSON.stringify(m)}\n`);
		p.stdin.flush();
	};
	for (;;) {
		const { value, done } = await reader.read();
		if (done) break;
		buf += new TextDecoder().decode(value);
		for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
			const m = JSON.parse(buf.slice(0, i));
			buf = buf.slice(i + 1);
			events.push(m);
			if (m.t === "hello") {
				helloMs = Math.round(performance.now() - t0);
				write({ t: "start", url, model: "spike-model", system: "You operate a computer.", instruction: "Press SAVE.", shot: FIRST });
			} else if (m.t === "key?") write({ t: "key", key: KEY });
			else if (m.t === "act") {
				if (opts.abortOnAction) {
					// The host user stops the task while the action runs; its
					// result comes after the abort.
					write({ t: "abort" });
					await Bun.sleep(300);
				}
				write({ t: "result", id: m.id, shot: AFTER });
			}
		}
	}
	await p.exited;
	const stderr = await new Response(p.stderr).text();
	const written = readdirSync(cwd);
	rmSync(cwd, { recursive: true, force: true });
	return { events, helloMs, written, stderr, totalMs: Math.round(performance.now() - t0) };
}

const sidecar = process.argv[2] ? [process.argv[2]] : ["bun", join(import.meta.dir, "sidecar.ts")];
const piHome = join(homedir(), ".pi");
const piBefore = existsSync(piHome);
const results: Record<string, unknown> = {};

// 1. A whole task: click, then done.
{
	const prov = provider([call("left_click", { coordinate: [640, 425] }), finish]);
	const r = await run(sidecar, prov.url);
	prov.stop();
	if (!prov.bodies.length) { console.log(JSON.stringify(r.events), r.stderr.slice(0, 3000)); process.exit(1); }
	const t = r.events.map((e) => e.t);
	const firstImage = JSON.stringify(prov.bodies[0]).includes(FIRST);
	const afterImage = JSON.stringify(prov.bodies[1] ?? {}).includes(AFTER);
	const hosts = [...new Set(r.events.filter((e) => e.t === "net").map((e) => e.host))];
	results.task = {
		events: t.join(","),
		requests: prov.bodies.length,
		imagesByteForByte: firstImage && afterImage,
		keyOnlyOverPipe: prov.headers.every((h) => h.authorization === `Bearer ${KEY}`),
		hosts,
		onlyProvider: hosts.every((h) => h === new URL(prov.url).host),
		filesWrittenInCwd: r.written,
		helloMs: r.helloMs,
		ready: r.events.find((e) => e.t === "ready"),
		endRssMB: Math.round((r.events.find((e) => e.t === "done")?.rss ?? 0) / 1048576),
		totalMs: r.totalMs,
		stderr: r.stderr.slice(0, 400),
	};
}

// 2. Abort during a tool: nothing more goes to the provider.
{
	const prov = provider([call("left_click", { coordinate: [640, 425] }), finish]);
	const r = await run(sidecar, prov.url, { abortOnAction: true });
	prov.stop();
	results.abort = {
		end: r.events.at(-1),
		requestsAfterAbort: prov.bodies.length - 1,
	};
}

results.piHomeCreated = !piBefore && existsSync(piHome);
console.log(JSON.stringify(results, null, 2));
