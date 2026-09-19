//go:build linux

package sensor

// Grove Base HAT ADC — the 12-bit STM32 on the HAT that reads the analog
// sockets. This is how the rotary angle sensor and the contact microphone get
// into the system; the Pi has no analog inputs of its own.
//
// It lives at 0x04, which is BELOW the 0x08-0x77 range i2cdetect and i2cget
// scan by default. That is why the chip looked absent for most of a night: the
// tools were never asking. Binding to it needs I2C_SLAVE_FORCE rather than
// I2C_SLAVE for the same reason.

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	// i2cSlaveForce binds an address the kernel would otherwise guard.
	i2cSlaveForce = 0x0706

	// AddrGroveADC is the HAT's ADC.
	AddrGroveADC = 0x04

	regDeviceID = 0x00
	regVersion  = 0x02
	regMilliV   = 0x20 // +channel
)

// DeviceIDGroveHAT is what the Base HAT for Raspberry Pi reports.
const DeviceIDGroveHAT = 0x0004

// ADC reads the Grove Base HAT's analog channels.
type ADC struct {
	mu sync.Mutex
	f  *os.File
}

// OpenADC binds to the HAT's ADC on /dev/i2c-<bus> and verifies it is really
// there, so a missing HAT fails immediately with a clear message rather than
// silently returning plausible-looking floating voltages.
func OpenADC(bus int) (*ADC, error) {
	path := fmt.Sprintf("/dev/i2c-%d", bus)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(i2cSlaveForce), uintptr(AddrGroveADC))
	if errno != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("bind 0x%02X on %s: %w", AddrGroveADC, path, errno)
	}
	a := &ADC{f: f}
	id, err := a.word(regDeviceID)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read device id: %w", err)
	}
	if id != DeviceIDGroveHAT {
		_ = f.Close()
		return nil, fmt.Errorf("device at 0x%02X reports id 0x%04X, want 0x%04X "+
			"(is the Grove Base HAT seated?)", AddrGroveADC, id, DeviceIDGroveHAT)
	}
	return a, nil
}

func (a *ADC) Close() error { return a.f.Close() }

func (a *ADC) word(reg uint8) (uint16, error) {
	if _, err := a.f.Write([]byte{reg}); err != nil {
		return 0, err
	}
	// The STM32 needs a moment between the register write and the read.
	time.Sleep(time.Millisecond)
	buf := make([]byte, 2)
	if _, err := a.f.Read(buf); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf), nil
}

// Version reports the HAT's firmware version.
func (a *ADC) Version() (uint16, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.word(regVersion)
}

// MilliVolts reads one analog channel, 0-7.
//
// Note the channel is not always the socket label: on this HAT a module in the
// socket marked A2 can land on channel 3. Probe rather than assume.
func (a *ADC) MilliVolts(channel int) (int, error) {
	if channel < 0 || channel > 7 {
		return 0, fmt.Errorf("channel %d out of range 0-7", channel)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	v, err := a.word(regMilliV + uint8(channel))
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

// FindSwingingChannel returns the channel that moves most over the sample
// window, which is how you locate a potentiometer without knowing its socket.
//
// Reading every channel in turn couples them: a fast-changing input bleeds a
// couple of hundred millivolts into its neighbours through the multiplexer. So
// a channel only qualifies if it swings far more than that.
func (a *ADC) FindSwingingChannel(d time.Duration, minSwing int) (int, int, error) {
	lo := [8]int{}
	hi := [8]int{}
	for i := range lo {
		lo[i] = 1 << 30
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for ch := 0; ch < 8; ch++ {
			v, err := a.MilliVolts(ch)
			if err != nil {
				continue
			}
			if v < lo[ch] {
				lo[ch] = v
			}
			if v > hi[ch] {
				hi[ch] = v
			}
		}
	}
	best, bestSwing := -1, 0
	for ch := 0; ch < 8; ch++ {
		if s := hi[ch] - lo[ch]; s > bestSwing {
			best, bestSwing = ch, s
		}
	}
	if bestSwing < minSwing {
		return -1, bestSwing, fmt.Errorf("no channel swung more than %d mV "+
			"(best was A%d at %d mV) — is the sensor in an A socket, fully clicked in?",
			minSwing, best, bestSwing)
	}
	return best, bestSwing, nil
}
