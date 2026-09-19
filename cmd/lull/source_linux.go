//go:build linux

package main

// Wiring for the real hardware, which only exists on the Pi. Kept behind a
// build tag so the Mac still builds and tests everything else.

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// parseBuses turns "abdo_a=1,abdo_b=3,ref=4" into node/bus pairs.
//
// One bus per node, because three identical accelerometers all answer on the
// same address and would collide on a shared bus. /dev/i2c-1 is the 40-pin
// hardware bus; 3 and 4 are bit-banged i2c-gpio buses added in config.txt.
func parseBuses(spec string) (map[sensor.Node]int, error) {
	valid := map[string]sensor.Node{
		"abdo_a": sensor.NodeAbdoA,
		"abdo_b": sensor.NodeAbdoB,
		"ref":    sensor.NodeRef,
	}
	out := map[sensor.Node]int{}
	used := map[int]sensor.Node{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, num, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad -buses entry %q: want node=busnumber", part)
		}
		node, ok := valid[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("bad -buses node %q: want abdo_a, abdo_b or ref", name)
		}
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil {
			return nil, fmt.Errorf("bad -buses number in %q: %w", part, err)
		}
		if prev, clash := used[n]; clash {
			return nil, fmt.Errorf("bus %d assigned to both %q and %q: identical "+
				"accelerometers share an address and would collide", n, prev, node)
		}
		if _, dup := out[node]; dup {
			return nil, fmt.Errorf("-buses lists %q twice", name)
		}
		used[n] = node
		out[node] = n
	}
	if _, ok := out[sensor.NodeRef]; !ok {
		return nil, fmt.Errorf("-buses must include a ref node: without the reference, " +
			"maternal movement cannot be subtracted and would be counted as fetal movement")
	}
	if _, a := out[sensor.NodeAbdoA]; !a {
		if _, b := out[sensor.NodeAbdoB]; !b {
			return nil, fmt.Errorf("-buses must include at least one abdominal node")
		}
	}
	return out, nil
}

// openI2CSource brings up every configured accelerometer.
func openI2CSource(spec string, sampleHz int) (*sensor.I2CSource, error) {
	buses, err := parseBuses(spec)
	if err != nil {
		return nil, err
	}
	var nodes []*sensor.I2CNode
	for _, node := range []sensor.Node{sensor.NodeAbdoA, sensor.NodeAbdoB, sensor.NodeRef} {
		n, ok := buses[node]
		if !ok {
			continue
		}
		bus, err := sensor.OpenBus(n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", node, err)
		}
		in, err := sensor.InitNode(bus, node)
		if err != nil {
			_ = bus.Close()
			return nil, fmt.Errorf("%s on /dev/i2c-%d: %w", node, n, err)
		}
		log.Printf("  %-7s /dev/i2c-%d  chip=%s addr=0x%02X", node, n, in.Chip, in.Addr)
		if in.Chip.BranchB() {
			log.Printf("  WARNING: %s is an MMA7660 — 6-bit over +/-1.5g is 0.047g per "+
				"count, and the weakest fetal movements fall below one count. "+
				"Prefer -source phone for the sensing path.", node)
		}
		nodes = append(nodes, in)
	}
	return sensor.NewI2CSource(nodes, sampleHz), nil
}

// openGroveServo opens the motor-driver bus and returns the phantom's servo.
func openGroveServo(busNum int, addr uint8) (kicker.Servo, func(), error) {
	bus, err := sensor.OpenBus(busNum)
	if err != nil {
		return nil, nil, fmt.Errorf("motor driver bus: %w", err)
	}
	g := kicker.NewGroveServo(bus, addr)
	if err := g.Init(); err != nil {
		_ = bus.Close()
		return nil, nil, err
	}
	cleanup := func() {
		// Stop before closing. A motor left driving is the one failure here
		// with a physical consequence.
		_ = g.Stop()
		_ = bus.Close()
	}
	return g, cleanup, nil
}

// hardwareSource opens the accelerometers and the phantom's motor driver.
func hardwareSource(busSpec string, sampleHz, motorBus int, motorAddr uint8) (
	sensor.Source, kicker.Servo, func(), error) {

	src, err := openI2CSource(busSpec, sampleHz)
	if err != nil {
		return nil, nil, nil, err
	}

	// The phantom is optional. A missing motor driver should not stop the
	// sensing path: counting movement is the product, driving the phantom is
	// only how the blind test is staged.
	servo, cleanup, err := openGroveServo(motorBus, motorAddr)
	if err != nil {
		log.Printf("no motor driver on /dev/i2c-%d (%v) — running without a phantom; "+
			"tap the pod by hand instead", motorBus, err)
		return src, nil, func() {}, nil
	}
	log.Printf("  phantom  /dev/i2c-%d  Grove motor driver at 0x%02X", motorBus, motorAddr)
	return src, servo, cleanup, nil
}
