# Preggo Pillow — landing page

The public marketing page, as a single static file. No build step, no framework,
no dependencies: one `index.html` with its CSS inline and one Google Fonts link.

## Why it is separate from the app

The dashboard needs a Go process, a database and, at demo time, a sensor on a
Raspberry Pi. None of that runs on Vercel. The landing page has no such needs,
so it deploys anywhere static hosting exists and points at wherever the app is
actually running.

The same file is also embedded in the Go binary and served at `/`, so a local
run gives you the whole product on one origin.

## Deploying

```bash
cd site
npx vercel deploy --prod
```

Vercel serves the directory as-is; `vercel.json` only sets clean URLs and
security headers.

## Deploying to Vercel

Point Vercel at **this directory**, not the repository root. The root is a Go
module; Vercel cannot run a long-lived Go server with an SSE stream, so there is
nothing for it to build there.

| Setting | Value |
|---|---|
| Root Directory | `site` |
| Framework Preset | Other |
| Build Command | *(none)* |
| Output Directory | *(leave empty)* |
| Install Command | *(none)* |

```bash
cd site && npx vercel deploy --prod
```

## Where the app lives

`index.html` here is **byte-identical** to `web/static/index.html`, the copy the
Go binary embeds and serves at `/`. Regenerate it with `make site` from the repo
root; a test fails if the two drift.

Its links are plain paths — `/dashboard`, `/history` and so on — which are
correct when the binary serves the page. On static hosting the same paths are
redirects, defined in `vercel.json`:

```json
{ "source": "/dashboard", "destination": "https://.../dashboard", "permanent": false }
```

Point them wherever the app is actually running. They are temporary redirects
on purpose: the app URL will move, and a browser that cached a 301 to a dead
Cloud Run revision is a bad afternoon.

This replaced a `{{APP_URL}}` placeholder that a documented `sed` step was
supposed to substitute before every deploy. Nothing ever ran it, so the live
Get Started buttons linked to a literal `{{APP_URL}}/dashboard`. A manual step
that is only needed at deploy time is a step that gets skipped at deploy time.
