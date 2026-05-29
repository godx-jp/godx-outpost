// Package qr provides helpers for generating pairing QR codes.
//
// PairingPayload encodes connection parameters as compact JSON suitable for
// embedding in a QR code. Render prints the resulting ASCII QR to stdout so
// that a mobile device can scan it to initiate a pairing session.
package qr

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"
)

// pairingPayloadJSON is the compact on-wire structure encoded inside the QR.
type pairingPayloadJSON struct {
	URL         string `json:"url"`
	DeviceID    string `json:"deviceID"`
	PairingCode string `json:"pairingCode"`
}

// PairingPayload builds the compact JSON string to embed in a pairing QR code.
// All three fields are required by the mobile app's scanner. The function never
// returns an error because the struct contains only plain strings; the returned
// value is always valid JSON.
func PairingPayload(url, deviceID, pairingCode string) string {
	p := pairingPayloadJSON{
		URL:         url,
		DeviceID:    deviceID,
		PairingCode: pairingCode,
	}
	b, err := json.Marshal(p)
	if err != nil {
		// json.Marshal on a plain struct with string fields cannot fail, but
		// handle it gracefully rather than panicking.
		return fmt.Sprintf(`{"url":%q,"deviceID":%q,"pairingCode":%q}`, url, deviceID, pairingCode)
	}
	return string(b)
}

// Render prints an ASCII QR code for payload to stdout.
//
// It uses half-block characters (▀▄█) so the code fits in roughly half the
// vertical space of a full-block rendering, making it easier to display in a
// standard terminal window. Error-correction level L is used to keep the
// symbol small; the payload must be under ~2953 bytes for L level.
func Render(payload string) error {
	if payload == "" {
		return fmt.Errorf("qr: payload must not be empty")
	}
	cfg := qrterminal.Config{
		Level:      qr.L,
		Writer:     os.Stdout,
		HalfBlocks: true,
		QuietZone:  qrterminal.QUIET_ZONE,
	}
	qrterminal.GenerateWithConfig(payload, cfg)
	return nil
}
