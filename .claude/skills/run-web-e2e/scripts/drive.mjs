// Drive the hazyflow web UI headlessly and run checks with screenshots.
//
// Prereqs (see ../SKILL.md):
//   1. bash scripts/stage-headless-deps.sh   (libs + fonts in /tmp)
//   2. cd /tmp && npm i playwright@latest && npx playwright install chromium
//   3. daemon running on :8090, vite on :5173 (proxying to :8090)
//
// Run:  node drive.mjs
// Env is baked in below on purpose — the harness can choke on long
// env-prefixed command lines, so keep the invocation a bare `node`.

process.env.LD_LIBRARY_PATH = "/tmp/libs:" + (process.env.LD_LIBRARY_PATH || "");
process.env.FONTCONFIG_FILE = "/tmp/fonts/fonts.conf";

import pw from "/tmp/node_modules/playwright/index.js";
const { chromium } = pw;

const API = "http://localhost:8090";
const APP = "http://localhost:5173";
const SHOT = "/tmp/shots";
const FID = "drive-" + process.pid; // fresh id per run — see gotcha #2 in SKILL.md
const EMAIL = `e2e-${process.pid}@example.com`;

const R = [];
const check = (name, cond) => { R.push({ name, ok: !!cond }); console.log(`${cond ? "PASS" : "FAIL"}  ${name}`); };

// ── 1. auth + seed via fetch (token never touches the CLI) ──
const su = await fetch(`${API}/api/v1/auth/signup`, {
  method: "POST", headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ email: EMAIL, password: "TestPassw0rd!23" }),
});
const { token, tenant } = await su.json();
if (!token) throw new Error("signup failed: " + su.status);

const seed = await fetch(`${API}/api/v1/me/flows/${encodeURIComponent(tenant + "/main/" + FID)}`, {
  method: "PUT", headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
  body: JSON.stringify({
    id: FID, tenant, workspace: "main", name: "Drive Test",
    nodes: [{ id: "hook", module: "webhook_input", params: {}, position: { x: 200, y: 160 } }],
    edges: [],
  }),
});
console.log("signup + seed:", su.status, seed.status, "flow:", FID);

// ── 2. drive the browser ──
import { mkdirSync } from "fs";
mkdirSync(SHOT, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
await ctx.addInitScript(([t, n]) => {
  localStorage.setItem("hazyflow.token", t);
  localStorage.setItem("hazyflow.activeTenant", n);
  localStorage.setItem("hazyflow.activeWorkspace", "main");
}, [token, tenant]);
const page = await ctx.newPage();

try {
  // Client-side nav (gotcha #1): list waits for workspace, then click the card.
  await page.goto(`${APP}/flows`, { waitUntil: "networkidle" });
  await page.getByRole("link", { name: new RegExp(FID) }).click();
  await page.waitForSelector(".editor-toolbar", { timeout: 15000 });
  await page.waitForSelector(".react-flow__node", { timeout: 10000 }).catch(() => {});
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOT}/01-editor.png` });
  check("node renders on canvas", (await page.locator(".react-flow__node").count()) > 0);

  // ── example checks: the Triggers modal (edit/extend for your feature) ──
  await page.getByRole("button", { name: "Triggers", exact: true }).click();
  const d = page.locator(".settings-dialog");
  await d.waitFor();
  await page.screenshot({ path: `${SHOT}/02-triggers-modal.png` });
  for (const tab of ["Form", "Webhook", "Schedule"]) {
    check(`${tab} tab present`, await d.getByRole("button", { name: tab }).count());
  }
  await d.getByRole("button", { name: "Form" }).click();
  await page.waitForTimeout(200);
  await d.locator('input[type="checkbox"]').first().check();
  await page.waitForTimeout(400);
  check("form link appears", (await d.locator("code", { hasText: "/form/" }).count()) > 0);
  await d.getByRole("button", { name: "Save", exact: true }).click();
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${SHOT}/03-after-save.png` });
  check("toolbar swapped to 'Send test event'",
    (await page.getByRole("button", { name: /Send test event/i }).count()) > 0);
} catch (e) {
  console.log("ERROR:", e.message);
  await page.screenshot({ path: `${SHOT}/error.png` }).catch(() => {});
  R.push({ name: "ran without throwing", ok: false });
} finally {
  await browser.close();
}

const failed = R.filter((r) => !r.ok).length;
console.log(`\n=== ${R.length - failed}/${R.length} checks passed ===  (screenshots in ${SHOT})`);
process.exit(failed ? 1 : 0);
