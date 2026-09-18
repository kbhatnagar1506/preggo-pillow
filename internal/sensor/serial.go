package sensor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// Serial reads accelerometer nodes from one or more Arduinos over USB.
//
// Design notes that matter at 3am:
//
//   - Every port runs its own goroutine with its own reconnect loop. A USB
//     disconnect on one node must not stall the others, and it must recover on
//     its own rather than needing someone to restart the process.
//   - Timestamps are applied HERE, on arrival, never by the Arduino. millis()
//     on two boards plus the Pi clock drift apart, and drifting clocks make the
//     blind-test overlay look broken when the detector is fine.
//   - Node identity comes from the sketch (NODE_ID), not from which port it
//     happened to enumerate on. Unplug and replug in any order and it still
//     works.
type Serial struct {
	readings  chan Reading
	acoustics chan Acoustic

	mu    sync.Mutex
	ports map[string]serial.Port // open handles, by device path
	kickN string                 // node whose board drives the servo

	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// line is what the Arduino sketch emits, one JSON object per line.
type line struct {
	Node  string  `json:"n"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Z     float64 `json:"z"`
	S     *int    `json:"s"` // optional: Grove sound sensor envelope, 0-1023
	Hello string  `json:"hello"`
	Chip  string  `json:"chip"`
	Addr  int     `json:"addr"`
}

// DiscoverPorts returns candidate serial devices, filtering out the noise macOS
// and Linux leave lying around (Bluetooth pairs, debug consoles).
func DiscoverPorts() ([]string, error) {
	all, err := serial.GetPortsList()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range all {
		lower := strings.ToLower(p)
		switch {
		case strings.Contains(lower, "bluetooth"),
			strings.Contains(lower, "debug-console"),
			strings.Contains(lower, "wlan"):
			continue
		}
		// macOS exposes both /dev/tty.* and /dev/cu.* for one device. cu is
		// the one you want for a device that is already streaming: tty blocks
		// waiting for carrier detect.
		if strings.HasPrefix(p, "/dev/tty.") {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// NewSerial opens each port and starts streaming. Pass kickNode as the NODE_ID
// of the board wired to the servo; kick commands are written to that port.
// An empty kickNode means the first board that says hello.
func NewSerial(ports []string, baud int, kickNode string) (*Serial, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("no serial ports given; run with -ports or check DiscoverPorts")
	}
	if baud == 0 {
		baud = 115200
	}

	s := &Serial{
		readings:  make(chan Reading, 4096),
		acoustics: make(chan Acoustic, 512),
		ports:     make(map[string]serial.Port),
		kickN:     kickNode,
		stop:      make(chan struct{}),
	}

	for _, p := range ports {
		s.wg.Add(1)
		go s.runPort(p, baud)
	}
	return s, nil
}

func (s *Serial) Readings() <-chan Reading   { return s.readings }
func (s *Serial) Acoustics() <-chan Acoustic { return s.acoustics }

func (s *Serial) Close() error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()

	s.mu.Lock()
	for _, p := range s.ports {
		_ = p.Close()
	}
	s.ports = nil
	s.mu.Unlock()

	close(s.readings)
	close(s.acoustics)
	return nil
}

// runPort owns one device for the life of the process: open, stream, and on any
// failure wait and try again. It never gives up, because "someone nudged the
// USB cable at hour 30" must not end the demo.
func (s *Serial) runPort(dev string, baud int) {
	defer s.wg.Done()

	backoff := 500 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		select {
		case <-s.stop:
			return
		default:
		}

		port, err := serial.Open(dev, &serial.Mode{BaudRate: baud})
		if err != nil {
			log.Printf("serial %s: open failed (%v), retrying in %s", dev, err, backoff)
			if !s.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		_ = port.SetReadTimeout(2 * time.Second)
		log.Printf("serial %s: open at %d baud", dev, baud)
		backoff = 500 * time.Millisecond

		s.mu.Lock()
		s.ports[dev] = port
		s.mu.Unlock()

		s.pump(dev, port)

		s.mu.Lock()
		delete(s.ports, dev)
		s.mu.Unlock()
		_ = port.Close()

		select {
		case <-s.stop:
			return
		default:
			log.Printf("serial %s: stream ended, reopening", dev)
			if !s.sleep(backoff) {
				return
			}
		}
	}
}

// pump reads lines until the port errors or the source is closed.
func (s *Serial) pump(dev string, port serial.Port) {
	sc := bufio.NewScanner(port)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)

	var node Node
	var warned bool

	for sc.Scan() {
		select {
		case <-s.stop:
			return
		default:
		}

		raw := strings.TrimSpace(sc.Text())
		if raw == "" || raw[0] != '{' {
			continue // boot noise, partial line after a reconnect
		}

		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}

		// The hello line tells us which node this board is and which chip it
		// found. Worth logging: it is how you confirm Branch A vs Branch B
		// without running i2cdetect separately.
		if l.Hello != "" {
			node = Node(l.Hello)
			log.Printf("serial %s: node=%s chip=%s addr=0x%02X", dev, l.Hello, l.Chip, l.Addr)
			if l.Chip == "mma7660" {
				log.Printf("serial %s: MMA7660FC is 6-bit. This is BRANCH B: make the acoustic channel primary.", dev)
			}
			if l.Chip == "none" {
				log.Printf("serial %s: NO ACCELEROMETER FOUND on this board. Check the Grove cable.", dev)
			}
			s.mu.Lock()
			if s.kickN == "" {
				s.kickN = l.Hello
			}
			s.mu.Unlock()
			continue
		}

		if l.Node != "" {
			node = Node(l.Node)
		}
		if node == "" {
			if !warned {
				log.Printf("serial %s: data before hello, ignoring until the board identifies itself", dev)
				warned = true
			}
			continue
		}

		now := time.Now()
		select {
		case s.readings <- Reading{T: now, Node: node, AX: l.X, AY: l.Y, AZ: l.Z}:
		default: // drop rather than block the reader and back up the port
		}

		// Only the board with the contact mic sends "s".
		if l.S != nil {
			select {
			case s.acoustics <- Acoustic{T: now, RMS: float64(*l.S)}:
			default:
			}
		}
	}
}

// Kick writes a servo command to whichever board is wired to the servo.
//
// The servo lives on an Arduino rather than the Pi on purpose: the Pi's
// software PWM jitters, and jittery kicks poison the ground truth that the
// whole accuracy number rests on.
func (s *Serial) Kick(angle int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.ports) == 0 {
		return fmt.Errorf("no serial port open")
	}
	// Any open port will do if we never saw a hello; the sketch ignores
	// commands when no servo is attached.
	for _, p := range s.ports {
		if _, err := p.Write([]byte(fmt.Sprintf("K%d\n", angle))); err != nil {
			continue
		}
		return nil
	}
	return fmt.Errorf("write failed on every open port")
}

func (s *Serial) sleep(d time.Duration) bool {
	select {
	case <-s.stop:
		return false
	case <-time.After(d):
		return true
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
