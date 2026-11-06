import assert from "node:assert/strict";
import { access, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { after, before, test } from "node:test";
import { chromium } from "playwright-core";
import { createServer } from "vite";

let server, browser, fixtureDir;
before(async () => {
  const candidates = process.platform === "win32"
    ? ["C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe", "C:/Program Files/Google/Chrome/Application/chrome.exe"]
    : process.platform === "darwin"
      ? ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"]
      : ["/opt/google/chrome/chrome", "/usr/bin/chromium", "/usr/bin/google-chrome"];
  let executablePath;
  for (const candidate of candidates) {
    try { await access(candidate); executablePath = candidate; break; } catch {}
  }
  assert.ok(executablePath, "Requires an installed Chromium browser");
  server = await createServer({
    root: fileURLToPath(new URL("../", import.meta.url)),
    server: { host: "127.0.0.1", port: 0 },
  });
  await server.listen();
  browser = await chromium.launch({ executablePath, headless: true });
  fixtureDir = await mkdtemp(path.join(os.tmpdir(), "mindfs-attachment-test-"));
});
after(async () => {
  await browser?.close();
  await server?.close();
  if (fixtureDir) await rm(fixtureDir, { recursive: true, force: true });
});

async function openComposer() {
  const page = await browser.newPage({ locale: "en-US", viewport: { width: 1280, height: 800 } });
  page.setDefaultTimeout(5000);
  await page.addInitScript(() => localStorage.setItem("mindfs-locale", "en-US"));
  await page.route("**/api/**", route => route.fulfill({ json: { agents: [], shells: [] } }));
  await page.goto(`${server.resolvedUrls.local[0]}tests/file-attachments.html`);
  await page.locator('[data-onboarding="attachment-action"]:not([disabled])').waitFor();
  return page;
}

test("file paste beats Lexical text handling, including files-only browser payloads", async () => {
  const page = await openComposer();
  try {
    const editor = page.locator(".token-editor-input");
    await editor.fill("Keep this draft");
    await editor.evaluate(element => {
      const mixed = new DataTransfer();
      mixed.items.add(new File(["%PDF-1.7"], "report.pdf", { type: "application/pdf" }));
      mixed.setData("text/plain", "file metadata, not draft text");
      element.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: mixed }));
      const fallback = new DataTransfer();
      fallback.items.add(new File([], "空文件.unknown"));
      Object.defineProperty(fallback, "items", { value: [] });
      element.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: fallback }));
    });
    await page.getByRole("button", { name: "Remove attachment report.pdf", exact: true }).waitFor();
    await page.getByRole("button", { name: "Remove attachment 空文件.unknown", exact: true }).waitFor();
    assert.equal(await editor.innerText(), "Keep this draft");
    await editor.evaluate(element => {
      const text = new DataTransfer();
      text.setData("text/plain", " normal text");
      element.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: text }));
    });
    await page.waitForFunction(() => document.querySelector(".token-editor-input").innerText === "Keep this draft normal text");
  } finally { await page.close(); }
});

test("native file drag adds mixed attachments and rejects files without a selected project", async () => {
  const page = await openComposer();
  const cdp = await page.context().newCDPSession(page);
  try {
    const names = ["archive.zip", "no-extension", "空文件.unknown"];
    const files = names.map(name => path.join(fixtureDir, name));
    await Promise.all(files.map((file, i) => writeFile(file, i === 2 ? "" : "file bytes")));
    const box = await page.locator(".token-editor-input").boundingBox();
    const position = { x: box.x + 20, y: box.y + 20 };
    const data = { items: [], files, dragOperationsMask: 1 };
    await cdp.send("Input.dispatchDragEvent", { type: "dragEnter", ...position, data });
    await cdp.send("Input.dispatchDragEvent", { type: "dragOver", ...position, data });
    await page.locator('[data-file-drop-active="true"]').waitFor();
    await cdp.send("Input.dispatchDragEvent", { type: "drop", ...position, data });
    for (const name of names) {
      await page.getByRole("button", { name: `Remove attachment ${name}`, exact: true }).waitFor();
    }
    assert.equal(await page.locator('[data-file-drop-active="true"]').count(), 0);
    await page.getByRole("button", { name: "Clear project", exact: true }).click();
    await page.locator('[data-onboarding="attachment-action"][disabled]').waitFor();
    const blocked = path.join(fixtureDir, "blocked.bin");
    await writeFile(blocked, "must not attach");
    const blockedData = { items: [], files: [blocked], dragOperationsMask: 1 };
    await cdp.send("Input.dispatchDragEvent", { type: "dragEnter", ...position, data: blockedData });
    await cdp.send("Input.dispatchDragEvent", { type: "dragOver", ...position, data: blockedData });
    await cdp.send("Input.dispatchDragEvent", { type: "drop", ...position, data: blockedData });
    assert.equal(await page.getByRole("button", { name: "Remove attachment blocked.bin", exact: true }).count(), 0);
    assert.equal(await page.locator('[data-file-drop-active="true"]').count(), 0);
    assert.equal(page.url().includes("file-attachments.html"), true, "Dropping a file must not navigate away");
  } finally { await cdp.detach(); await page.close(); }
});
