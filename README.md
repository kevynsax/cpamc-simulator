# cpamc-simulator

An offline, secret-free HTTP stand-in for [cpamc](https://github.com/kevynsax/cpamc-custom). For local development, not production. MIT licensed; Go standard library only.

```sh
docker run --rm -p 127.0.0.1:8317:8317 kevynsax/cpamc-simulator:0.1.1
# Or Apple container:
container run --rm -p 127.0.0.1:8317:8317 docker.io/kevynsax/cpamc-simulator:0.1.1
```

Public Docker Hub image: `docker.io/kevynsax/cpamc-simulator`, Linux amd64 and arm64. Bind published ports to loopback. After pulling the image, the simulator needs no network, provider account, GPU, database, volume, or real credentials. Typical image size is a few MB; requests cost nothing. Every response includes `X-Cpamc-Simulator: true`. Simulated token usage is fixture data, not a real bill.

## Connection settings

| Setting | Default |
| --- | --- |
| Client base URL | `http://127.0.0.1:8317/v1` |
| Client bearer key | `simulator-client-key` |
| Management base URL | `http://127.0.0.1:8317/v8/management` |
| Management bearer key | `simulator-management-key` |
| `LISTEN_ADDR` | `:8317` |
| `CLIENT_API_KEY` | `simulator-client-key` |
| `MANAGEMENT_API_KEY` | `simulator-management-key` |
| `SIMULATOR_SCENARIO` | `healthy` |

Dummy keys are public test values, never production credentials. Client keys cannot access management routes and management keys cannot access client routes. The simulator never forwards credentials or requests elsewhere. JSON bodies are bounded to 1 MiB and multipart edits to 16 MiB and stay in request memory; neither prompts nor image references are stored or logged. Restart resets all in-memory fixture state.

```sh
curl http://127.0.0.1:8317/v1/models -H 'Authorization: Bearer simulator-client-key'
curl http://127.0.0.1:8317/v8/management/credentials \
  -H 'Authorization: Bearer simulator-management-key'
curl http://127.0.0.1:8317/v1/chat/completions \
  -H 'Authorization: Bearer simulator-client-key' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}]}'
```

## Supported contracts and limits

- `GET /v1/models`: fixed text, image and Jev model catalog.
- `GET /v1/quota/view`: cached fake Codex Plus quota, never real account identities.
- `GET /v8/management/credentials`, `GET /v8/management/custom/quota/credentials`: fake credential metadata only.
- `POST /v1/chat/completions`: fixed text, SSE including usage and `[DONE]`, `json_object` translation fixtures, and a bounded subset of `json_schema` fixtures (types, properties, arrays, enum, const and basic lengths/bounds), capped at 1,024 generated nodes. Translation-shaped `json_schema` requests (the last user message is a JSON object with `targetLanguage`) echo each requested top-level string the input also carries as `[<targetLanguage>] <input>`, so markup, URLs and placeholders stay byte-identical. Unsupported schemas fail explicitly. Fixture content is **not** a real translation or completion.
- `POST /v1/images/generations`, `POST /v1/images/edits`: valid 256px checkerboard PNGs, not prompt-based generation; requested dimensions and quality do not affect the placeholder. Reference images are discarded, not edited. Chat image models return the same placeholder as a data URL.
- `POST /v1/decisions`: deterministic canned answers, **not real judgments**, to exercise positive classification flows. Choice picks the first offered key in request order (`options` or native `criteria`) with one-hot probabilities and fake confidence 1. Noul returns `noul: 0.9`; Score returns `score: 0` with the first level selected, a legend, one-hot probabilities and fake confidence 1 (`levels` or native `criteria`). Legacy boolean/number questions return `value: true` / `value: 1`. State is ignored, never interpreted or reflected. The response and every answer carry `simulator: true`; reported cost is 0. Empty/malformed choice options or score levels fail with 400. Supports batches of up to 48. Never use these fake answers or confidence values for real safety, permissions or content approval.
- `POST /v1/live`: honest `501`, no simulated WebRTC session.
- `GET /live`, `GET /ready`, `GET /healthz`: health only, unauthenticated; business `/v1/live` is different.
- Unsupported routes return `404`; invalid input `400`; incorrect keys `401`; oversized JSON `413`.

Scenarios (`SIMULATOR_SCENARIO`): `healthy`, `low-quota` (99% consumed), `rate-limited` (AI requests return 429 with Retry-After), `upstream-error` (AI requests return 502). Restart to switch; there is no public debug/control API.

## Develop and release

```sh
go test -race ./...
go run ./cmd/cpamc-simulator
# Release build (TeamCity, never GitHub Actions):
docker buildx build --platform linux/amd64,linux/arm64 \
  -t docker.io/kevynsax/cpamc-simulator:0.1.1 \
  -t docker.io/kevynsax/cpamc-simulator:latest --push .
```

The Dockerfile copies only runtime source into the build stage; the final scratch image contains only the unprivileged executable. Tests, development tools and user credentials never enter the runtime image.
