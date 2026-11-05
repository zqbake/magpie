// The real install list and confirmation, with package managers/API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const command = "npm install -g @google/gemini-cli";
const items = [{ id: "gemini", name: "Gemini CLI", icon: "generic", commands: [
  { via: "npm", command, runnable: true },
  { via: "script", command: "curl https://example.com/install | bash" },
] }, { id: "pi", name: "Pi", icon: "generic", commands: [{ via: "npm", node: "nvm", command: "nvm install --lts && npm install -g pi" }] }];
const words = { en: "Install", zh: "安装", "zh-TW": "安裝", ja: "インストール", de: "Installieren" };

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const [lang, install] of Object.entries(words)) {
    test(`${engine} ${lang}: install confirms, stays busy, shows failure/copy fallback, then refreshes`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 440, height: 800 } });
      page.setDefaultTimeout(5000);
      const errors = [], calls = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let release, installed = false, fail = true;
      const state = () => ({ agents: installed ? [{ id: "gemini", name: "Gemini CLI", icon: "generic", wired: false, fields: [] }] : [], profiles: [], settings: { lang, theme: "light" } });
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const json = (data, status = 200) => route.fulfill({ json: data, status });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/agents/install/gemini") {
          calls.push({ method: route.request().method(), body: route.request().postDataJSON() });
          await new Promise((resolve) => { release = resolve; });
          if (fail) return json({ error: "npm error code ETARGET" }, 400);
          installed = true;
          return json(state());
        }
        if (url.pathname === "/api/state") return json(state());
        if (url.pathname === "/api/agents/install") return json(installed ? items.slice(1) : items);
        if (url.pathname === "/api/usage/quotas" || url.pathname === "/api/plugins/agents") return json([]);
        if (url.pathname === "/api/groups") return json({ groups: [] });
        if (url.pathname === "/api/plugins") return json({ plugins: [] });
        if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
        if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
      });
      await page.goto("http://magpie.test/?view=agents");
      const go = page.locator(".ag-install-run");
      await go.waitFor();
      assert.equal(await go.count(), 1, "script/bootstrap commands are copy-only");
      assert.equal(await go.textContent(), install);
      await go.click();
      const ask = page.locator("dialog.action-confirm[open]");
      await ask.waitFor();
      assert.ok((await ask.textContent()).includes(command));
      assert.doesNotMatch(await ask.textContent(), /\{\w+\}/);
      if (lang !== "en") assert.ok(!(await ask.textContent()).includes("Runs as your current user"));
      await page.keyboard.press("Escape");
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls, []);
      await go.click();
      await ask.locator("button.primary").click();
      await page.waitForFunction(() => document.querySelector(".ag-install-run")?.disabled);
      assert.equal(await go.isDisabled(), true);
      assert.deepEqual(calls, [{ method: "POST", body: { via: "npm" } }]);
      release();
      const diagnostic = page.locator(".ag-install-error");
      await diagnostic.waitFor();
      assert.match(await diagnostic.textContent(), /ETARGET/);
      assert.equal(await go.isDisabled(), false);
      assert.equal(await page.locator('.ag-install-row[data-id="gemini"] .copy').count(), 2, "copy buttons remain after failure");
      fail = false;
      await go.click();
      await ask.locator("button.primary").click();
      await page.waitForFunction(() => document.querySelector(".ag-install-run")?.disabled);
      release();
      await page.locator('.row.agent[data-id="gemini"]').waitFor();
      await page.locator('.ag-install-row[data-id="gemini"]').waitFor({ state: "detached" });
      assert.equal(calls.length, 2);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "no horizontal overflow");
      assert.deepEqual(errors, []);
    });
  }
}
