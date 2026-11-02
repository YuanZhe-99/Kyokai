// Writes the notices of every npm package compiled into kyokai-agent: each
// package's name, version and license, and its license text, in one file
// the app ships (scripts/build.sh, resources/licenses/SIDECAR_LICENSES.txt).
//
//   bun licenses.ts <output file>

import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const root = join(import.meta.dir, "node_modules");
const dirs: string[] = [];
for (const name of readdirSync(root)) {
	if (name.startsWith(".")) continue;
	if (name.startsWith("@")) for (const sub of readdirSync(join(root, name))) dirs.push(join(root, name, sub));
	else dirs.push(join(root, name));
}

const parts: string[] = [];
for (const dir of dirs.sort()) {
	const manifest = join(dir, "package.json");
	if (!existsSync(manifest)) continue;
	const pkg = JSON.parse(readFileSync(manifest, "utf8"));
	const license = typeof pkg.license === "string" ? pkg.license : (pkg.license?.type ?? "see its license text");
	const file = readdirSync(dir).find((f) => /^(licen[cs]e|copying)(\..*)?$/i.test(f));
	const text = file ? readFileSync(join(dir, file), "utf8").trim() : "(the package ships no license text)";
	parts.push(`${pkg.name} ${pkg.version} — ${license}\n\n${text}\n`);
}

const out = process.argv[2];
if (!out) throw new Error("usage: bun licenses.ts <output file>");
writeFileSync(out, `The npm packages compiled into kyokai-agent (Kyokai's agent sidecar).\n\n${parts.join("\n----\n\n")}`);
console.log(`${parts.length} packages`);
