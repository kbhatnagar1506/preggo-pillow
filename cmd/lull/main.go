// Command lull runs the whole system: sensor ingest, kick detection, storage,
// and the dashboard.
//
// It runs in two modes.
//
//	-source=sim    synthetic sensors. Everything works with no hardware at all.
//	-source=serial read real accelerometer nodes over USB from the Arduinos.
//
// Build the entire product against -source=sim first. Then, once real sensor
// traces are recorded at hour 8, the software never has to depend on live
// hardware again. That is the insurance policy: if a cable dies during
// judging, you still have a demo.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kbhatnagar1506/lull/internal/api"
	"github.com/kbhatnagar1506/lull/internal/auth"
	"github.com/kbhatnagar1506/lull/internal/clinical"
	"github.com/kbhatnagar1506/lull/internal/detect"
	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/maternal"
	"github.com/kbhatnagar1506/lull/internal/memory"
	"github.com/kbhatnagar1506/lull/internal/sensor"
	"github.com/kbhatnagar1506/lull/internal/store"
	"github.com/kbhatnagar1506/lull/internal/tiger"
	"github.com/kbhatnagar1506/lull/internal/vitals"
	"github.com/kbhatnagar1506/lull/internal/voice"
	"github.com/kbhatnagar1506/lull/web"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		dbPath   = flag.String("db", "data/lull.db", "sqlite path")
		source   = flag.String("source", "sim", "sensor source: sim | serial | phone | i2c")
		seed     = flag.Bool("seed", true, "seed a simulated 14-night baseline if empty")
		sampleHz = flag.Int("hz", 100, "sensor sample rate (sim only)")

		buses = flag.String("buses", "abdo_a=1,abdo_b=3,ref=4",
			"accelerometer I2C buses as node=busnumber (one bus each: identical "+
				"chips share an address and would collide)")
		owner     = flag.String("owner", "krishnabhatnagar", "path the phone remote is served at, e.g. /krishnabhatnagar")
		callTo    = flag.String("call-to", "", "number the escalation call reaches; falls back to VAPI_TO_NUMBER")
		vapiKey   = flag.String("vapi-key", "", "Vapi PRIVATE api key; falls back to VAPI_API_KEY")
		vapiFrom  = flag.String("vapi-phone-id", "", "Vapi phoneNumberId to call from; falls back to VAPI_PHONE_NUMBER_ID")
		vapiVoice = flag.String("vapi-voice", "", "11Labs voice id; falls back to VAPI_VOICE_ID")

		replayPath  = flag.String("replay", "", "replay a recorded trace instead of live sensors — the demo fallback")
		replaySpeed = flag.Float64("replay-speed", 1, "playback speed; 60 puts an hour of night into a minute")
		replayLoop  = flag.Bool("replay-loop", true, "restart the trace when it ends")
		recordPath  = flag.String("record", "", "write every reading to this file, so tonight becomes tomorrow's fallback")

		knobEnable  = flag.Bool("knob", false, "read a Grove rotary angle sensor as the human \"I felt it\" tally")
		knobBus     = flag.Int("knob-bus", 1, "I2C bus the Grove Base HAT ADC is on")
		knobChannel = flag.Int("knob-channel", -1, "ADC channel the dial is on; -1 finds it by asking you to turn it")

		motorBus  = flag.Int("motor-bus", 1, "I2C bus the Grove motor driver is on")
		motorAddr = flag.Int("motor-addr", 0x0F, "Grove motor driver address (DIP-switch selectable)")

		phones = flag.String("phones", "", "phyphox handsets as node=host pairs, e.g. "+
			"\"abdo_a=192.168.1.21,abdo_b=192.168.1.22,ref=192.168.1.23\" "+
			"(iOS serves on port 80, Android on 8080)")
		phonePollHz = flag.Int("phone-poll-hz", 10, "how often to drain each handset's buffer")

		ports     = flag.String("ports", "auto", "serial devices, comma separated, or \"auto\" to discover")
		baud      = flag.Int("baud", 115200, "serial baud rate, must match the sketch")
		listPorts = flag.Bool("list-ports", false, "print candidate serial devices and exit")

		acousticThreshold = flag.Float64("acoustic-threshold", 0, "acoustic spike threshold above rolling floor; 0 picks a default per source")
		requireAcoustic   = flag.Bool("require-acoustic", true, "a detection must be confirmed by the contact mic")
		kickThreshold     = flag.Float64("threshold", 0, "detection residual in g; 0 uses the default")

		bbKey   = flag.String("backboard-key", "", "Backboard API key; falls back to BACKBOARD_API_KEY")
		bbID    = flag.String("backboard-assistant", "", "existing Backboard assistant id; found or created by name if empty")
		envFile = flag.String("env", ".env", "file of KEY=VALUE lines to load before starting")

		tigerURL = flag.String("tiger-url", "", "TigerData/Timescale connection string; falls back to TIGER_DATABASE_URL")
		deviceID = flag.String("device", "lull-01", "which physical unit this is, so one database can hold many")

		demo = flag.Bool("demo", true, "keep the seeded night fixed so the override demo is stable; "+
			"set false to roll real detections into tonight's count")
	)
	flag.Parse()
	loadEnv(*envFile)

	if *listPorts {
		found, err := sensor.DiscoverPorts()
		if err != nil {
			log.Fatalf("discover ports: %v", err)
		}
		if len(found) == 0 {
			log.Println("no serial devices found. Is the Arduino plugged in and the sketch flashed?")
			return
		}
		for _, p := range found {
			log.Printf("  %s", p)
		}
		return
	}

	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if *seed {
		if err := seedBaseline(st); err != nil {
			log.Printf("seed baseline: %v", err)
		}
	}

	// --- narrative memory (Backboard) ----------------------------------
	// The device remembers the numbers. Backboard remembers the pregnancy.
	// Entirely optional: with no key, everything below degrades to a log line.
	memCtx, memCancel := context.WithTimeout(context.Background(), 30*time.Second)
	mem, _ := memory.New(memCtx, memory.Options{APIKey: *bbKey, AssistantID: *bbID, Name: "Lull"})
	memCancel()
	defer mem.Close()
	rec := memory.NewRecorder(mem)

	// --- longitudinal store (TigerData) --------------------------------
	// SQLite above is authoritative and offline. Tiger holds the long record:
	// hypertables for the raw series, and a continuous aggregate that IS the
	// nightly baseline.
	tgCtx, tgCancel := context.WithTimeout(context.Background(), 30*time.Second)
	tg, err := tiger.Open(tgCtx, tiger.Options{URL: *tigerURL, Device: *deviceID})
	tgCancel()
	if err != nil {
		log.Printf("tiger: %v", err)
	}
	defer tg.Close()

	// --- clinical summary (Gemini on Vertex, via LiteLLM) ---------------
	clin := clinical.New(clinical.Options{})

	hub := api.NewHub()

	// --- sensor source -------------------------------------------------
	var src sensor.Source
	var servo kicker.Servo
	var fool func()
	defaultAcoustic := 10.0

	switch *source {
	case "sim":
		sim := sensor.NewSim(*sampleHz)
		src = sim
		servo = kicker.SimServo{Inject: sim.InjectKick}
		fool = func() { sim.InjectMaternal(0.45) }
		log.Printf("sensor source: SIMULATED at %d Hz", *sampleHz)

	case "serial":
		devs, err := resolvePorts(*ports)
		if err != nil {
			log.Fatalf("%v", err)
		}
		log.Printf("sensor source: SERIAL on %v at %d baud", devs, *baud)
		ser, err := sensor.NewSerial(devs, *baud, "")
		if err != nil {
			log.Fatalf("serial: %v", err)
		}
		src = ser
		servo = kicker.SerialServo{Send: ser.Kick}
		// On real hardware there is nothing to fake: shake the phantom with
		// your hand. That is a better demo anyway.
		fool = nil
		// The Grove sound sensor is a 0-1023 analog envelope, so its spike
		// scale is nothing like the simulator's.
		defaultAcoustic = 60.0

	case "replay":
		if *replayPath == "" {
			log.Fatalf(`-source replay needs -replay <file>`)
		}
		tr, err := sensor.LoadTrace(*replayPath)
		if err != nil {
			log.Fatalf("replay: %v", err)
		}
		// Refuse a trace with no reference node. Without it there is nothing to
		// subtract, so maternal movement would be counted as fetal — and a
		// fallback that quietly reports wrong numbers is worse than no fallback.
		var hasRef bool
		for _, n := range tr.Nodes() {
			if n == sensor.NodeRef {
				hasRef = true
			}
		}
		if !hasRef {
			log.Fatalf("replay: %s has no reference node (has %v) — reference "+
				"subtraction is impossible and maternal movement would be counted "+
				"as fetal", *replayPath, tr.Nodes())
		}
		log.Printf("sensor source: REPLAY of %s", *replayPath)
		log.Printf("  %d readings, %v of recording, nodes %v, %.0fx speed, loop=%v",
			len(tr.Readings), tr.Duration().Round(time.Second), tr.Nodes(), *replaySpeed, *replayLoop)
		src = sensor.NewReplaySource(tr, *replaySpeed, *replayLoop)
		servo = nil
		fool = nil
		if *requireAcoustic && len(tr.Acoustics) == 0 {
			log.Printf("  trace has no acoustic samples: disabling the acoustic gate")
			*requireAcoustic = false
		}

	case "i2c":
		log.Printf("sensor source: I2C on the Pi")
		s, sv, clean, err := hardwareSource(*buses, *sampleHz, *motorBus, uint8(*motorAddr))
		if err != nil {
			log.Fatalf("%v", err)
		}
		src = s
		servo = sv
		fool = nil
		if clean != nil {
			defer clean()
		}
		// No contact mic: the Grove HAT's ADC does not enumerate on a Pi 5, so
		// leaving the acoustic gate on would veto every detection.
		if *requireAcoustic {
			log.Printf("  no contact mic on this path: disabling the acoustic confirmation gate")
		}
		*requireAcoustic = false

	case "phone":
		handsets, err := parsePhones(*phones)
		if err != nil {
			log.Fatalf("%v", err)
		}
		log.Printf("sensor source: PHONE (phyphox) — %d handsets, polling at %d Hz",
			len(handsets), *phonePollHz)
		for _, h := range handsets {
			log.Printf("  %-7s %s", h.Node, h.BaseURL())
		}
		ph := sensor.NewPhoneSource(handsets, *phonePollHz)
		src = ph
		// There is no servo on this path and nothing to fake: tap the pod.
		servo = nil
		fool = nil
		// Phones carry no contact mic, so the acoustic gate would veto every
		// detection. Turn it off rather than silently detecting nothing.
		if *requireAcoustic {
			log.Printf("  no contact mic on this path: disabling the acoustic confirmation gate")
		}
		*requireAcoustic = false

	default:
		log.Fatalf("unknown -source %q", *source)
	}
	defer src.Close()

	// Recording tees the live source to a file. Doing it here rather than
	// inside each source means every path — sim, serial, phone, i2c — can be
	// recorded with the same flag.
	if *recordPath != "" {
		tw, err := sensor.NewTraceWriter(*recordPath)
		if err != nil {
			log.Fatalf("record: %v", err)
		}
		defer tw.Close()
		src = sensor.NewRecorder(src, tw)
		log.Printf("recording every reading to %s", *recordPath)
	}

	// --- maternal metrics ----------------------------------------------
	// The override argument is "every maternal number is normal and the baby
	// still moved 41% less". That only lands if these came from a sensor.
	mat := maternal.NewTracker()

	// --- detector ------------------------------------------------------
	cfg := detect.DefaultConfig()
	cfg.RequireAcoustic = *requireAcoustic
	if *acousticThreshold > 0 {
		cfg.AcousticThreshold = *acousticThreshold
	} else {
		cfg.AcousticThreshold = defaultAcoustic
	}
	if *kickThreshold > 0 {
		cfg.Threshold = *kickThreshold
	}
	log.Printf("detector: threshold=%.3fg acoustic=%.1f require_acoustic=%v",
		cfg.Threshold, cfg.AcousticThreshold, cfg.RequireAcoustic)
	det := detect.New(cfg)

	// --- kicker (the phantom) ------------------------------------------
	kk := kicker.New(
		servo,
		func(t time.Time, strength string) {
			if err := st.InsertCommand(t, strength); err != nil {
				log.Printf("command insert: %v", err)
			}
			// Commands are broadcast so the operator console can show them.
			// The dashboard count NEVER uses this: the count comes from
			// detections only.
			hub.Broadcast(api.Event{Kind: "command", Data: map[string]any{
				"t_ms": t.UnixMilli(), "strength": strength,
			}})
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pumpSensors(ctx, src, det, mat, hub)
	go pumpDetections(ctx, det, st, hub)
	go recordNightly(ctx, st, mat, rec)
	go syncToTiger(ctx, st, mat, tg)
	if !*demo {
		go rollUpNights(ctx, st)
	} else {
		log.Println("demo mode: tonight's seeded count stays fixed (-demo=false to roll up live detections)")
	}

	// --- http ----------------------------------------------------------
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		log.Fatalf("embed web: %v", err)
	}

	// --- Auth0 + sessions --------------------------------------------
	// Auth0 proves identity; the app keeps its own session so it can enforce
	// idle logout and revoke access without waiting for a token to expire.
	var authSvc *auth.Service
	{
		dataKey, _ := base64.StdEncoding.DecodeString(os.Getenv("DATA_ENCRYPTION_KEY"))
		cfg := auth.Config{
			Domain:       os.Getenv("AUTH0_DOMAIN"),
			ClientID:     os.Getenv("AUTH0_CLIENT_ID"),
			ClientSecret: os.Getenv("AUTH0_CLIENT_SECRET"),
			AppBaseURL:   firstNonEmpty(os.Getenv("APP_BASE_URL"), "http://localhost"+*addr),
		}
		sessions, serr := auth.NewStore(st.DB())
		if serr != nil {
			log.Printf("auth: session store unavailable: %v", serr)
		} else {
			authSvc = &auth.Service{
				Provider:      auth.NewProvider(cfg),
				Sessions:      sessions,
				SessionSecret: os.Getenv("SESSION_SECRET"),
				DataKey:       dataKey,
			}
			switch {
			case authSvc.Enabled():
				log.Printf("auth: Auth0 ready at %s", cfg.Domain)
				log.Printf("auth: register this callback in Auth0 -> %s", cfg.RedirectURI())
				// Auth0 compares redirect_uri byte-for-byte against the
				// registered list. If APP_BASE_URL does not match where we are
				// actually listening, every login fails at the callback with a
				// generic error and nothing in our logs explains why.
				if u, uerr := url.Parse(cfg.AppBaseURL); uerr == nil {
					want := u.Port()
					if want == "" {
						if u.Scheme == "https" {
							want = "443"
						} else {
							want = "80"
						}
					}
					got := strings.TrimPrefix(*addr, ":")
					if want != got {
						log.Printf("auth: WARNING APP_BASE_URL is %s but we are listening on :%s.", cfg.AppBaseURL, got)
						log.Printf("auth: Auth0 will send the browser to port %s and the login will fail.", want)
						log.Printf("auth: either run with -addr :%s, or set APP_BASE_URL=http://localhost:%s", want, got)
					}
				}
			case cfg.Domain == "":
				log.Printf("auth: disabled (no AUTH0_DOMAIN); every page is open")
			case len(dataKey) != 32:
				log.Printf("auth: disabled — DATA_ENCRYPTION_KEY must be 32 bytes base64, got %d", len(dataKey))
			case os.Getenv("SESSION_SECRET") == "":
				log.Printf("auth: disabled — SESSION_SECRET is not set")
			default:
				log.Printf("auth: disabled — incomplete Auth0 configuration")
			}
		}
	}

	// Voice escalation. Unconfigured simply hides the capability; it must
	// never stop the dashboard from serving.
	vc := voice.New(
		firstNonEmpty(*vapiKey, os.Getenv("VAPI_API_KEY")),
		firstNonEmpty(*vapiFrom, os.Getenv("VAPI_PHONE_NUMBER_ID")),
		firstNonEmpty(*vapiVoice, os.Getenv("VAPI_VOICE_ID")),
	)
	to := firstNonEmpty(*callTo, os.Getenv("VAPI_TO_NUMBER"))
	if vc.Enabled() {
		log.Printf("voice: Vapi ready, calls go to %s", to)
	} else {
		log.Printf("voice: not configured (set VAPI_API_KEY and VAPI_PHONE_NUMBER_ID)")
	}
	if *owner != "" {
		log.Printf("phone remote: http://localhost%s/%s", *addr, *owner)
	}

	// Maternal vitals: the control arm of the argument. Fed by the Presage
	// bridge, or by anything else that can POST /api/vitals.
	vitalStore := vitals.NewStore(2048)

	srv := &api.Server{
		Hub:      hub,
		Store:    st,
		Vitals:   vitalStore,
		Voice:    vc,
		CallTo:   to,
		Owner:    *owner,
		Auth:     authSvc,
		Kicker:   kk,
		Web:      http.FS(sub),
		Fool:     fool,
		Memory:   mem,
		Recorder: rec,
		Clinical: clin,
		Maternal: func() map[string]any {
			st := mat.Stats()
			return map[string]any{
				"posture":         st.Posture,
				"supine_minutes":  st.SupineMinutes,
				"respiration_rpm": st.RespirationRPM,
				"wake_events":     st.WakeEvents,
				"snore_percent":   st.SnorePercent,
				"ready":           st.Ready,
			}
		},
	}

	// --- the dial: the human half of the blind test -------------------
	// One notch per movement she noticed. Recorded to `presses`, the same
	// table the on-screen button writes to, so the three-way comparison
	// (fired / detected / felt) does not care which input was used.
	if *knobEnable {
		stop, err := startKnob(ctx, *knobBus, *knobChannel, func(t time.Time, settled int) {
			if err := st.InsertPress(t, "knob"); err != nil {
				log.Printf("knob press insert: %v", err)
			}
			hub.Broadcast(api.Event{Kind: "press", Data: map[string]any{
				"t_ms": t.UnixMilli(), "source": "knob", "mv": settled,
			}})
			log.Printf("knob: felt-movement mark recorded")
		})
		if err != nil {
			// Not fatal. The dial is one input to one of three records; losing
			// it must not take the detector down with it.
			log.Printf("knob unavailable: %v", err)
		} else {
			defer stop()
			log.Printf("knob: recording felt-movement marks")
		}
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind BEFORE announcing. ListenAndServe logs success then fails, so a
	// port already in use prints "listening on :8080" immediately followed by
	// the error — and anyone reading the top of the log believes it started.
	ln, lerr := net.Listen("tcp", *addr)
	if lerr != nil {
		log.Fatalf("cannot listen on %s: %v\n\nSomething else is already using that port. Find it with:\n  lsof -nP -iTCP%s -sTCP:LISTEN", *addr, lerr, *addr)
	}
	log.Printf("lull listening on http://localhost%s", *addr)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

// loadEnv reads KEY=VALUE lines so the API key lives in a 0600 file rather than
// in a shell history or a command line, where it would be visible to every
// process on the machine.
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // absent is normal
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

// recordNightly writes the salient events to narrative memory: what the night
// came to against her own baseline, her own state, and any alert.
//
// Once per night, never per reading. A memory per detected kick would be
// thousands of identical rows, and recall over that returns nothing useful.
func recordNightly(ctx context.Context, st *store.Store, mat *maternal.Tracker, rec *memory.Recorder) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()

	write := func() {
		nights, err := st.Nights(14)
		if err != nil || len(nights) == 0 {
			return
		}
		baseline, err := st.Baseline(1)
		if err != nil || baseline <= 0 {
			return
		}
		latest := nights[len(nights)-1]
		dev := (float64(latest.KickCount) - baseline) / baseline * 100

		rec.Night(latest.Date, latest.KickCount, baseline, dev)

		if ms := mat.Stats(); ms.Ready {
			rec.Maternal(latest.Date, ms.Posture, ms.SupineMinutes,
				ms.RespirationRPM, ms.SnorePercent, ms.WakeEvents)
		}

		// Two consecutive nights, never one: fetal sleep cycles run 20-40
		// minutes and babies have genuinely quiet nights.
		if dev <= -25 && len(nights) >= 2 &&
			float64(nights[len(nights)-2].KickCount) <= baseline*0.75 {
			rec.Alert(latest.Date, latest.KickCount, baseline, dev, 2)
		}
	}

	write() // once at startup so a demo has something to recall immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			write()
		}
	}
}

