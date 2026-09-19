package kicker

// Grove I2C Motor Driver (L298P + ATmega8L), the board that answers at 0x0F.
//
// This replaced the Arduino servo path, because the hardware lab had no
// Arduinos. It drives a DC motor through an H-bridge rather than positioning a
// hobby servo, which suits a kick better than a servo sweep did: a kick is an
// impulse, not a position.
//
// Protocol, from Seeed's own library. Every command is exactly three bytes,
// and the third is padding on the commands that only need one argument:
//
//	[0x82, speedM1, speedM2]   set PWM duty, 0-255 each
//	[0xAA, direction, 0x01]    set H-bridge direction
//	[0x84, prescaler, 0x01]    set PWM frequency
//
// The firmware ignores writes it does not understand, so a wrong byte here is
// silent — which is exactly why the encoding is split out and tested.

import (
	"fmt"
	"sync"
	"time"
)

// AddrGroveMotor is the driver's default address. It is DIP-switch selectable;
// i2cdetect showed 0x0F, which is the factory setting.
const AddrGroveMotor = 0x0F

// Command bytes.
const (
	cmdMotorSpeedSet = 0x82
	cmdPWMFrequency  = 0x84
	cmdDirectionSet  = 0xAA
	cmdNothing       = 0x01 // padding; the firmware requires a third byte
)

// Direction encodings. Only the M1 half matters here — the phantom has one
// motor — but the driver sets both channels in a single byte, so M2 is parked
// in the same direction rather than left undefined.
const (
	dirBothClockWise     = 0x0A
	dirBothAntiClockWise = 0x05
)

// PWM frequency prescalers.
const (
	freq31372Hz = 0x01 // above hearing: the motor does not whine
	freq3921Hz  = 0x02
	freq490Hz   = 0x03
	freq122Hz   = 0x04
	freq30Hz    = 0x05
)

// I2CWriter is the one thing this needs from a bus: write these bytes to that
// address. Narrow on purpose, so the protocol can be tested without a Pi.
type I2CWriter interface {
	Write(addr uint8, data []byte) error
}

// Pulse is how hard and how long the motor drives for one kick.
//
// Duration matters as much as speed. A DC motor takes a few milliseconds to
// spin up, so a very short pulse produces almost nothing no matter the duty
// cycle; the weak kick is shaped by shortening the pulse *and* dropping the
// duty, which is what makes it genuinely hard for a person to feel.
type Pulse struct {
	Speed uint8         // PWM duty, 0-255
	For   time.Duration // how long to drive before stopping
}

// Pulses per named strength. Retune against the real foam: weak must be barely
// perceptible to a human, because the weak kicks are what make the blind test
// mean anything. If weak is obvious, the test proves nothing.
var pulses = map[string]Pulse{
	Weak:   {Speed: 90, For: 40 * time.Millisecond},
	Medium: {Speed: 160, For: 70 * time.Millisecond},
	Strong: {Speed: 255, For: 110 * time.Millisecond},
}

// EncodeSpeed builds the 3-byte speed command.
func EncodeSpeed(m1, m2 uint8) []byte {
	return []byte{cmdMotorSpeedSet, m1, m2}
}

// EncodeDirection builds the 3-byte direction command.
func EncodeDirection(dir uint8) []byte {
	return []byte{cmdDirectionSet, dir, cmdNothing}
}

// EncodeFrequency builds the 3-byte PWM frequency command.
func EncodeFrequency(prescaler uint8) []byte {
	return []byte{cmdPWMFrequency, prescaler, cmdNothing}
}

// PulseFor returns the drive parameters for a named strength, falling back to
// Medium for anything unrecognised rather than refusing to kick.
func PulseFor(strength string) Pulse {
	if p, ok := pulses[strength]; ok {
		return p
	}
	return pulses[Medium]
}

// GroveServo drives the phantom through the Grove I2C Motor Driver.
type GroveServo struct {
	Bus  I2CWriter
	Addr uint8

	mu   sync.Mutex // one kick at a time; overlapping pulses would stack
	init bool
}

// NewGroveServo returns a servo driving the board at addr. Pass 0 for the
// factory address.
func NewGroveServo(bus I2CWriter, addr uint8) *GroveServo {
	if addr == 0 {
		addr = AddrGroveMotor
	}
	return &GroveServo{Bus: bus, Addr: addr}
}

// Init sets the PWM frequency once. 31372 Hz is above hearing, so the motor
// does not whine between kicks — which matters when the whole demo is about
// listening to a quiet thing.
func (g *GroveServo) Init() error {
	if g.Bus == nil {
		return fmt.Errorf("grove servo: no bus")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.Bus.Write(g.Addr, EncodeFrequency(freq31372Hz)); err != nil {
		return fmt.Errorf("grove servo: set frequency: %w", err)
	}
	time.Sleep(4 * time.Millisecond)
	if err := g.Bus.Write(g.Addr, EncodeDirection(dirBothClockWise)); err != nil {
		return fmt.Errorf("grove servo: set direction: %w", err)
	}
	time.Sleep(4 * time.Millisecond)
	// Park stopped. The driver powers up with an undefined duty, and a phantom
	// that starts running on its own would pollute the recording before anyone
	// has pressed anything.
	if err := g.Bus.Write(g.Addr, EncodeSpeed(0, 0)); err != nil {
		return fmt.Errorf("grove servo: stop: %w", err)
	}
	g.init = true
	return nil
}

// Stop halts the motor. Safe to call at any time, including from a defer.
func (g *GroveServo) Stop() error {
	if g.Bus == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Bus.Write(g.Addr, EncodeSpeed(0, 0))
}

// Kick drives one impulse. It blocks for the pulse duration and then stops the
// motor, so the caller cannot leave it running by forgetting to.
//
// The amplitude argument is ignored: it is the simulator's unit (g at the
// sensor), and this path produces a physical push whose amplitude is whatever
// the foam and the paddle geometry give. The named strength is the contract.
func (g *GroveServo) Kick(strength string, _ float64) error {
	if g.Bus == nil {
		return fmt.Errorf("grove servo: no bus")
	}
	if !g.init {
		if err := g.Init(); err != nil {
			return err
		}
	}
	p := PulseFor(strength)

	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.Bus.Write(g.Addr, EncodeSpeed(p.Speed, 0)); err != nil {
		return fmt.Errorf("grove servo: start %s: %w", strength, err)
	}
	time.Sleep(p.For)
	// Stop even if the start succeeded and something else failed: a motor left
	// driving is the one failure here with a physical consequence.
	if err := g.Bus.Write(g.Addr, EncodeSpeed(0, 0)); err != nil {
		return fmt.Errorf("grove servo: stop after %s: %w", strength, err)
	}
	return nil
}
