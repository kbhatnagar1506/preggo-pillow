package sensor

// Phone accelerometer source, over phyphox's REST interface.
//
// This exists because the hardware lab's Raspberry Pi would not boot and there
// was no card reader in the building. A modern phone carries a better
// accelerometer than the Grove parts do, four people on a team carry four of
// them, and phyphox (free, from RWTH Aachen) exposes the raw buffers over HTTP.
//
// Nothing downstream changes. This satisfies sensor.Source exactly like the
// I2C and serial paths, so the detector, the reference subtraction and every
// test run against the same code either way.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gravity converts phyphox's m/s^2 to the g the rest of the system speaks.
const gravity = 9.80665

// Phone is one handset pinned to one body position.
type Phone struct {
	Node Node   // which body position this handset is taped to
	Host string // "192.168.1.23" or "192.168.1.23:8080"
}

// BaseURL returns the phyphox root, defaulting the port by platform. iOS serves
// on 80 and Android on 8080, which is the single most common thing to get wrong
// when wiring this up at 2am.
func (p Phone) BaseURL() string {
	h := p.Host
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	h = strings.TrimSuffix(h, "/")
	return "http://" + h
}

// phyphoxResponse is the documented /get shape. Only the fields we rely on.
type phyphoxResponse struct {
	Buffer map[string]struct {
		Buffer []*float64 `json:"buffer"`
	} `json:"buffer"`
	Status struct {
		Measuring bool `json:"measuring"`
	} `json:"status"`
}

// parsePhyphox pulls aligned X/Y/Z/time samples out of one /get response.
//
// Split from the HTTP call so the decoding — which is where the bugs live — is
// testable with no phone, no network and no phyphox.
func parsePhyphox(body []byte) (xs, ys, zs, ts []float64, measuring bool, err error) {
	var r phyphoxResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, nil, nil, nil, false, fmt.Errorf("decode phyphox response: %w", err)
	}
	get := func(name string) []float64 {
		b, ok := r.Buffer[name]
		if !ok {
			return nil
		}
		out := make([]float64, 0, len(b.Buffer))
		for _, v := range b.Buffer {
			if v == nil { // phyphox sends null for gaps; a null is not a zero
				continue
			}
			out = append(out, *v)
		}
		return out
	}
	xs, ys, zs, ts = get("accX"), get("accY"), get("accZ"), get("acc_time")

	// Truncate to the shortest. The four buffers are filled by one sensor
	// callback so they are normally equal, but a poll that lands mid-write can
	// return one extra sample on some of them, and zipping mismatched lengths
	// would silently pair the wrong axes together.
	n := len(xs)
	for _, l := range []int{len(ys), len(zs), len(ts)} {
		if l < n {
			n = l
		}
	}
	return xs[:n], ys[:n], zs[:n], ts[:n], r.Status.Measuring, nil
}

// PhoneSource polls one or more handsets and emits Readings.
type PhoneSource struct {
	phones   []Phone
	client   *http.Client
	readings chan Reading
	acoustic chan Acoustic
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup

	mu   sync.Mutex
	errs map[Node]string // last error per node, for the status endpoint
}

// NewPhoneSource starts polling every handset at pollHz.
//
// phyphox buffers samples between polls and the threshold query returns only
// what is new, so a 10 Hz poll still yields the sensor's full rate — typically
// 100 Hz. Polling faster just burns battery and Wi-Fi airtime.
func NewPhoneSource(phones []Phone, pollHz int) *PhoneSource {
	if pollHz <= 0 {
		pollHz = 10
	}
	s := &PhoneSource{
		phones:   phones,
		client:   &http.Client{Timeout: 2 * time.Second},
		readings: make(chan Reading, 8192),
		acoustic: make(chan Acoustic, 512),
		stop:     make(chan struct{}),
		errs:     make(map[Node]string),
	}
	for _, p := range phones {
		s.wg.Add(1)
		go s.poll(p, pollHz)
	}
	return s
}

func (s *PhoneSource) Readings() <-chan Reading   { return s.readings }
func (s *PhoneSource) Acoustics() <-chan Acoustic { return s.acoustic }

// Errors reports the last error seen per node, so the dashboard can say "phone
// B dropped off Wi-Fi" instead of quietly counting fewer kicks.
func (s *PhoneSource) Errors() map[Node]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[Node]string, len(s.errs))
	for k, v := range s.errs {
		out[k] = v
	}
	return out
}

func (s *PhoneSource) setErr(n Node, msg string) {
	s.mu.Lock()
	if msg == "" {
		delete(s.errs, n)
	} else {
		s.errs[n] = msg
	}
	s.mu.Unlock()
}

func (s *PhoneSource) Close() error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	close(s.readings)
	close(s.acoustic)
	return nil
}

// Start asks a handset to begin measuring, so nobody has to tap Play on four
// phones in the right order while a judge watches.
func (s *PhoneSource) Start(ctx context.Context, p Phone) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL()+"/control?cmd=start", nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w (is phyphox open with remote access enabled?)", p.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: /control returned %s", p.Host, resp.Status)
	}
	return nil
}

func (s *PhoneSource) poll(p Phone, hz int) {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second / time.Duration(hz))
	defer tick.Stop()

	var (
		lastT    float64 // phyphox experiment clock, seconds
		haveBase bool
		baseWall time.Time
		baseExp  float64
	)

	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}

		// Ask only for samples newer than the last one we kept. "<t>|acc_time"
		// means "values of this buffer where acc_time exceeds t", which is how
		// phyphox does incremental reads.
		// Assembled by hand rather than with url.Values, because that would
		// percent-escape the "|" and phyphox's parser wants it raw.
		thr := strconv.FormatFloat(lastT, 'f', 6, 64)
		parts := make([]string, 0, 4)
		for _, b := range []string{"acc_time", "accX", "accY", "accZ"} {
			parts = append(parts, b+"="+thr+"|acc_time")
		}
		endpoint := p.BaseURL() + "/get?" + strings.Join(parts, "&")

		body, err := s.fetch(endpoint)
		if err != nil {
			s.setErr(p.Node, err.Error())
			continue
		}
		xs, ys, zs, ts, measuring, err := parsePhyphox(body)
		if err != nil {
			s.setErr(p.Node, err.Error())
			continue
		}
		if !measuring {
			s.setErr(p.Node, "phyphox is paused — press play on "+string(p.Node))
			continue
		}
		s.setErr(p.Node, "")

		for i := range ts {
			if !haveBase {
				baseWall, baseExp, haveBase = time.Now(), ts[i], true
			}
			// Map the phone's experiment clock onto wall time. Using the offset
			// rather than time.Now() per sample preserves the real spacing
			// between samples inside one batch, which the detector's high-pass
			// depends on.
			when := baseWall.Add(time.Duration((ts[i] - baseExp) * float64(time.Second)))
			r := Reading{
				T:    when,
				Node: p.Node,
				AX:   xs[i] / gravity,
				AY:   ys[i] / gravity,
				AZ:   zs[i] / gravity,
			}
			select {
			case s.readings <- r:
			default: // never block the poller on a slow consumer
			}
			if ts[i] > lastT {
				lastT = ts[i]
			}
		}
	}
}

func (s *PhoneSource) fetch(endpoint string) ([]byte, error) {
	resp, err := s.client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/get returned %s", resp.Status)
	}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 8*1024*1024 {
			return nil, fmt.Errorf("/get response too large")
		}
	}
	return buf, nil
}