// syncToTiger ships detections to the longitudinal store in batches.
//
// Local-first: SQLite is written first and is authoritative, and the watermark
// only advances after the remote write succeeds. Being offline costs a larger
// catch-up batch and nothing else.
func syncToTiger(ctx context.Context, st *store.Store, mat *maternal.Tracker, tg *tiger.Store) {
	if !tg.Enabled() {
		return
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			rows, err := st.UnsyncedDetections(2000)
			if err != nil || len(rows) == 0 {
				continue
			}
			batch := make([]tiger.Detection, 0, len(rows))
			for _, r := range rows {
				batch = append(batch, tiger.Detection{
					T: r.T, Residual: r.Residual, Confidence: r.Confidence, Acoustic: r.Acoustic,
				})
			}
			sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			if err := tg.SyncDetections(sctx, batch); err != nil {
				cancel()
				log.Printf("tiger: sync failed (%v), will retry", err)
				continue
			}
			if err := st.MarkSynced(rows[len(rows)-1].ID); err != nil {
				log.Printf("tiger: watermark: %v", err)
			}
			if ms := mat.Stats(); ms.Ready {
				_ = tg.RecordMaternal(sctx, time.Now(), ms.Posture, ms.SupineMinutes,
					ms.RespirationRPM, ms.SnorePercent, ms.WakeEvents)
			}
			_ = tg.Refresh(sctx)
			cancel()
			log.Printf("tiger: synced %d detections", len(batch))
		}
	}
}

