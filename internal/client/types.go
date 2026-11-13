package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The API currently sends instance enums as numbers and timestamps as
// {seconds, nanos}, not the documented form. These types accept both.

// Names are listed in enum-number order.
var (
	statusNames = []string{
		"CREATED", "DEPLOYED", "STARTING", "RUNNING", "ERRORED",
		"TERMINATING", "TERMINATED", "STOPPING", "STOPPED", "PAUSED",
	}
	terminationReasonNames = []string{"OTHER_UNKNOWN", "USER_INITIATED", "CREDIT_EXHAUSTION"}
	guestReadyNames        = []string{"UNKNOWN", "READY", "DEGRADED"}
)

// decodeEnum accepts an enum name or its protobuf number. Numbers outside
// names decode to "UNKNOWN_<n>" so a newer API doesn't break reads.
func decodeEnum(b []byte, names []string) (string, error) {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		return name, nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return "", fmt.Errorf("%s is neither a name nor an enum number", b)
	}
	if n >= 0 && n < len(names) {
		return names[n], nil
	}
	return "UNKNOWN_" + strconv.Itoa(n), nil
}

// Status is an instance lifecycle state, e.g. "RUNNING".
type Status string

// UnmarshalJSON accepts the status name or its protobuf enum number.
func (s *Status) UnmarshalJSON(b []byte) error {
	v, err := decodeEnum(b, statusNames)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	*s = Status(v)
	return nil
}

// TerminationReason says why an instance was terminated, e.g. "USER_INITIATED".
// The protobuf zero value is OTHER_UNKNOWN, so it is only meaningful once the
// instance has a TerminatedAt.
type TerminationReason string

// UnmarshalJSON accepts the reason name or its protobuf enum number.
func (r *TerminationReason) UnmarshalJSON(b []byte) error {
	v, err := decodeEnum(b, terminationReasonNames)
	if err != nil {
		return fmt.Errorf("termination_reason: %w", err)
	}
	*r = TerminationReason(v)
	return nil
}

// GuestReady says whether the guest OS is reachable: "READY", "DEGRADED" or "UNKNOWN".
type GuestReady string

// UnmarshalJSON accepts the name, with or without the GUEST_READY_ prefix the
// protobuf enum uses, or its protobuf enum number.
func (g *GuestReady) UnmarshalJSON(b []byte) error {
	v, err := decodeEnum(b, guestReadyNames)
	if err != nil {
		return fmt.Errorf("guest_ready: %w", err)
	}
	*g = GuestReady(strings.TrimPrefix(v, "GUEST_READY_"))
	return nil
}

// Timestamp is a point in time.
type Timestamp struct {
	time.Time
}

// UnmarshalJSON accepts an RFC 3339 string or a protobuf {seconds, nanos} object.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if err := t.Time.UnmarshalJSON(b); err == nil {
		return nil
	}
	var pb struct {
		Seconds json.Number `json:"seconds"`
		Nanos   int64       `json:"nanos"`
	}
	if err := json.Unmarshal(b, &pb); err != nil {
		return fmt.Errorf("timestamp: %s is neither RFC 3339 nor {seconds, nanos}", b)
	}
	secs, err := pb.Seconds.Int64()
	if err != nil {
		return fmt.Errorf("timestamp seconds: %w", err)
	}
	t.Time = time.Unix(secs, pb.Nanos).UTC()
	return nil
}
