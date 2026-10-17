// Shared date/time formatting for console views. A missing or unparseable value
// renders as an em dash so a card or table never shows "Invalid Date".
export function formatDateTime(value: string): string {
  if (!value) {
    return '—';
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return '—';
  }
  return parsed.toLocaleString();
}
