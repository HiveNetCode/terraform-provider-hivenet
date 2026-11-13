// Command acccost prints the acceptance-test org's spend for the current month
// as Markdown, for the GitHub Actions job summary.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/acctest"
	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
)

func main() {
	c, err := acctest.Client()
	if err != nil {
		log.Fatal(err)
	}
	instances, err := c.ListInstances(context.Background(), client.ListInstancesOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(report(instances, time.Now().UTC()))
}

// report sums lifetime cost of instances started this month. Test instances
// live minutes, so lifetime cost is close enough to cost within the month.
func report(instances []client.Instance, now time.Time) string {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var cost, runway float64
	count := 0
	for _, inst := range instances {
		runway = max(runway, inst.TTLRemainingHours)
		if inst.StartedAt == nil || inst.StartedAt.Before(monthStart) {
			continue
		}
		cost += inst.TotalCostIncurred
		count++
	}

	out := fmt.Sprintf("### Acceptance-test spend, %s\n\n", now.Format("January 2006"))
	out += "| Instances started | Credits spent | Credit runway |\n|---|---|---|\n"
	runwayCell := "n/a"
	if len(instances) > 0 {
		runwayCell = fmt.Sprintf("%.0f h", runway)
	}
	out += fmt.Sprintf("| %d | %.2f | %s |\n", count, cost, runwayCell)
	return out
}
