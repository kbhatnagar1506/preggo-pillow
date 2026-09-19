package api

// The phone remote's markup. Kept as one constant so the binary stays a single
// file to copy to the Pi.
//
// Designed for a thumb in a dim room: large targets, high contrast, no small
// text, and a synthesised thump on every kick so the person holding it feels
// the connection between their tap and the number moving across the table.
const remotePage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d14">
<title>Preggo Pillow remote</title>
<style>
  :root{
    --ink:#f2f4f8; --dim:#8b93a7; --ground:#0b0d14; --panel:#151926;
    --edge:#232a3d; --calm:#5fd3c4; --warm:#f2a25c; --alarm:#e8615a;
  }
  *{box-sizing:border-box;-webkit-tap-highlight-color:transparent}
  /* Without this, iOS Safari holds every tap for ~300ms to see whether a
     second one is coming, and then treats the pair as double-tap-to-zoom and
     never delivers the second click. The arm-then-confirm call button needs
     exactly that gesture, so it never fired on an iPhone. */
  button,a,[role=button]{touch-action:manipulation}
  body{margin:0;background:var(--ground);color:var(--ink);
    font:16px/1.4 ui-sans-serif,-apple-system,"SF Pro Text",system-ui,sans-serif;
    padding:env(safe-area-inset-top) 16px calc(env(safe-area-inset-bottom) + 16px);
    max-width:520px;margin-inline:auto}
  header{display:flex;align-items:baseline;justify-content:space-between;padding:18px 2px 10px}
  h1{font-size:15px;margin:0;letter-spacing:.14em;text-transform:uppercase;color:var(--dim);font-weight:600}
  .who{font-size:12px;color:var(--dim);letter-spacing:.04em}
  .count{background:var(--panel);border:1px solid var(--edge);border-radius:18px;
    padding:22px 20px;text-align:center;margin-bottom:14px}
  .count b{display:block;font-size:76px;line-height:1;font-weight:700;
    font-variant-numeric:tabular-nums;letter-spacing:-.03em}
  .count span{display:block;font-size:12px;color:var(--dim);margin-top:8px;
    letter-spacing:.14em;text-transform:uppercase}
  .tally{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:16px}
  .tally div{background:var(--panel);border:1px solid var(--edge);border-radius:14px;
    padding:12px;text-align:center}
  .tally strong{display:block;font-size:30px;font-variant-numeric:tabular-nums}
  .tally small{color:var(--dim);font-size:11px;letter-spacing:.1em;text-transform:uppercase}
  .grid{display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px;margin-bottom:10px}
  button{font:inherit;font-weight:600;color:var(--ink);background:var(--panel);
    border:1px solid var(--edge);border-radius:16px;padding:20px 8px;width:100%;
    cursor:pointer;transition:transform .06s ease,background .15s ease}
  button:active{transform:scale(.96);background:#1d2334}
  button:focus-visible{outline:2px solid var(--calm);outline-offset:2px}
  .wide{grid-column:1/-1}
  .kick{border-color:#2c3550}
  .kick b{display:block;font-size:22px}
  .kick small{color:var(--dim);font-size:11px;letter-spacing:.08em;text-transform:uppercase}
  .shake{border-color:#3a3350;color:var(--warm)}
  .felt{border-color:#2a4a46;color:var(--calm)}
  .beat{border-color:#3d2b44;color:#d9a7e0}
  .beat b{display:block;font-size:17px}
  .beat small{display:block;color:var(--dim);font-size:11px;letter-spacing:.08em;
    text-transform:uppercase;margin-top:4px}
  .beat.on{background:#241a2b;border-color:#6d4a78}
  .beat.on b{animation:pulse 430ms ease-out infinite}
  @keyframes pulse{0%{transform:scale(1)}10%{transform:scale(1.14)}26%{transform:scale(1.02)}
    34%{transform:scale(1.08)}55%{transform:scale(1)}}
  @media (prefers-reduced-motion:reduce){.beat.on b{animation:none}}
  .call{border-color:#5a2f2d;color:var(--alarm);font-size:17px}
  .call.armed{background:var(--alarm);color:#160a09;border-color:var(--alarm)}
  .note{color:var(--dim);font-size:12.5px;line-height:1.5;padding:4px 2px 0}
  .log{margin-top:14px;font-size:12px;color:var(--dim);min-height:2.6em;
    font-variant-numeric:tabular-nums}
  .dot{display:inline-block;width:7px;height:7px;border-radius:50%;
    background:var(--dim);margin-right:7px;vertical-align:middle}
  .dot.on{background:var(--calm)}
</style>

<header>
  <h1>Preggo Pillow &middot; remote</h1>
  <div class="who"><span class="dot" id="dot"></span><span id="link">connecting</span></div>
</header>

<div class="count"><b id="detected">0</b><span>movements detected</span></div>

<div class="tally">
  <div><strong id="fired">0</strong><small>fired</small></div>
  <div><strong id="felt">0</strong><small>she felt</small></div>
</div>

<div class="grid">
  <button class="kick" data-kick="weak"><b>&bull;</b><small>weak</small></button>
  <button class="kick" data-kick="medium"><b>&bull;&bull;</b><small>medium</small></button>
  <button class="kick" data-kick="strong"><b>&bull;&bull;&bull;</b><small>strong</small></button>
</div>

<div class="grid">
  <button class="shake wide" id="shake">Maternal movement &mdash; both sensors</button>
</div>

<div class="grid">
  <button class="felt wide" id="felt-btn">I felt that</button>
</div>

<div class="grid">
  <button class="beat wide" id="beat">
    <b id="beatlabel">Fetal heartbeat</b>
    <small id="beatrate">142 bpm &middot; tap to start</small>
  </button>
</div>

<div class="grid">
  <button class="call wide" id="call">Call the emergency contact</button>
</div>

<p class="note" id="callnote">Places a real phone call to the emergency contact.</p>
<div class="log" id="log"></div>

<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  var log = function(m){ $("log").textContent = m; };

  // A synthesised thump, so the person holding the phone feels their tap land
  // instead of only seeing a number change on a screen across the room.
  var ac = null;
  function thump(gain, dur){
    try {
      ac = ac || new (window.AudioContext || window.webkitAudioContext)();
      if (ac.state === "suspended") ac.resume();
      var t = ac.currentTime;
      var o = ac.createOscillator(), g = ac.createGain();
      o.type = "sine";
      o.frequency.setValueAtTime(150, t);
      o.frequency.exponentialRampToValueAtTime(48, t + dur);
      g.gain.setValueAtTime(0.0001, t);
      g.gain.exponentialRampToValueAtTime(gain, t + 0.012);
      g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
      o.connect(g); g.connect(ac.destination);
      o.start(t); o.stop(t + dur + 0.05);
    } catch (e) {}
    if (navigator.vibrate) navigator.vibrate(Math.round(dur * 120));
  }

  function post(url){
    return fetch(url, { method: "POST" }).then(function(r){ return r.json().catch(function(){ return {}; }); });
  }

  var STRENGTH = { weak:[0.18,0.10], medium:[0.34,0.16], strong:[0.6,0.24] };
  Array.prototype.forEach.call(document.querySelectorAll("[data-kick]"), function(b){
    b.addEventListener("click", function(){
      var s = b.getAttribute("data-kick"), p = STRENGTH[s];
      thump(p[0], p[1]);
      post("/api/kick?strength=" + s);
      log(s + " kick fired");
    });
  });

  $("shake").addEventListener("click", function(){
    thump(0.5, 0.42);
    post("/api/fool");
    log("maternal movement on all nodes - the count should NOT move");
  });

  $("felt-btn").addEventListener("click", function(){
    thump(0.12, 0.07);
    post("/api/press?source=remote");
    log("marked: she felt that one");
  });

  // Fetal heartbeat, synthesised. Normal fetal range is 110-160 bpm, which is
  // roughly twice an adult's -- that fast double-thump is what makes people
  // who have heard a scan recognise it instantly.
  //
  // A cardiac cycle is two sounds, not one: S1 ("lub") is the valves closing at
  // the start of systole, S2 ("dub") follows at about a third of the cycle and
  // is shorter and higher. Spacing them evenly sounds like a machine; spacing
  // them at S1 then S1+0.32 sounds like a heart.
  var BPM = 142;
  var beatTimer = null;

  function sound(f0, f1, dur, gain){
    try {
      ac = ac || new (window.AudioContext || window.webkitAudioContext)();
      if (ac.state === "suspended") ac.resume();
      var t = ac.currentTime;
      var o = ac.createOscillator(), g = ac.createGain(), lp = ac.createBiquadFilter();
      lp.type = "lowpass"; lp.frequency.value = 320;
      o.type = "sine";
      o.frequency.setValueAtTime(f0, t);
      o.frequency.exponentialRampToValueAtTime(f1, t + dur);
      g.gain.setValueAtTime(0.0001, t);
      g.gain.exponentialRampToValueAtTime(gain, t + 0.010);
      g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
      o.connect(lp); lp.connect(g); g.connect(ac.destination);
      o.start(t); o.stop(t + dur + 0.05);
    } catch (e) {}
  }

  function lubDub(){
    var period = 60 / BPM;
    sound(92, 42, 0.11, 0.55);                                  // S1, lub
    setTimeout(function(){ sound(120, 58, 0.075, 0.34); },       // S2, dub
               Math.round(period * 0.32 * 1000));
    if (navigator.vibrate) navigator.vibrate([26, Math.round(period*320)-26, 16]);
  }

  $("beat").addEventListener("click", function(){
    var b = $("beat");
    if (beatTimer) {
      clearInterval(beatTimer); beatTimer = null;
      b.classList.remove("on");
      $("beatlabel").textContent = "Fetal heartbeat";
      $("beatrate").innerHTML = BPM + " bpm &middot; tap to start";
      log("heartbeat stopped");
      return;
    }
    b.classList.add("on");
    $("beatlabel").textContent = "Beating";
    $("beatrate").innerHTML = BPM + " bpm &middot; tap to stop";
    lubDub();
    beatTimer = setInterval(lubDub, Math.round(60000 / BPM));
    log("fetal heartbeat at " + BPM + " bpm");
  });

  // One tap places the call.
  //
  // This started as an arm-then-confirm button, on the reasoning that a real
  // phone call should not fire on a stray touch. On a phone, in a demo, that
  // reasoning was wrong twice over: iOS ate the second tap as a zoom gesture,
  // and even once that was fixed, a button you have to hit twice is a button
  // that fails in front of an audience. The guard that stays is the one that
  // matters - the call cannot be fired twice while one is already going out.
  var calling = false;
  $("call").addEventListener("click", function(){
    var btn = $("call");
    if (calling) { log("already calling"); return; }
    calling = true;
    btn.classList.add("armed");
    btn.textContent = "Calling\u2026";
    log("placing call");
    post("/api/call").then(function(j){
      if (j && j.ok) {
        btn.textContent = "Called \u2713";
        log("calling " + j.to + " - " + (j.status || "queued"));
      } else {
        btn.textContent = "Call failed";
        log("call failed: " + ((j && j.error) || "unknown"));
      }
      release(btn);
    }).catch(function(e){
      btn.textContent = "Call failed";
      log("call failed: " + e);
      release(btn);
    });
  });
  function release(btn){
    setTimeout(function(){
      calling = false;
      btn.classList.remove("armed");
      btn.textContent = "Call the emergency contact";
    }, 4000);
  }

  // Live counts. The detected number is the one that matters: it comes from
  // detections only, never from commands, so it cannot be inflated by pressing
  // buttons on this page.
  var detected = 0, fired = 0, felt = 0;
  function connect(){
    var es = new EventSource("/api/stream");
    es.onopen = function(){ $("dot").className = "dot on"; $("link").textContent = "live"; };
    es.onerror = function(){ $("dot").className = "dot"; $("link").textContent = "reconnecting"; };
    es.onmessage = function(ev){
      var e; try { e = JSON.parse(ev.data); } catch (x) { return; }
      if (e.kind === "detection") { detected++; $("detected").textContent = detected; thump(0.08, 0.06); }
      else if (e.kind === "command") { fired++; $("fired").textContent = fired; }
      else if (e.kind === "press")   { felt++;  $("felt").textContent = felt; }
      else if (e.kind === "call")    { log("call " + (e.data && e.data.status)); }
    };
  }
  connect();
})();
</script>
`
