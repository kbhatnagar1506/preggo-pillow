//go:build linux

package sensor

// I2C accelerometer source, for reading Grove sensors straight off a Raspberry
// Pi's bus. This is the path that replaced the Arduino-over-USB one, because
// the hardware lab had no Arduinos.
//
// No cgo and no third-party driver: an I2C transfer on Linux is an ioctl on
// /dev/i2c-N, which is a syscall and a few bytes. Keeping it in pure Go means
// the binary still cross-compiles to the Pi from a Mac with CGO_ENABLED=0.

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// i2cSlave is the ioctl that binds an open bus to one device address.
const i2cSlave = 0x0703

// Bus is one open /dev/i2c-N.
type Bus struct {
	mu   sync.Mutex
	f    *os.File
	path string
	addr uint8
}

// OpenBus opens /dev/i2c-<n>.
func OpenBus(n int) (*Bus, error) {
	path := fmt.Sprintf("/dev/i2c-%d", n)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (is I2C enabled? run: sudo raspi-config nonint do_i2c 0)", path, err)
	}
	return &Bus{f: f, path: path}, nil
}

func (b *Bus) Close() error { return b.f.Close() }

// setAddr points the bus at one device. Held under the mutex by callers.
func (b *Bus) setAddr(addr uint8) error {
	if b.addr == addr {
		return nil
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, b.f.Fd(), uintptr(i2cSlave), uintptr(addr))
	if errno != 0 {
		return fmt.Errorf("select address 0x%02X on %s: %w", addr, b.path, errno)
	}
	b.addr = addr
	return nil
}

// WriteReg writes one byte to one register.
func (b *Bus) WriteReg(addr, reg, val uint8) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.setAddr(addr); err != nil {
		return err
	}
	_, err := b.f.Write([]byte{reg, val})
	return err
}

// Write sends raw bytes to a device, with no register prefix.
//
// Needed because not every I2C device is register-addressed. The Grove motor
// driver takes three-byte commands as a plain write, so WriteReg's
// register-then-value shape does not fit it.
func (b *Bus) Write(addr uint8, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.setAddr(addr); err != nil {
		return err
	}
	_, err := b.f.Write(data)
	return err
}

// ReadReg reads n bytes starting at reg.
func (b *Bus) ReadReg(addr, reg uint8, n int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.setAddr(addr); err != nil {
		return nil, err
	}
	if _, err := b.f.Write([]byte{reg}); err != nil {
		return nil, fmt.Errorf("set register 0x%02X: %w", reg, err)
	}
	buf := make([]byte, n)
	got, err := b.f.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:got], nil
}

// Present reports whether a device answers at an address. This is what
// i2cdetect does: address the device and see whether it acknowledges.
func (b *Bus) Present(addr uint8) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.setAddr(addr); err != nil {
		return false
	}
	_, err := b.f.Write([]byte{0x00})
	return err == nil
}

// DetectChip finds which accelerometer is on this bus, so nobody has to read a
// part number off a chip the size of a grain of rice.
func (b *Bus) DetectChip() (Chip, uint8, error) {
	for _, addr := range []uint8{AddrADXL345, AddrLIS3DH, AddrLIS3DHB, AddrMMA7660} {
		if b.Present(addr) {
			return ChipAt(addr), addr, nil
		}
	}
	return ChipUnknown, 0, fmt.Errorf(
		"no accelerometer found on %s — check the Grove cable is clicked in at both ends, "+
			"and that this sensor is the only one of its kind on this bus", b.path)
}

// I2CNode is one accelerometer on one bus, read at a fixed rate.
type I2CNode struct {
	Bus  *Bus
	Addr uint8
	Chip Chip
	Node Node
}

// InitNode wakes the chip into continuous measurement.
func InitNode(bus *Bus, node Node) (*I2CNode, error) {
	chip, addr, err := bus.DetectChip()
	if err != nil {
		return nil, err
	}
	p, ok := profiles[chip]
	if !ok {
		return nil, fmt.Errorf("no profile for chip %q", chip)
	}
	for _, kv := range p.initSeq {
		if err := bus.WriteReg(addr, kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("init %s reg 0x%02X: %w", chip, kv[0], err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return &I2CNode{Bus: bus, Addr: addr, Chip: chip, Node: node}, nil
}

// Read takes one sample.
func (n *I2CNode) Read() (Reading, error) {
	p := profiles[n.Chip]
	reg := p.dataReg
	if p.autoIncr {
		reg |= 0x80 // LIS3DH: MSB set means auto-increment across registers
	}
	raw, err := n.Bus.ReadReg(n.Addr, reg, p.dataLen)
	if err != nil {
		return Reading{}, err
	}
	x, y, z, err := DecodeSample(n.Chip, raw)
	if err != nil {
		return Reading{}, err
	}
	// Stamped here, on arrival, for the same reason the serial path does it:
	// one clock for the whole system.
	return Reading{T: time.Now(), Node: n.Node, AX: x, AY: y, AZ: z}, nil
}

// I2CSource reads one or more local accelerometers as a sensor.Source.
type I2CSource struct {
	nodes    []*I2CNode
	readings chan Reading
	acoustic chan Acoustic
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

// NewI2CSource starts sampling every node at sampleHz.
func NewI2CSource(nodes []*I2CNode, sampleHz int) *I2CSource {
	if sampleHz <= 0 {
		sampleHz = 100
	}
	s := &I2CSource{
		nodes:    nodes,
		readings: make(chan Reading, 4096),
		acoustic: make(chan Acoustic, 512),
		stop:     make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run(sampleHz)
	return s
}

func (s *I2CSource) Readings() <-chan Reading   { return s.readings }
func (s *I2CSource) Acoustics() <-chan Acoustic { return s.acoustic }

// Close is idempotent: the channel closes must sit INSIDE the Once, or a
// second call panics on an already-closed channel.
func (s *I2CSource) Close() error {
	s.once.Do(func() {
		close(s.stop)
		s.wg.Wait()
		close(s.readings)
		close(s.acoustic)
		for _, n := range s.nodes {
			_ = n.Bus.Close()
		}
	})
	return nil
}

func (s *I2CSource) run(hz int) {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second / time.Duration(hz))
	defer tick.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			for _, n := range s.nodes {
				r, err := n.Read()
				if err != nil {
					// A read error is usually a cable that has been nudged.
					// Skip the sample rather than tearing the process down;
					// the detector already refuses to run without a reference
					// node, so a missing sensor fails safe.
					continue
				}
				select {
				case s.readings <- r:
				default:
				}
			}
		}
	}
}

var _ = unsafe.Pointer(nil) // syscall.Syscall keeps the unsafe import honest
