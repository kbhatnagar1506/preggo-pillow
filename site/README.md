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

## Before you deploy

`index.html` here contains `{{APP_URL}}` placeholders where the app version
links to `/dashboard`. Replace them with wherever the dashboard is reachable:

```bash
sed -i '' 's|{{APP_URL}}|https://your-app-host|g' index.html
```

Leave them unreplaced and the Get Started buttons go nowhere, which is worse
than pointing at a holding page.
