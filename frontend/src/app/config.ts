/**
 * Konfigurasi frontend.
 *
 * Semua nilai dibaca dari `import.meta.env` (prefix `VITE_`) dan di-inline oleh
 * Vite saat build: mengubahnya butuh build ulang. Nilai bawaan di sini sama
 * dengan yang berlaku sebelum konfigurasi dipindah, jadi `.env` yang kosong
 * berperilaku persis seperti sebelumnya.
 *
 * Backend punya batas threshold sendiri (`THRESHOLD_MIN`/`THRESHOLD_MAX`,
 * bawaan 0.1-0.9). Preset di bawah dijepit ke rentang yang sama supaya UI tidak
 * mengirim nilai yang pasti ditolak dengan 422.
 */

const FALLBACK = {
  apiBaseUrl: "",
  apiTimeoutMs: 15_000,
  defaultThreshold: 0.53,
  resultsPerPage: 10,
  searchDebounceMs: 350,
  relevanceLow: 0.4,
  relevanceMedium: 0.5,
  relevanceHigh: 0.6,
} as const;

const THRESHOLD_FLOOR = 0.1;
const THRESHOLD_CEILING = 0.9;

function toNumber(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw.trim() === "") {
    return fallback;
  }
  const value = Number(raw);
  return Number.isFinite(value) ? value : fallback;
}

function toInteger(raw: string | undefined, fallback: number, minimum: number): number {
  return Math.max(minimum, Math.round(toNumber(raw, fallback)));
}

function toThreshold(raw: string | undefined, fallback: number, name: string): number {
  const value = toNumber(raw, fallback);

  if (value < THRESHOLD_FLOOR || value > THRESHOLD_CEILING) {
    const clamped = Math.min(THRESHOLD_CEILING, Math.max(THRESHOLD_FLOOR, value));
    console.warn(
      `[config] ${name}=${value} di luar ${THRESHOLD_FLOOR}-${THRESHOLD_CEILING}; memakai ${clamped}.`
    );
    return clamped;
  }

  return value;
}

function relevancePresets() {
  const low = toThreshold(import.meta.env.VITE_RELEVANCE_LOW, FALLBACK.relevanceLow, "VITE_RELEVANCE_LOW");
  const medium = toThreshold(
    import.meta.env.VITE_RELEVANCE_MEDIUM,
    FALLBACK.relevanceMedium,
    "VITE_RELEVANCE_MEDIUM"
  );
  const high = toThreshold(import.meta.env.VITE_RELEVANCE_HIGH, FALLBACK.relevanceHigh, "VITE_RELEVANCE_HIGH");

  if (low > medium || medium > high) {
    console.warn(
      `[config] Preset relevansi harus naik (low <= medium <= high); ` +
        `ditemukan ${low}/${medium}/${high}, memakai nilai bawaan.`
    );
    return {
      low: FALLBACK.relevanceLow,
      medium: FALLBACK.relevanceMedium,
      high: FALLBACK.relevanceHigh,
    };
  }

  return { low, medium, high };
}

export const config = {
  apiBaseUrl: (import.meta.env.VITE_API_BASE_URL ?? FALLBACK.apiBaseUrl).trim(),
  apiTimeoutMs: toInteger(import.meta.env.VITE_API_TIMEOUT_MS, FALLBACK.apiTimeoutMs, 1),
  defaultThreshold: toThreshold(
    import.meta.env.VITE_DEFAULT_THRESHOLD,
    FALLBACK.defaultThreshold,
    "VITE_DEFAULT_THRESHOLD"
  ),
  resultsPerPage: toInteger(import.meta.env.VITE_RESULTS_PER_PAGE, FALLBACK.resultsPerPage, 1),
  searchDebounceMs: toInteger(
    import.meta.env.VITE_SEARCH_DEBOUNCE_MS,
    FALLBACK.searchDebounceMs,
    0
  ),
  relevancePresets: relevancePresets(),
} as const;
