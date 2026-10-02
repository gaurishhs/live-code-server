# livecode

`livecode` is a small, read-only local file server for static website viewers hosted on Cloudflare Pages. It exposes a directory tree, text file contents, and filesystem changes over WebSocket. The UI remains a separate static site.

## Install

With Go installed:

```sh
go install github.com/gaurishhs/live-code-server/cmd/livecode@latest
```

Or download a binary from the project's GitHub Releases. Build locally with `make build`; cross-platform archives are produced by `make release`.

## Usage

```sh
livecode .
livecode ./my-project --port 9000
livecode ~/Projects/my-site --allow-origin https://viewer.example.com
```

The server listens on `127.0.0.1:8787` by default. Useful flags:

| Flag | Purpose |
| --- | --- |
| `--host` | Bind address (default `127.0.0.1`) |
| `--port` | Listen port |
| `--public` | Bind all interfaces |
| `--ignore` | Additional comma-separated ignore patterns |
| `--max-file-size` | Maximum readable file size in MB (default 10) |
| `--allow-origin` | Comma-separated allowed CORS origins; `*` is available for development |
| `--token` | Require `Authorization: Bearer <token>` for API and WebSocket access |
| `--tunnel` | Start a Cloudflare quick tunnel using installed `cloudflared` |
| `--config` | Read JSON settings (`host`, `port`, `token`, `allow_origin`, `ignore`, `max_file_size`) |
| `--verbose` | Enable verbose logging |
| `--version` | Print version |

The default ignored names are `.git`, `node_modules`, `dist`, `build`, `.astro`, `.cache`, and `.DS_Store`. The API is read-only and serves text files only.

## Cloudflare Tunnel

Automatic quick tunnel:

```sh
livecode . --tunnel
```

`cloudflared` must already be installed and on `PATH`; livecode never downloads it. Alternatively, run the CLI and tunnel separately:

```sh
livecode .
cloudflared tunnel --url http://127.0.0.1:8787
```

The `--tunnel-host` named tunnel option is not supported in this release. Quick tunnels use a generated `trycloudflare.com` hostname.

## Sharing

`livecode share [directory]` starts the local read-only livecode server, claims the temporary event sharing lease, and runs `cloudflared` with the tunnel token returned by the event service. The token and lease stay in memory and are not printed or saved. The CLI prints the public URL returned by the service for the other person to open in the live viewer.

Only one user can hold the event slot at a time. Press **Ctrl+C** to stop the tunnel, release the slot, and stop the local server.

```sh
export LIVECODE_EVENT_PASSWORD="your-event-password"
livecode share .
```

Configuration is available through flags or environment variables:

| Setting | Flag | Environment variable | Default |
| --- | --- | --- | --- |
| Event API URL | `--event-url` | `LIVECODE_EVENT_API` | `https://api.gaurishhs.xyz` |
| Event password | `--event-password` | `LIVECODE_EVENT_PASSWORD` | required |
| cloudflared executable | `--cloudflared` | `LIVECODE_CLOUDFLARED` | `cloudflared` |
| Browser origins | `--allow-origin` | `LIVECODE_ALLOW_ORIGIN` | none |

For example:

```sh
livecode share ./my-project --event-url https://api.gaurishhs.xyz
livecode share . --cloudflared /usr/local/bin/cloudflared
livecode share . --allow-origin https://viewer.example.com
```

For local frontend development, use its exact origin, such as `--allow-origin http://localhost:4321`. For a deployed viewer, allow its Pages origin. Browser API requests and WebSocket upgrades both check this setting; CORS is disabled by default for sharing.

The event service handles only claiming, renewing, and releasing the lease and returning the public URL and tunnel credential. Browser API and WebSocket traffic flows through Cloudflare Tunnel to the local livecode server; the Worker does not proxy those requests.

## API

- `GET /api/health` → `{"status":"ok"}`
- `GET /api/tree` → recursive tree, with paths relative to the selected root
- `GET /api/file?path=src/styles.css` → UTF-8/text content
- `WS /ws` → JSON events such as `{"type":"modified","path":"src/styles.css"}`

WebSocket browser clients that cannot set headers can use `/ws?token=...` when authentication is enabled. Use `wss://` behind the HTTPS tunnel. Events include `created`, `modified`, `deleted`, and `renamed`; clients should fetch changed content from `/api/file`.

## Architecture

```text
Cloudflare Pages (static viewer)
          ↓ HTTPS/WSS
     Cloudflare Tunnel
          ↓
       livecode
          ↓
  local filesystem
```

The CLI does not serve the Pages frontend. The browser UI calls the API directly.

## Security

The server only reads files under the selected root, blocks paths that escape it, refuses outside-root symlinks, and offers no write, upload, shell, or proxy endpoints. Configure `--allow-origin` for the Pages site's exact origin. CORS is disabled by default.

Exposing the CLI through a public tunnel makes the selected directory's readable files available to anyone who can reach the URL unless `--token` is set. Treat the token like a password and do not place it in logs or share it publicly. A token passed in a browser WebSocket query string may appear in browser or proxy URL logs; prefer a trusted viewer and protect access to that URL.
