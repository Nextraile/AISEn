# Network Layer (`src/app`)

The network layer for AISEn. **Owned by the network developer** — UI developers
just consume hooks and never touch `fetch`, URLs, headers, or error handling.

> API contract status: `POST /search` (FastAPI + ChromaDB) follows the official
> documentation. Other endpoints are still **provisional** — they are waiting on
> backend documentation; update only
> `src/features/<feature>/types/*.ts` when that lands.

## Structure

```
src/app/
├── api/
│   ├── types.ts         # Base types (RequestOptions, interceptors, config)
│   ├── body.ts          # Body serialization (JSON vs FormData)
│   ├── errors.ts        # ApiError + messages safe to show users
│   ├── state.ts         # Discriminated union state + reducer
│   ├── interceptors.ts  # Built-in interceptors (headers, auth, envelope, logger)
│   ├── client.ts        # Base API client (fetch, timeout, abort, error normalization)
│   └── index.ts         # ← the public imports live here
└── hooks/
    ├── useRequestState.ts  # Generic request-state hook
    └── index.ts
```

## How a UI developer uses a hook

```tsx
const { state, refetch, reset } = useSearchDocuments(query);

switch (state.status) {
  case "idle":    return null;
  case "loading": return <Spinner />;
  case "error":   return <p>{state.error.message}</p>; // already clear and safe
  case "success": return <List items={state.data} />;
}
```

- `state.error.message` is **always** safe to show the user.
- `state.error.detail` / `.status` / `.code` are for debugging only.
- `refetch()` backs a "try again" button; `reset()` returns to `idle`.

## How a network developer adds an endpoint

```ts
// 1. Define the contract in features/<feature>/types/*.ts
export interface ProfileResponse { name: string; }

// 2. Add a consumer in features/<feature>/services/*Api.ts
import { apiClient } from "../../../app/api";

export const getProfile = () => apiClient.get<ProfileResponse>("/api/v1/profile");
```

`apiClient` already handles: the base URL (`VITE_API_BASE_URL`), JSON headers, an
`Authorization` token (from `localStorage["access_token"]`), a 15-second timeout,
query parameters, envelope unwrapping (`{ data, message, code }`), and
normalizing every failure into an `ApiError` with a clear message.

## Error kinds (`ApiErrorKind`)

| kind | meaning |
|---|---|
| `network` | Could not connect (offline/DNS/CORS) |
| `timeout` | Server did not respond within 15 seconds |
| `abort` | Cancelled (not a failure; hooks ignore it) |
| `unauthorized` / `forbidden` | 401 / 403 |
| `notFound` | 404 |
| `validation` | 400 / 422 (uses the server message when present) |
| `server` | 5xx |
| `client` | Any other 4xx |
| `parse` | Response was not valid JSON |
| `unknown` | Anything else |

## Interceptors

Order: request → `jsonHeaders` → `auth` → custom; response →
`unwrapEnvelope` → custom.

```ts
const log = createLoggingInterceptor();
const api = createApiClient({
  requestInterceptors: [log.request],
  responseInterceptors: [log.response],
  enableEnvelopeUnwrap: true, // turn off if the backend does not use an envelope
});
```
