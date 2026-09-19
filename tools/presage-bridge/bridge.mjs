// Presage SmartSpectra -> Lull bridge.
//
// SmartSpectra is SDK-only: iOS, Android, C++, Node/Electron. There is no REST
// endpoint that takes video and returns vitals, so something has to run the SDK
// against a camera. This is that something — it reads a face, decodes the
// metrics, and POSTs pulse and breathing to Lull's /api/vitals.
//
// It measures the MOTHER, not the baby. rPPG reads surface blood flow from a
// face; there is no optical path to a fetus through the abdominal wall. Maternal
// is the right role: it is the control that lets you say "every maternal number
// is normal AND the baby moved 41% less".
//
//   PRESAGE_API_KEY=... node bridge.mjs --lull http://localhost:8080

import {
  SmartSpectraSDK, ProcessingStatus, ValidationCode,
  SmartSpectraErrorCode, cardioMetrics, breathingMetrics, decodeMetrics,
} from "@smartspectra/node-sdk";

const arg = (name, fallback) => {
  const i = process.argv.indexOf("--" + name);
  return i > -1 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
};

const API_KEY = process.env.PRESAGE_API_KEY;
const LULL    = arg("lull", "http://localhost:8080").replace(/\/$/, "");
const EVERY   = Number(arg("interval", "5")) * 1000;
const CAMERA  = Number(arg("camera", "0"));

if (!API_KEY) {
  console.error("PRESAGE_API_KEY is not set. Get one at https://physiology.presagetech.com");
  process.exit(1);
}

const nameOf = (obj, v) => Object.keys(obj).find(k => obj[k] === v) ?? String(v);
const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a);

const sdk = new SmartSpectraSDK({
  apiKey: API_KEY,
  requestedMetrics: [...cardioMetrics, ...breathingMetrics],
  enableAccumulatedOutput: true,
  logLevel: 2, // warnings and errors; the SDK is chatty at info
});

let latest = { pulse: null, breathing: null, at: 0 };
let posted = 0, rejected = 0, lastHint = null;

sdk.on("error", (code, message, retryable) => {
  log(`error ${nameOf(SmartSpectraErrorCode, code)}: ${message}${retryable ? " (retryable)" : ""}`);
  if (code === SmartSpectraErrorCode.kAuthenticationFailed) {
    log("the API key was rejected — check PRESAGE_API_KEY");
    process.exit(1);
  }
});

sdk.on("processingStatus", s => log("status:", nameOf(ProcessingStatus, s)));

// Frame-quality feedback is the difference between "it is broken" and "move
// into the light", so it is surfaced rather than swallowed.
sdk.on("validationStatus", (code, _ts, hint) => {
  const n = nameOf(ValidationCode, code);
  if (n === lastHint) return;
  lastHint = n;
  if (code !== ValidationCode.kOk) log(`frame: ${n}${hint ? " — " + hint : ""}`);
  else log("frame: ok, measuring");
});

// The metrics arrive as a protobuf buffer; decodeMetrics turns it into the
// nested shape below. Field layout varies a little by SDK build, so each value
// is looked up defensively rather than assumed.
const pick = (obj, ...paths) => {
  for (const p of paths) {
    const v = p.split(".").reduce((o, k) => (o == null ? o : o[k]), obj);
    const last = Array.isArray(v) ? v[v.length - 1] : v;
    const n = last && typeof last === "object" ? last.value : last;
    if (typeof n === "number" && Number.isFinite(n) && n > 0) return n;
  }
  return null;
};

sdk.on("metrics", (buf) => {
  let m;
  try { m = decodeMetrics(buf); } catch { return; }
  const pulse = pick(m, "pulse.rate", "pulse.strict", "cardiac.rate");
  const breathing = pick(m, "breathing.rate", "breathing.strict", "respiration.rate");
  if (pulse || breathing) latest = { pulse, breathing, at: Date.now() };
});

async function push() {
  if (!latest.pulse && !latest.breathing) return;
  if (Date.now() - latest.at > 30_000) return; // stale: the face left the frame
  const body = {
    pulse_bpm: latest.pulse ?? 0,
    breathing_rpm: latest.breathing ?? 0,
    source: "presage",
  };
  try {
    const res = await fetch(`${LULL}/api/vitals`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (res.ok) {
      posted++;
      log(`-> lull  pulse=${body.pulse_bpm ? body.pulse_bpm.toFixed(1) : "-"} ` +
          `breathing=${body.breathing_rpm ? body.breathing_rpm.toFixed(1) : "-"}  (${posted} sent)`);
    } else {
      rejected++;
      log(`lull rejected it: HTTP ${res.status} ${(await res.text()).trim()}`);
    }
  } catch (e) {
    log("lull unreachable:", e.message);
  }
}

sdk.useCamera({ deviceIndex: CAMERA, fps: 30 });
log(`starting — camera ${CAMERA}, posting to ${LULL} every ${EVERY / 1000}s`);
log("sit facing the camera in even light; it needs a few seconds to lock on");
sdk.start();
const timer = setInterval(push, EVERY);

const shutdown = async () => {
  clearInterval(timer);
  log(`stopping — ${posted} readings sent, ${rejected} rejected`);
  try { await sdk.stopAsync(); } catch {}
  try { await sdk.destroy(); } catch {}
  process.exit(0);
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
