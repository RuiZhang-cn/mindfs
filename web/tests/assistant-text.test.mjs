import test from "node:test";
import assert from "node:assert/strict";

import { isPlaceholderAssistantText, mergeAssistantChunk } from "../src/services/assistantText.ts";

test("detects punctuation-only provider placeholders", () => {
  for (const text of [".", "。", "…", "..", " . \n", "\n。\n"]) {
    assert.equal(isPlaceholderAssistantText(text), true, JSON.stringify(text));
  }
});

test("keeps real assistant text", () => {
  for (const text of ["", "   ", "Hello", "第 1 步", "✅ 完成", "1.", "a.b", "…继续"]) {
    assert.equal(isPlaceholderAssistantText(text), false, JSON.stringify(text));
  }
});

test("holds a lone dot until the next delta decides", () => {
  assert.deepEqual(mergeAssistantChunk("", "."), { pending: ".", text: "" });
  assert.deepEqual(mergeAssistantChunk(".", "9"), { pending: "", text: ".9" });
});

test("preserves dots inside a streamed IP address", () => {
  let pending = "";
  let text = "";
  for (const chunk of ["154", ".", "9", ".", "249", ".", "177", ":808/i/"]) {
    const merged = mergeAssistantChunk(pending, chunk);
    pending = merged.pending;
    text += merged.text;
  }
  assert.equal(pending, "");
  assert.equal(text, "154.9.249.177:808/i/");
});

test("keeps accumulating provider placeholders", () => {
  assert.deepEqual(mergeAssistantChunk(".", "."), { pending: "..", text: "" });
  assert.deepEqual(mergeAssistantChunk("。", "…"), { pending: "。…", text: "" });
  assert.deepEqual(mergeAssistantChunk("", "hello"), { pending: "", text: "hello" });
});
