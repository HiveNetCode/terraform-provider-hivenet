package main

import (
	"strings"
	"testing"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
)

func TestReportCountsOnlyThisMonth(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	thisMonth := client.Timestamp{Time: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	lastMonth := client.Timestamp{Time: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}

	got := report([]client.Instance{
		{StartedAt: &thisMonth, TotalCostIncurred: 1.25, TTLRemainingHours: 40},
		{StartedAt: &thisMonth, TotalCostIncurred: 0.75, TTLRemainingHours: 40},
		{StartedAt: &lastMonth, TotalCostIncurred: 9, TTLRemainingHours: 40},
		{StartedAt: nil, TTLRemainingHours: 40},
	}, now)

	if !strings.Contains(got, "| 2 | 2.00 | 40 h |") {
		t.Fatalf("unexpected report:\n%s", got)
	}
}
