const MIN_THROUGHPUT_DURATION_MS = 1000;

/**
 * Output tokens per second for one reply, or null when the sample is too small
 * to be meaningful. Callers pass the agent-reported model duration when
 * available and the turn's wall-clock duration otherwise.
 */
export function computeTokensPerSecond(
  outputTokens: number,
  durationMs: number | null | undefined,
): number | null {
  const tokens = Number(outputTokens);
  const duration = Number(durationMs);
  if (!Number.isFinite(tokens) || tokens <= 0) {
    return null;
  }
  if (!Number.isFinite(duration) || duration < MIN_THROUGHPUT_DURATION_MS) {
    return null;
  }
  return (tokens * 1000) / duration;
}

export function formatTokensPerSecond(rate: number | null | undefined): string {
  if (rate === null || rate === undefined || !Number.isFinite(rate) || rate <= 0) {
    return "";
  }
  return `${rate.toFixed(1)} tok/s`;
}

/** Cache-read share of logical input, one decimal so non-exact ratios stay honest. */
export function formatCacheHitPercent(
  cacheReadTokens: number | null | undefined,
  inputTokens: number | null | undefined,
): string {
  const read = Number(cacheReadTokens);
  const input = Number(inputTokens);
  if (!Number.isFinite(read) || !Number.isFinite(input) || input <= 0 || read < 0) {
    return "";
  }
  if (read >= input) {
    return "100%";
  }
  const percent = Math.floor((read / input) * 1000) / 10;
  if (percent <= 0) {
    return read > 0 ? "<0.1%" : "0%";
  }
  return `${percent.toFixed(1)}%`;
}

const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: "$",
  CNY: "¥",
  RMB: "¥",
  EUR: "€",
  GBP: "£",
};

/** Compact money label: `$0.42`, `$0.0123`, or `0.42 XYZ` for unknown codes. */
export function formatCostAmount(
  amount: number | null | undefined,
  currency?: string,
): string {
  const value = Number(amount);
  if (!Number.isFinite(value) || value <= 0) {
    return "";
  }
  const code = `${currency || "USD"}`.trim().toUpperCase() || "USD";
  const digits = value >= 1 ? 2 : value >= 0.01 ? 4 : value >= 0.0001 ? 6 : 8;
  // Keep two decimals, drop only extra padding: `$0.0425`, `$0.50`.
  const text = value
    .toFixed(digits)
    .replace(/(\.\d{2}\d*?)0+$/, "$1");
  const symbol = CURRENCY_SYMBOLS[code];
  return symbol ? `${symbol}${text}` : `${text} ${code}`;
}
