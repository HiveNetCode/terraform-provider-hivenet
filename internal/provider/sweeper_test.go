package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/acctest"
	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestMain runs the sweepers when go test is called with -sweep.
func TestMain(m *testing.M) {
	resource.TestMain(m)
}

func init() {
	resource.AddTestSweepers("hivenet_instance", &resource.Sweeper{
		Name: "hivenet_instance",
		F:    sweepInstances,
	})
}

// sweepInstances terminates every instance whose name starts with
// acctest.NamePrefix. The -sweep value is ignored: there are no regions to scope.
func sweepInstances(_ string) error {
	c, err := acctest.Client()
	if err != nil {
		return err
	}
	ctx := context.Background()

	instances, err := c.ListInstances(ctx, client.ListInstancesOptions{FreeTextSearch: acctest.NamePrefix})
	if err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}

	var errs []error
	for _, inst := range instances {
		if !strings.HasPrefix(inst.Name, acctest.NamePrefix) {
			continue
		}
		if inst.Status == "TERMINATED" || inst.Status == "TERMINATING" {
			continue
		}
		log.Printf("[INFO] terminating leaked instance %s (%s, %s)", inst.Name, inst.ID, inst.Status)
		// 409 means it reached TERMINATED since we listed it.
		if err := c.TerminateInstance(ctx, inst.ID); err != nil && !client.IsStatus(err, http.StatusConflict) {
			errs = append(errs, fmt.Errorf("terminating %s: %w", inst.ID, err))
		}
	}
	return errors.Join(errs...)
}
