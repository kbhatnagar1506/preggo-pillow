package sensor

import (
	"math"
	"testing"
)

func nearly(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.4f, want %.4f (±%.4f)", what, got, want, tol)
	}
}

func TestChipAtKnownAddresses(t *testing.T) {
	cases := map[uint8]Chip{
		0x53: ChipADXL345,
		0x19: ChipLIS3DH,
		0x18: ChipLIS3DH,
		0x4C: ChipMMA7660,
		0x77: ChipUnknown,
	}
	for addr, want := range cases {
		if got := ChipAt(addr); got != want {
			t.Errorf("address 0x%02X: got %q, want %q", addr, got, want)
		}
	}
}

// Only the 6-bit part forces the acoustic channel to become primary.
func TestOnlyMMA7660ForcesBranchB(t *testing.T) {
	if !ChipMMA7660.BranchB() {
		t.Error("MMA7660 is 6-bit over ±1.5g; it must force branch B")
	}
	for _, c := range []Chip{ChipADXL345, ChipLIS3DH} {
		if c.BranchB() {
			t.Errorf("%q should not force branch B", c)
		}
	}
}

// One g on Z, nothing on X or Y: the sensor lying flat on a table.
func TestDecodeADXL345OneG(t *testing.T) {
	// 256 counts × 3.9 mg = 0.998 g
	raw := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	x, y, z, err := DecodeSample(ChipADXL345, raw)
	if err != nil {
		t.Fatal(err)
	}
	nearly(t, x, 0, 0.001, "x")
	nearly(t, y, 0, 0.001, "y")
	nearly(t, z, 0.998, 0.01, "z")
}

func TestDecodeADXL345Negative(t *testing.T) {
	// -256 in little-endian two's complement
	raw := []byte{0x00, 0xFF, 0x00, 0x00, 0x00, 0x00}
	x, _, _, err := DecodeSample(ChipADXL345, raw)
	if err != nil {
		t.Fatal(err)
	}
	nearly(t, x, -0.998, 0.01, "x")
}

// The LIS3DH left-justifies its 12-bit value in a 16-bit word. Forgetting the
// >>4 reads sixteen times too large and every threshold in the detector is
// wrong — this test exists to catch exactly that.
func TestDecodeLIS3DHShiftsBeforeScaling(t *testing.T) {
	// 1000 << 4 = 16000 = 0x3E80, little-endian
	raw := []byte{0x80, 0x3E, 0x00, 0x00, 0x00, 0x00}
	x, _, _, err := DecodeSample(ChipLIS3DH, raw)
	if err != nil {
		t.Fatal(err)
	}
	nearly(t, x, 1.0, 0.01, "x") // 1000 counts × 0.001 g
}

func TestDecodeMMA7660SignExtends(t *testing.T) {
	// 6-bit two's complement: 0x20 is -32, 0x1F is +31
	raw := []byte{0x1F, 0x20, 0x00}
	x, y, z, err := DecodeSample(ChipMMA7660, raw)
	if err != nil {
		t.Fatal(err)
	}
	nearly(t, x, 31*(1.5/32), 0.01, "x (+31)")
	nearly(t, y, -32*(1.5/32), 0.01, "y (-32)")
	nearly(t, z, 0, 0.01, "z")
}

// The top two bits of an MMA7660 byte are status, not data. Masking them off
// is the difference between a reading and nonsense.
func TestDecodeMMA7660MasksStatusBits(t *testing.T) {
	withStatus := []byte{0xC0 | 0x10, 0x00, 0x00} // status bits set, value 16
	clean := []byte{0x10, 0x00, 0x00}
	a, _, _, err := DecodeSample(ChipMMA7660, withStatus)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := DecodeSample(ChipMMA7660, clean)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("status bits changed the reading: %.4f vs %.4f", a, b)
	}
}

func TestDecodeRejectsShortReads(t *testing.T) {
	for _, c := range []Chip{ChipADXL345, ChipLIS3DH} {
		if _, _, _, err := DecodeSample(c, []byte{0x00, 0x00}); err == nil {
			t.Errorf("%q accepted a 2-byte read, want an error", c)
		}
	}
	if _, _, _, err := DecodeSample(ChipMMA7660, []byte{0x00}); err == nil {
		t.Error("MMA7660 accepted a 1-byte read, want an error")
	}
}

func TestDecodeRejectsUnknownChip(t *testing.T) {
	if _, _, _, err := DecodeSample(ChipUnknown, make([]byte, 6)); err == nil {
		t.Error("decoded a sample for an unknown chip")
	}
}

// Whatever the chip, a sensor lying flat must read about 1 g total. This is the
// sanity check you would run on the bench, expressed as a test.
func TestOneGravityAcrossEveryChip(t *testing.T) {
	cases := []struct {
		chip Chip
		raw  []byte
	}{
		{ChipADXL345, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x01}},
		{ChipLIS3DH, []byte{0x00, 0x00, 0x00, 0x00, 0xA0, 0x3E}},
		{ChipMMA7660, []byte{0x00, 0x00, 0x15}},
	}
	for _, c := range cases {
		x, y, z, err := DecodeSample(c.chip, c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.chip, err)
		}
		mag := math.Sqrt(x*x + y*y + z*z)
		if mag < 0.9 || mag > 1.1 {
			t.Errorf("%s: flat on a table reads %.3f g, want about 1.0", c.chip, mag)
		}
	}
}
