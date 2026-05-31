package protocol

import (
	"bytes"
	"testing"
)

func TestBinaryFrameRoundTrip(t *testing.T) {
	cases := []BinaryFrame{
		{Kind: BinTermOutput, StreamID: "t-abc", Payload: []byte("hello")},
		{Kind: BinTermInput, StreamID: "", Payload: []byte("x")},      // empty stream id
		{Kind: BinFSData, StreamID: "id", Payload: nil},               // empty payload
		{Kind: BinTermOutput, StreamID: "🍎", Payload: bytes.Repeat([]byte{0xAB}, 5000)}, // unicode id + big payload
	}
	for _, in := range cases {
		got, err := DecodeBinaryFrame(in.Encode())
		if err != nil {
			t.Fatalf("decode(%v): %v", in.StreamID, err)
		}
		if got.Kind != in.Kind || got.StreamID != in.StreamID || !bytes.Equal(got.Payload, in.Payload) {
			t.Fatalf("round-trip mismatch:\n in=%+v\n got=%+v", in, got)
		}
	}
}

func TestDecodeBinaryFrameShort(t *testing.T) {
	for _, b := range [][]byte{nil, {1}, {1, 0}} {
		if _, err := DecodeBinaryFrame(b); err != ErrShortFrame {
			t.Fatalf("want ErrShortFrame for %v, got %v", b, err)
		}
	}
	// idLen header claims 10 bytes but only 1 is present.
	if _, err := DecodeBinaryFrame([]byte{1, 0, 10, 'a'}); err != ErrShortFrame {
		t.Fatalf("want ErrShortFrame for over-long idLen, got %v", err)
	}
}

func TestEnvelopeBind(t *testing.T) {
	type payload struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	e, err := NewEnvelope(ChTerm, "create", "1", payload{A: 7, B: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Ch != ChTerm || e.Type != "create" || e.ID != "1" {
		t.Fatalf("envelope fields: %+v", e)
	}
	var got payload
	if err := e.Bind(&got); err != nil {
		t.Fatal(err)
	}
	if got.A != 7 || got.B != "hi" {
		t.Fatalf("bind: %+v", got)
	}

	// nil data → Bind leaves the target untouched (no error).
	e2, _ := NewEnvelope(ChCtrl, "ping", "2", nil)
	var z payload
	if err := e2.Bind(&z); err != nil {
		t.Fatal(err)
	}
	if z.A != 0 || z.B != "" {
		t.Fatalf("expected zero value, got %+v", z)
	}
}
