package kicker

// Hobby-servo driver, through the kernel's sysfs PWM interface.
//
// The phantom's actuator turned out to be a 9g micro servo (SG90 class), not a
// DC motor, so the Grove H-bridge at 0x0F cannot drive it: a servo has its own
// control electronics and wants a timed pulse, not a switched voltage.
//
// A servo reads position from PULSE WIDTH, repeated every 20ms:
//
//	 0.5ms  ->   0 degrees
//	 1.5ms  ->  90 degrees
//	 2.4ms  -> 180 degrees
//
// The frame rate is fixed; only the width carries information. Hardware PWM
// from the RP1 is used rather than bit-banging, because a jittering pulse makes
// the servo buzz and creep — and a phantom that moves when it was not asked to
// is ground truth that lies.
//
// The sysfs root is injectable so the whole thing is testable against a
// temporary directory, with no Pi and no servo.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Servo timing. Defaults suit an SG90; every servo is slightly different and a
// pulse outside its range makes it grind against its endstop.
const (
	DefaultPeriod   = 20 * time.Millisecond   // 50 Hz frame
	DefaultMinPulse = 500 * time.Microsecond  // 0 degrees
	DefaultMaxPulse = 2400 * time.Microsecond // 180 degrees
)

// RestAngle is where the paddle sits between kicks: clear of the foam, so the
// phantom is not resting against the pod and damping it.
const RestAngle = 10

// PWMServo drives one servo on one sysfs PWM channel.
type PWMServo struct {
	Root     string        // sysfs root, normally /sys/class/pwm
	Chip     string        // e.g. "pwmchip0"
	Channel  int           // channel within the chip
	Period   time.Duration // frame period
	MinPulse time.Duration // pulse for 0 degrees
	MaxPulse time.Duration // pulse for 180 degrees

	// Hold is how long the paddle stays at the kick angle before returning.
	// A servo needs time to travel; returning immediately produces a twitch
	// the foam swallows entirely.
	Hold time.Duration

	mu       sync.Mutex
	exported bool
}

// NewPWMServo returns a servo with SG90 defaults.
func NewPWMServo(chip string, channel int) *PWMServo {
	return &PWMServo{
		Root:     "/sys/class/pwm",
		Chip:     chip,
		Channel:  channel,
		Period:   DefaultPeriod,
		MinPulse: DefaultMinPulse,
		MaxPulse: DefaultMaxPulse,
		Hold:     140 * time.Millisecond,
	}
}

func (p *PWMServo) chipDir() string { return filepath.Join(p.Root, p.Chip) }
func (p *PWMServo) pwmDir() string {
	return filepath.Join(p.chipDir(), fmt.Sprintf("pwm%d", p.Channel))
}

// PulseForAngle converts degrees to a pulse width.
//
// Clamped rather than rejected: an out-of-range angle is a bug in the caller,
// but grinding a servo against its endstop for the length of a demo is a
// physical failure, and clamping is the safer of the two.
func (p *PWMServo) PulseForAngle(deg int) time.Duration {
	if deg < 0 {
		deg = 0
	}
	if deg > 180 {
		deg = 180
	}
	span := p.MaxPulse - p.MinPulse
	return p.MinPulse + time.Duration(float64(span)*float64(deg)/180.0)
}

func (p *PWMServo) write(name, value string) error {
	path := filepath.Join(p.pwmDir(), name)
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %s=%s: %w", path, value, err)
	}
	return nil
}

// Export claims the channel and sets the frame period.
func (p *PWMServo) Export() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exportLocked()
}

func (p *PWMServo) exportLocked() error {
	if p.exported {
		return nil
	}
	if _, err := os.Stat(p.chipDir()); err != nil {
		return fmt.Errorf("no %s (is the overlay loaded? "+
			"dtoverlay=pwm-2chan,pin=18,func=2): %w", p.chipDir(), err)
	}
	// Exporting an already-exported channel returns EBUSY, which is not an
	// error for us — it means a previous run left it claimed.
	if _, err := os.Stat(p.pwmDir()); err != nil {
		_ = os.WriteFile(filepath.Join(p.chipDir(), "export"),
			[]byte(strconv.Itoa(p.Channel)), 0o644)
		// The kernel creates the directory asynchronously.
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(p.pwmDir()); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := os.Stat(p.pwmDir()); err != nil {
			return fmt.Errorf("channel %d did not appear at %s: %w", p.Channel, p.pwmDir(), err)
		}
	}
	// Period must be set before duty_cycle: the kernel rejects a duty larger
	// than the current period, and the default period is 0.
	if err := p.write("period", strconv.FormatInt(p.Period.Nanoseconds(), 10)); err != nil {
		return err
	}
	if err := p.write("duty_cycle", strconv.FormatInt(p.PulseForAngle(RestAngle).Nanoseconds(), 10)); err != nil {
		return err
	}
	if err := p.write("enable", "1"); err != nil {
		return err
	}
	p.exported = true
	return nil
}

// SetAngle moves the paddle.
func (p *PWMServo) SetAngle(deg int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.exportLocked(); err != nil {
		return err
	}
	return p.write("duty_cycle", strconv.FormatInt(p.PulseForAngle(deg).Nanoseconds(), 10))
}

// Kick swings to the strength's angle, holds, and returns to rest.
//
// The amplitude argument is the simulator's unit (g at the sensor) and is
// ignored here: this path produces a physical push whose amplitude is set by
// the foam and the paddle geometry. The named strength is the contract.
func (p *PWMServo) Kick(strength string, _ float64) error {
	a, ok := angles[strength]
	if !ok {
		a = angles[Medium]
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.exportLocked(); err != nil {
		return err
	}
	if err := p.write("duty_cycle", strconv.FormatInt(p.PulseForAngle(a).Nanoseconds(), 10)); err != nil {
		return fmt.Errorf("swing to %s: %w", strength, err)
	}
	time.Sleep(p.Hold)
	// Always return to rest, even if the swing errored partway. A paddle left
	// pressed into the foam damps the pod and quietly kills detection for the
	// rest of the night.
	if err := p.write("duty_cycle", strconv.FormatInt(p.PulseForAngle(RestAngle).Nanoseconds(), 10)); err != nil {
		return fmt.Errorf("return to rest after %s: %w", strength, err)
	}
	return nil
}

// Close parks at rest and disables the output, so the servo stops holding
// torque and stops drawing current.
func (p *PWMServo) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exported {
		return nil
	}
	_ = p.write("duty_cycle", strconv.FormatInt(p.PulseForAngle(RestAngle).Nanoseconds(), 10))
	time.Sleep(200 * time.Millisecond) // let it actually get there
	return p.write("enable", "0")
}
