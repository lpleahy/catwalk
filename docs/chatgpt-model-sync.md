# ChatGPT model sync

The ChatGPT provider is backed by the ChatGPT/Codex subscription endpoint, not
the public OpenAI API. Its available model list is generated from the live
`/models` response and committed into Catwalk's provider metadata.

## Sync models

Run from the `catwalk` checkout:

```sh
scripts/sync-chatgpt-models
```

The wrapper calls `go run ./cmd/chatgpt` and prints the discovered model IDs
after regenerating `internal/providers/configs/chatgpt.json`.

Credential lookup order:

1. `--token`
2. `CHATGPT_ACCESS_TOKEN`
3. Crush's persisted ChatGPT OAuth config:
   - `$XDG_DATA_HOME/crush/crush.json`
   - `$XDG_CONFIG_HOME/crush/crush.json`
   - `~/.local/share/crush/crush.json`
   - `~/.config/crush/crush.json`
4. `~/.codex/auth.json` or `$CODEX_HOME/auth.json`

Use a short-lived token only through the environment or `--token`; do not write
access or refresh tokens into the repository.

## Defaults

The ChatGPT `/models` response does not define Catwalk defaults, so the wrapper
keeps them explicit:

```sh
DEFAULT_LARGE_MODEL=gpt-5.5 \
DEFAULT_SMALL_MODEL=gpt-5.4-mini \
scripts/sync-chatgpt-models
```

If a default is not present in the response, generation fails instead of
committing a provider config with broken defaults.

Other override knobs:

```sh
CLIENT_VERSION=0.130.0 \
API_ENDPOINT=https://chatgpt.com/backend-api/codex \
scripts/sync-chatgpt-models
```

## Verification

After syncing:

```sh
go test ./cmd/chatgpt ./pkg/catwalk ./internal/providers
```

Then build or run Crush from the sibling workspace so it uses this Catwalk
checkout. If the provider auto-update cache masks local metadata, launch Crush
with:

```sh
CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 crush
```
