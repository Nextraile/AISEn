import { useEffect, useState } from "react";

import { buildUrl } from "./api";
import { config } from "./config";
import { logger } from "./logging";

export type ApiStatus = "checking" | "ok" | "down";

interface ReadyBody {
  status?: string;
  problems?: string[];
  documents?: number;
}

const RETRY_MS = 30_000;
const TIMEOUT_MS = 5_000;

/**
 * Check `/ready` once at boot, then retry every 30 s only while it is down.
 *
 * Stops polling once the API answers, so a healthy deployment does not keep
 * running `COUNT` over the whole collection. The banner exists to make "the API
 * is unreachable" visible immediately instead of on the first search.
 */
export function useApiStatus(): { status: ApiStatus; detail: string | null } {
  const [status, setStatus] = useState<ApiStatus>("checking");
  const [detail, setDetail] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;

    async function probe(): Promise<void> {
      let ok = false;
      let message: string | null = null;

      try {
        const response = await fetch(buildUrl(config.apiBaseUrl, "/ready"), {
          headers: { Accept: "application/json" },
          signal: AbortSignal.timeout(TIMEOUT_MS),
        });
        const body = (await response.json().catch(() => null)) as ReadyBody | null;
        ok = response.ok && body?.status === "ok";
        if (!ok) {
          message =
            body?.problems?.join("; ") || `HTTP ${response.status}`;
        }
      } catch (error) {
        message = error instanceof Error ? error.message : "tidak dapat terhubung";
        logger.debug("readiness check failed", error);
      }

      if (cancelled) {
        return;
      }

      setStatus(ok ? "ok" : "down");
      setDetail(message);
      if (!ok) {
        timer = window.setTimeout(probe, RETRY_MS);
      }
    }

    void probe();
    return () => {
      cancelled = true;
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
    };
  }, []);

  return { status, detail };
}