// resolvePorts turns the -ports flag into a device list.
func resolvePorts(spec string) ([]string, error) {
	if spec != "auto" && spec != "" {
		var out []string
		for _, p := range strings.Split(spec, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("-ports %q parsed to nothing", spec)
		}
		return out, nil
	}

	found, err := sensor.DiscoverPorts()
	if err != nil {
		return nil, fmt.Errorf("discover ports: %w", err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no serial devices found. Plug in the Arduinos, flash accel_node.ino, " +
			"then re-run. Use -list-ports to see what is visible.")
	}
	return found, nil
}

// pumpSensors feeds every sample into the detector and streams a decimated
// trace to the dashboard. The trace is decimated because 100 Hz times three
// nodes is more than any browser needs to draw a legible waveform.
func pumpSensors(ctx context.Context, src sensor.Source, det *detect.Detector, mat *maternal.Tracker, hub *api.Hub) {
	readings := src.Readings()
	acoustics := src.Acoustics()

	var n int
	const decimate = 5 // ~20 Hz to the browser

	for {
		select {
		case <-ctx.Done():
			return
		case r, ok := <-readings:
			if !ok {
				return
			}
			det.Feed(r)
			mat.Feed(r)
			if r.Node == sensor.NodeRef {
				n++
				if n%decimate == 0 {
					hub.Broadcast(api.Event{Kind: "trace", Data: map[string]any{
						"t_ms": r.T.UnixMilli(),
						"mag":  r.Magnitude(),
					}})
				}
			}
		case a, ok := <-acoustics:
			if !ok {
				continue
			}
			det.FeedAcoustic(a)
			mat.FeedAcoustic(a)
		}
	}
}

