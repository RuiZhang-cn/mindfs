/** Elapsed milliseconds between two ISO timestamps, or null when unavailable. */
export function sessionElapsedMs(
  startISO: string | undefined,
  endISO: string | undefined,
): number | null {
  const start = Date.parse(startISO || "");
  const end = Date.parse(endISO || "");
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) {
    return null;
  }
  return end - start;
}

export function formatSessionDuration(startISO: string | undefined, endISO: string | undefined): string {
  const elapsedMs = sessionElapsedMs(startISO, endISO);
  if (elapsedMs === null) {
    return "";
  }

  const seconds = Math.floor(elapsedMs / 1000);
  if (seconds <= 0) {
    return "";
  }
  if (seconds <= 100) {
    return `(${seconds}s)`;
  }
  return `(${Math.floor(seconds / 60)}m)`;
}
