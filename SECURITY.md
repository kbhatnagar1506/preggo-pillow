# Security

## This is not a medical device

Preggo Pillow counts fetal movement and reports change against a baseline. It
does not diagnose, it is not validated against any clinical reference, and it
has never been tested on a real pregnancy. Nothing it produces is medical
advice. If you are worried about your baby's movements, contact your maternity
unit today — not this software.

## Reporting a vulnerability

Open a GitHub issue for anything non-sensitive. For something that should not
be public, use GitHub's private vulnerability reporting on this repository.

## What the threat model actually is

The interesting asset here is not a database of users. It is **one pregnancy's
movement record**, plus a phone number that a button can dial.

**The record needs a session.** Gating the pages is not enough — for a while
`/dashboard` redirected anonymous visitors while `/report` served the entire
clinical summary to anyone who could reach the port. Routes are now split by
what they *touch*, not by whether they render HTML. See "Sign-in, and who can
read the record" in the README.

**The phone remote is unauthenticated on purpose.** It is a bedside device for
one pregnancy and the path is the identity. That is fine while the button fires
a servo. It stops being fine when a live Vapi key is on a public host, so
`CALL_TOKEN` gates `/api/call`, and the page hands the token to the button only
if the visitor already had it.

**It fails closed.** If sign-in is configured and the session store cannot be
created, the server refuses to start rather than serving the record with
authentication silently disabled.

## Running it safely

- Set the Auth0 variables. Without them **every page is open**, which is what a
  bench demo wants and what you must not ship. `/settings` says so plainly when
  it detects that state.
- `APP_BASE_URL` must be HTTPS anywhere that is not localhost. Auth0 will not
  accept a plain-HTTP callback, and a session cookie over HTTP is not a session.
- `.env` is gitignored. Keep it that way. `.env.example` documents every
  variable and contains no values.
- Rotate `SESSION_SECRET` and `DATA_ENCRYPTION_KEY` if they have ever been
  pasted anywhere. Rotating `DATA_ENCRYPTION_KEY` invalidates existing
  encrypted fields and every session, which is the intended effect.

## What leaves the device

Nothing, unless you configure it to. Each integration is off without its key
and says so at startup. When configured, `/settings` lists every outbound
destination, built from what is actually wired rather than from a sentence
someone typed once.
