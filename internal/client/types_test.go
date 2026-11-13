package client

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstanceDecodesBothShapes(t *testing.T) {
	want := time.Date(2026, 11, 5, 20, 0, 0, 500_000_000, time.UTC)
	cases := map[string]string{
		"documented": `{"status":"RUNNING","started_at":"2026-11-05T20:00:00.5Z"}`,
		"protobuf":   `{"status":3,"started_at":{"seconds":1793908800,"nanos":500000000}}`,
		"int64 str":  `{"status":3,"started_at":{"seconds":"1793908800","nanos":500000000}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var inst Instance
			if err := json.Unmarshal([]byte(body), &inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status != "RUNNING" {
				t.Errorf("Status = %q", inst.Status)
			}
			if inst.StartedAt == nil || !inst.StartedAt.Equal(want) {
				t.Errorf("StartedAt = %v, want %v", inst.StartedAt, want)
			}
		})
	}
}

func TestInstanceDecodesMissingAndUnknown(t *testing.T) {
	var inst Instance
	if err := json.Unmarshal([]byte(`{"status":42,"started_at":null}`), &inst); err != nil {
		t.Fatal(err)
	}
	if inst.Status != "UNKNOWN_42" || inst.StartedAt != nil {
		t.Errorf("got %q, %v", inst.Status, inst.StartedAt)
	}
}

func TestInstanceEnumsDecodeBothShapes(t *testing.T) {
	cases := map[string]string{
		"documented": `{"status":"PAUSED","termination_reason":"CREDIT_EXHAUSTION","guest_ready":"DEGRADED"}`,
		"protobuf":   `{"status":9,"termination_reason":2,"guest_ready":2}`,
		"prefixed":   `{"status":9,"termination_reason":2,"guest_ready":"GUEST_READY_DEGRADED"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var inst Instance
			if err := json.Unmarshal([]byte(body), &inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status != "PAUSED" || inst.TerminationReason == nil || *inst.TerminationReason != "CREDIT_EXHAUSTION" || inst.GuestReady != "DEGRADED" {
				t.Errorf("got status %q, reason %v, guest %q", inst.Status, inst.TerminationReason, inst.GuestReady)
			}
		})
	}
}
