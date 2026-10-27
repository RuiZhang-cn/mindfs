const PLACEHOLDER_ONLY = /^[.。…]+$/;

/**
 * Whether assistant text contains only sentence punctuation.
 *
 * Some OpenAI-compatible providers (notably DeepSeek's native API) send "." as
 * the assistant content for a turn that otherwise only carries reasoning and
 * tool calls. MindFS renders assistant text as Markdown, so each placeholder
 * becomes a stray empty paragraph between the thinking block and the tool card.
 * Treat punctuation-only text as a provider placeholder rather than a reply.
 */
export function isPlaceholderAssistantText(text: string): boolean {
  const trimmed = (text || "").trim();
  return trimmed.length > 0 && PLACEHOLDER_ONLY.test(trimmed);
}

/**
 * Merge a streamed assistant delta with punctuation held back from earlier
 * deltas. Returns `text` only when the merged content is no longer
 * punctuation-only. This keeps a real dot inside an IP address while still
 * dropping the provider's lone "." between reasoning and a tool call.
 */
export function mergeAssistantChunk(
  pending: string,
  chunk: string,
): { pending: string; text: string } {
  const merged = `${pending || ""}${chunk || ""}`;
  if (isPlaceholderAssistantText(merged)) {
    return { pending: merged, text: "" };
  }
  return { pending: "", text: merged };
}
