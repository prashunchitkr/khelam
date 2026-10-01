package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	if err := WriteMessage(&buf, KindOk, OkMessage{Name: "mc", PublicPort: 41000}); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	msg, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	ok, isOk := msg.(OkMessage)
	if !isOk {
		t.Fatalf("got %T, want OkMessage", msg)
	}
	if ok.Name != "mc" || ok.PublicPort != 41000 {
		t.Fatalf("got %+v", ok)
	}
}

func TestReadMessageConsumesExactlyOneFrame(t *testing.T) {
	var buf bytes.Buffer
	WriteMessage(&buf, KindRegister, RegisterMessage{Name: "a"})
	buf.WriteString("trailing")

	if _, err := ReadMessage(&buf); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if buf.String() != "trailing" {
		t.Fatalf("ReadMessage consumed past its frame; remaining %q", buf.String())
	}
}

func TestWriteMessageRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	err := WriteMessage(&buf, KindError, ErrorMessage{Reason: strings.Repeat("x", MaxPayloadSizeBytes)})
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes for a rejected message", buf.Len())
	}
}

func TestReadMessageRejectsOversizedHeader(t *testing.T) {
	header := make([]byte, HeaderSizeBytes)
	binary.BigEndian.PutUint32(header, MaxPayloadSizeBytes+1)

	if _, err := ReadMessage(bytes.NewReader(header)); err == nil {
		t.Fatal("expected error for oversized header")
	}
}

func TestReadMessageRejectsUnknownKind(t *testing.T) {
	var buf bytes.Buffer
	WriteMessage(&buf, "BOGUS", struct{}{})

	if _, err := ReadMessage(&buf); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}
