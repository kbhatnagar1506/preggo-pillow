//go:build !linux

package main

import (
	"context"
	"fmt"
	"time"
)

// startKnob exists so main.go builds off the Pi. The dial is read through the
// Grove HAT's ADC over I2C, which is a Linux interface.
func startKnob(_ context.Context, _, _ int, _ func(time.Time, int)) (func(), error) {
	return nil, fmt.Errorf("-knob only works on the Pi (this binary is not Linux)")
}
