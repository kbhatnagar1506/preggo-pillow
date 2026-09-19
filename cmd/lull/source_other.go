//go:build !linux

package main

import (
	"fmt"

	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// hardwareSource exists so main.go compiles everywhere. I2C is a Linux
// interface; on a Mac there is no /dev/i2c-N to open, so this fails with an
// explanation rather than a link error.
func hardwareSource(busSpec string, sampleHz, motorBus int, motorAddr uint8) (
	sensor.Source, kicker.Servo, func(), error) {
	return nil, nil, nil, fmt.Errorf(
		"-source i2c only works on the Pi (this binary is not Linux). " +
			"Cross-compile with: GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/lull")
}