// pumpDetections persists each detection and pushes it to the dashboard. This
// is the only thing that increments the count on screen.
func pumpDetections(ctx context.Context, det *detect.Detector, st *store.Store, hub *api.Hub) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-det.Out:
			if err := st.InsertDetection(d.T, d.Residual, d.Confidence, d.Acoustic); err != nil {
				log.Printf("detection insert: %v", err)
			}
			hub.Broadcast(api.Event{Kind: "detection", Data: d})
		}
	}
}

// rollUpNights writes the real detection count for today into the nights table
// so the trend reflects what actually happened. Off in demo mode, because the
// seeded night is what makes the override visible on stage.
func rollUpNights(ctx context.Context, st *store.Store) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			day := time.Now().Format("2006-01-02")
			n, err := st.CountDetectionsOn(day)
			if err != nil {
				log.Printf("rollup: %v", err)
				continue
			}
			if err := st.UpsertNight(day, n, false); err != nil {
				log.Printf("rollup upsert: %v", err)
			}
		}
	}
}

// seedBaseline writes fourteen prior nights so the trend view has something to
// show. These are LABELLED SIMULATED in the UI and must stay that way: at a
// health track, unlabelled fabricated data is a real integrity problem, and
// judges do ask whether the chart is real.
func seedBaseline(st *store.Store) error {
	existing, err := st.Nights(1)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	const baseline = 340
	now := time.Now()
	for i := 13; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		var count int
		switch {
		case i == 0:
			count = 198 // tonight: 41% below baseline
		case i == 1:
			count = 241 // and the night before, which is why the alert fires
		default:
			count = baseline + rand.Intn(61) - 30
		}
		if err := st.UpsertNight(day, count, true); err != nil {
			return err
		}
	}
	log.Println("seeded 14 simulated baseline nights (labelled simulated in the UI)")
	return nil
}

