package main

import (
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

func TestParsePhonesHappyPath(t *testing.T) {
	got, err := parsePhones("abdo_a=192.168.1.21,abdo_b=192.168.1.22,ref=192.168.1.23:8080")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 handsets, got %d", len(got))
	}
	if got[0].Node != sensor.NodeAbdoA || got[2].Node != sensor.NodeRef {
		t.Errorf("nodes parsed wrong: %+v", got)
	}
	if got[2].BaseURL() != "http://192.168.1.23:8080" {
		t.Errorf("host parsed wrong: %q", got[2].BaseURL())
	}
}

// The reference node is the whole idea. Running without it would count
// maternal movement as fetal movement, so it has to be a hard error rather
// than a warning somebody misses at 3am.
func TestParsePhonesRequiresReference(t *testing.T) {
	_, err := parsePhones("abdo_a=192.168.1.21,abdo_b=192.168.1.22")
	if err == nil {
		t.Fatal("expected an error with no ref node")
	}
	if !strings.Contains(err.Error(), "ref") {
		t.Errorf("the error should name the missing ref node: %v", err)
	}
}

func TestParsePhonesRequiresAnAbdominalNode(t *testing.T) {
	if _, err := parsePhones("ref=192.168.1.23"); err == nil {
		t.Fatal("expected an error with only a ref node")
	}
}

func TestParsePhonesRejectsBadInput(t *testing.T) {
	cases := []struct{ name, spec string }{
		{"empty", ""},
		{"no equals", "abdo_a 192.168.1.21,ref=192.168.1.23"},
		{"unknown node", "belly=192.168.1.21,ref=192.168.1.23"},
		{"duplicate node", "ref=192.168.1.23,ref=192.168.1.24"},
		{"missing host", "abdo_a=,ref=192.168.1.23"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parsePhones(c.spec); err == nil {
				t.Errorf("parsePhones(%q) should have failed", c.spec)
			}
		})
	}
}

func TestParsePhonesToleratesWhitespaceAndCase(t *testing.T) {
	got, err := parsePhones("  ABDO_A = 192.168.1.21 , Ref=192.168.1.23 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Host != "192.168.1.21" {
		t.Errorf("host not trimmed: %q", got[0].Host)
	}
}
