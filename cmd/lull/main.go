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
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kbhatnagar1506/lull/internal/api"
	"github.com/kbhatnagar1506/lull/internal/detect"
	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/sensor"
	"github.com/kbhatnagar1506/lull/internal/store"
	"github.com/kbhatnagar1506/lull/web"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		dbPath   = flag.String("db", "data/lull.db", "sqlite path")
		source   = flag.String("source", "sim", "sensor source: sim | serial")
		seed     = flag.Bool("seed", true, "seed a simulated 14-night baseline if empty")
		sampleHz = flag.Int("hz", 100, "sensor sample rate (sim only)")

		ports     = flag.String("ports", "auto", "serial devices, comma separated, or \"auto\" to discover")
		baud      = flag.Int("baud", 115200, "serial baud rate, must match the sketch")
		listPorts = flag.Bool("list-ports", false, "print candidate serial devices and exit")

		acousticThreshold = flag.Float64("acoustic-threshold", 0, "acoustic spike threshold above rolling floor; 0 picks a default per source")
		requireAcoustic   = flag.Bool("require-acoustic", true, "a detection must be confirmed by the contact mic")
		kickThreshold     = flag.Float64("threshold", 0, "detection residual in g; 0 uses the default")
	)
	flag.Parse()

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

	default:
		log.Fatalf("unknown -source %q", *source)
	}
	defer src.Close()

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

	go pumpSensors(ctx, src, det, hub)
	go pumpDetections(ctx, det, st, hub)

	// --- http ----------------------------------------------------------
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		log.Fatalf("embed web: %v", err)
	}

	srv := &api.Server{
		Hub:    hub,
		Store:  st,
		Kicker: kk,
		Web:    http.FS(sub),
		Fool:   fool,
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("lull listening on http://localhost%s", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
func pumpSensors(ctx context.Context, src sensor.Source, det *detect.Detector, hub *api.Hub) {
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