// parsePhones turns "abdo_a=192.168.1.21,ref=192.168.1.23" into handsets.
//
// The node names are the same three the detector already knows about, and at
// least one abdominal node plus the reference is required — reference
// subtraction is what makes this work at all, and a run without it would
// quietly count maternal movement as fetal movement.
func parsePhones(spec string) ([]sensor.Phone, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("-source phone needs -phones, e.g. " +
			"-phones \"abdo_a=192.168.1.21,ref=192.168.1.23\"")
	}
	valid := map[string]sensor.Node{
		"abdo_a": sensor.NodeAbdoA,
		"abdo_b": sensor.NodeAbdoB,
		"ref":    sensor.NodeRef,
	}
	var out []sensor.Phone
	seen := map[sensor.Node]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, host, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad -phones entry %q: want node=host", part)
		}
		node, ok := valid[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("bad -phones node %q: want abdo_a, abdo_b or ref", name)
		}
		if seen[node] {
			return nil, fmt.Errorf("-phones lists %q twice", name)
		}
		seen[node] = true
		host = strings.TrimSpace(host)
		if host == "" {
			return nil, fmt.Errorf("-phones entry %q has no host", part)
		}
		out = append(out, sensor.Phone{Node: node, Host: host})
	}
	if !seen[sensor.NodeRef] {
		return nil, fmt.Errorf("-phones must include a ref handset: without the " +
			"reference node, maternal movement cannot be subtracted and it would " +
			"be counted as fetal movement")
	}
	if !seen[sensor.NodeAbdoA] && !seen[sensor.NodeAbdoB] {
		return nil, fmt.Errorf("-phones must include at least one abdominal handset (abdo_a or abdo_b)")
	}
	return out, nil
}

// firstNonEmpty returns the first non-blank value, so a flag beats an env var
// and an env var beats nothing.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
