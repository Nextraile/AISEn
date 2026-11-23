/**
 * Small logging facade.
 *
 * Keeps `console.debug` noise out of production while leaving warnings and
 * errors visible. Everything is prefixed with `[aisen]` so a report can point
 * at a specific line.
 */

type Level = "debug" | "info" | "warn" | "error";

const isDev = import.meta.env.DEV;

function emit(level: Level, message: string, ...args: unknown[]): void {
  if (level === "debug" && isDev === false) {
    return;
  }
  const sink =
    level === "debug"
      ? console.debug
      : level === "info"
        ? console.info
        : level === "warn"
          ? console.warn
          : console.error;
  sink(`[aisen] ${message}`, ...args);
}

export const logger = {
  debug: (message: string, ...args: unknown[]) => emit("debug", message, ...args),
  info: (message: string, ...args: unknown[]) => emit("info", message, ...args),
  warn: (message: string, ...args: unknown[]) => emit("warn", message, ...args),
  error: (message: string, ...args: unknown[]) => emit("error", message, ...args),
};
