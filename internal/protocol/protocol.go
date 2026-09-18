package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	KiB                 = 1 << 10
	HeaderSizeBytes     = 4
	MaxPayloadSizeBytes = 10 * KiB // 10 KiB
)

const (
	KindRegister = "REGISTER"
	KindOk       = "OK"
	KindError    = "ERROR"
)

type Envelope struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// RegisterMessage agent -> server
type RegisterMessage struct {
	Name string `json:"name"`
}

// OkMessage server -> agent
type OkMessage struct {
	Name       string `json:"name"`
	PublicPort uint32 `json:"public_port"`
}

// ErrorMessage server -> agent
type ErrorMessage struct {
	Reason string `json:"reason"`
}

func decodePayload[T any](payload []byte) (any, error) {
	var target T

	if err := json.Unmarshal(payload, &target); err != nil {
		return nil, err
	}

	return target, nil
}

var registry = map[string]func([]byte) (any, error){
	KindRegister: decodePayload[RegisterMessage],
	KindOk:       decodePayload[OkMessage],
	KindError:    decodePayload[ErrorMessage],
}

func WriteMessage(w io.Writer, kind string, payload any) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	env := Envelope{
		Kind:    kind,
		Payload: payloadBytes,
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	if len(envBytes) > MaxPayloadSizeBytes {
		return fmt.Errorf("outgoing payload size (%d bytes) exceeds maximum limit (%d bytes)", len(envBytes), MaxPayloadSizeBytes)
	}

	msgLength := uint32(len(envBytes))
	header := make([]byte, HeaderSizeBytes)
	binary.BigEndian.PutUint32(header, msgLength)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("failed to write message header: %w", err)
	}

	if _, err := w.Write(envBytes); err != nil {
		return fmt.Errorf("failed to write message body: %w", err)
	}

	return nil
}

func ReadMessage(r io.Reader) (any, error) {
	header := make([]byte, HeaderSizeBytes)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(header)
	if length > MaxPayloadSizeBytes {
		return nil, fmt.Errorf("incoming payload size (%d bytes) exceeds max limit (%d bytes)", length, MaxPayloadSizeBytes)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("failed to read full payload (expected %d bytes): %w", length, err)
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshall envelope: %w", err)
	}

	decoder, exists := registry[env.Kind]
	if !exists {
		return nil, fmt.Errorf("unsupported message kind: %s", env.Kind)
	}

	return decoder(env.Payload)
}
