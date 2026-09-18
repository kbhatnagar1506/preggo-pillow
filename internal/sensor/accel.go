package sensor

import (
	"encoding/binary"
	"fmt"
)

// Chip is one of the accelerometers Seeed ships under the single name
// "Grove 3-Axis Digital Accelerometer". Which one is in the bag decides how the
// raw bytes become g, so the code autodetects rather than asking anyone to
// know.
type Chip string

const (
	ChipADXL345 Chip = "adxl345"  // 10-13 bit, up to 3200 Hz  — the good one
	ChipLIS3DH  Chip = "lis3dhtr" // 8-12 bit, up to 5.3 kHz   — also fine
	ChipMMA7660 Chip = "mma7660"  // 6-bit, 120 Hz max         — see BranchB
	ChipUnknown Chip = "unknown"
)

// Addresses each chip answers on. i2cdetect prints these.
const (
	AddrADXL345 = 0x53
	AddrLIS3DH  = 0x19
	AddrLIS3DHB = 0x18
	AddrMMA7660 = 0x4C
)

// ChipAt maps an I2C address to the chip that lives there.
func ChipAt(addr uint8) Chip {
	switch addr {
	case AddrADXL345:
		return ChipADXL345
	case AddrLIS3DH, AddrLIS3DHB:
		return ChipLIS3DH
	case AddrMMA7660:
		return ChipMMA7660
	default:
		return ChipUnknown
	}
}

// BranchB reports whether this chip forces the acoustic channel to be primary.
//
// The MMA7660FC is 6-bit over +/-1.5g, which is about 0.047g per count. Fetal
// movement at the abdominal wall is a small acceleration, and quantising it
// that coarsely loses it. The product does not change: the contact microphone
// becomes the primary channel and the accelerometer drops to gating and
// maternal-movement rejection.
func (c Chip) BranchB() bool { return c == ChipMMA7660 }

// Registers and scale factors, per datasheet.
type chipProfile struct {
	dataReg  uint8   // first data register
	dataLen  int     // bytes to read
	scale    float64 // raw count -> g
	autoIncr bool    // set the MSB of the register address to auto-increment
	initSeq  [][2]uint8
	decode   func(raw []byte, scale float64) (x, y, z float64, err error)
}

var profiles = map[Chip]chipProfile{
	ChipADXL345: {
		dataReg:  0x32,
		dataLen:  6,
		scale:    0.0039, // 3.9 mg/LSB in full-resolution mode
		autoIncr: false,
		initSeq: [][2]uint8{
			{0x2D, 0x08}, // POWER_CTL: leave standby, start measuring
			{0x31, 0x0B}, // DATA_FORMAT: full resolution, +/-16g
			{0x2C, 0x0C}, // BW_RATE: 400 Hz output
		},
		decode: decodeLE16,
	},
	ChipLIS3DH: {
		dataReg:  0x28,
		dataLen:  6,
		scale:    0.001, // approx, +/-2g high-resolution
		autoIncr: true,  // LIS3DH needs bit 7 set to read consecutive registers
		initSeq: [][2]uint8{
			{0x20, 0x77}, // CTRL_REG1: 400 Hz, X/Y/Z enabled
			{0x23, 0x88}, // CTRL_REG4: high resolution, block data update
		},
		decode: decodeLE16Shift4,
	},
	ChipMMA7660: {
		dataReg:  0x00,
		dataLen:  3,
		scale:    1.5 / 32.0, // 6 bits across +/-1.5g
		autoIncr: false,
		initSeq: [][2]uint8{
			{0x07, 0x00}, // standby, so MODE becomes writable
			{0x08, 0x01}, // SR: 120 samples/sec
			{0x07, 0x01}, // active
		},
		decode: decode6Bit,
	},
}

// decodeLE16 reads three little-endian signed 16-bit values.
func decodeLE16(raw []byte, scale float64) (x, y, z float64, err error) {
	if len(raw) < 6 {
		return 0, 0, 0, fmt.Errorf("short read: %d bytes, want 6", len(raw))
	}
	rx := int16(binary.LittleEndian.Uint16(raw[0:2]))
	ry := int16(binary.LittleEndian.Uint16(raw[2:4]))
	rz := int16(binary.LittleEndian.Uint16(raw[4:6]))
	return float64(rx) * scale, float64(ry) * scale, float64(rz) * scale, nil
}

// decodeLE16Shift4 is the LIS3DH's left-justified 12-bit format: the value sits
// in the top 12 bits, so it has to be shifted down before scaling. Forgetting
// the shift reads 16x too large and every threshold in the detector is wrong.
func decodeLE16Shift4(raw []byte, scale float64) (x, y, z float64, err error) {
	if len(raw) < 6 {
		return 0, 0, 0, fmt.Errorf("short read: %d bytes, want 6", len(raw))
	}
	rx := int16(binary.LittleEndian.Uint16(raw[0:2])) >> 4
	ry := int16(binary.LittleEndian.Uint16(raw[2:4])) >> 4
	rz := int16(binary.LittleEndian.Uint16(raw[4:6])) >> 4
	return float64(rx) * scale, float64(ry) * scale, float64(rz) * scale, nil
}

// decode6Bit reads the MMA7660's three 6-bit two's-complement values.
func decode6Bit(raw []byte, scale float64) (x, y, z float64, err error) {
	if len(raw) < 3 {
		return 0, 0, 0, fmt.Errorf("short read: %d bytes, want 3", len(raw))
	}
	v := make([]float64, 3)
	for i := 0; i < 3; i++ {
		b := raw[i] & 0x3F // the top two bits are status, not data
		s := int8(b)
		if b > 31 {
			s = int8(int(b) - 64) // sign-extend from 6 bits
		}
		v[i] = float64(s) * scale
	}
	return v[0], v[1], v[2], nil
}

// DecodeSample turns a raw register read into g, for whichever chip produced it.
// Split out from the I2C transport so it can be tested with no hardware at all.
func DecodeSample(c Chip, raw []byte) (x, y, z float64, err error) {
	p, ok := profiles[c]
	if !ok {
		return 0, 0, 0, fmt.Errorf("unknown chip %q", c)
	}
	return p.decode(raw, p.scale)
}
