#!/usr/bin/env node
// Fails on high/critical npm audit advisories that are not explicitly
// allowlisted in scripts/npm_audit_allowlist.json. The allowlist exists only
// for advisories that have no patched release yet; remove entries once npm
// audit clears them.
//
// npm audit reports indirect vulnerabilities with `via` entries that name the
// vulnerable package they inherit from, so a package is tolerated only when
// every advisory in its dependency chain is either below the gate severity or
// allowlisted.
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const allowlistPath = resolve(process.env.NPM_AUDIT_ALLOWLIST ?? join(here, "npm_audit_allowlist.json"));
const allowlist = JSON.parse(readFileSync(allowlistPath, "utf8")).allow ?? [];
const allowed = new Map(allowlist.map((entry) => [entry.id, entry]));

const result = spawnSync("npm", ["audit", "--json"], {
  cwd: join(here, "..", "web"),
  encoding: "utf8",
  shell: process.platform === "win32",
});
let report;
try {
  report = JSON.parse(result.stdout);
} catch {
  console.error("npm audit gate: npm audit did not return JSON");
  console.error(result.stdout || result.stderr);
  process.exit(1);
}

const vulnerabilities = report.vulnerabilities ?? {};
const blocked = (severity) => severity === "high" || severity === "critical";
const advisoryID = (advisory) => (advisory.url ?? "").split("/").pop();

const memo = new Map();
const inFlight = new Set();

function tolerated(name) {
  if (memo.has(name)) return memo.get(name);
  const item = vulnerabilities[name];
  if (!item || !blocked(item.severity)) return true;
  if (inFlight.has(name)) return true;
  inFlight.add(name);
  const advisories = (item.via ?? []).filter((via) => typeof via === "object" && blocked(via.severity));
  const inherited = (item.via ?? []).filter((via) => typeof via === "string");
  let ok = advisories.length > 0 || inherited.length > 0;
  for (const advisory of advisories) {
    if (!allowed.has(advisoryID(advisory))) ok = false;
  }
  for (const parent of inherited) {
    if (!tolerated(parent)) ok = false;
  }
  inFlight.delete(name);
  memo.set(name, ok);
  return ok;
}

const blocking = [];
const toleratedNames = new Set();
const usedAllowlist = new Set();
for (const [name, item] of Object.entries(vulnerabilities)) {
  if (!blocked(item.severity)) continue;
  if (tolerated(name)) {
    toleratedNames.add(name);
    for (const advisory of (item.via ?? [])) {
      if (typeof advisory === "object" && blocked(advisory.severity) && allowed.has(advisoryID(advisory))) {
        usedAllowlist.add(advisoryID(advisory));
      }
    }
  } else {
    blocking.push(`${name} (${item.severity})`);
  }
}

for (const id of usedAllowlist) {
  const entry = allowed.get(id);
  console.log(`npm audit gate: allowlisted ${id} (${entry.package}: ${entry.reason})`);
}
if (blocking.length > 0) {
  console.error("npm audit gate: blocking advisories");
  for (const entry of blocking) console.error(`  - ${entry}`);
  console.error(`Add an entry to ${allowlistPath} only when no patched release exists.`);
  process.exit(1);
}
console.log(`npm audit gate: no blocking high/critical advisories (${toleratedNames.size} vulnerable package(s) covered by allowlist)`);
