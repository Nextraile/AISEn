/// <reference types="vite/client" />

/**
 * Variabel `VITE_*` yang dikenali. Semua opsional: kalau tidak diisi, nilai
 * bawaannya ada di `src/app/config.ts`. Dokumentasinya di `.env.example`.
 *
 * Ingat: Vite meng-inline nilai ini saat build, jadi mengubahnya butuh build
 * ulang, bukan sekadar restart dev server.
 */
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_API_TIMEOUT_MS?: string;
  readonly VITE_DEFAULT_THRESHOLD?: string;
  readonly VITE_RESULTS_PER_PAGE?: string;
  readonly VITE_SEARCH_DEBOUNCE_MS?: string;
  readonly VITE_RELEVANCE_LOW?: string;
  readonly VITE_RELEVANCE_MEDIUM?: string;
  readonly VITE_RELEVANCE_HIGH?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
