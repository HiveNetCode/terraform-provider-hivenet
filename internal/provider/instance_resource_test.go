package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// Enum numbers the API sends for status.
var fakeStatusNumbers = map[string]int{
	"CREATED": 0, "DEPLOYED": 1, "STARTING": 2, "RUNNING": 3, "ERRORED": 4,
	"TERMINATING": 5, "TERMINATED": 6, "STOPPING": 7, "STOPPED": 8, "PAUSED": 9,
}

type fakeInstance struct {
	status string
	// next holds the statuses later GETs step through, one per GET.
	next []string
	// startTo overrides the statuses a start steps through.
	startTo []string
}

// fakeInstanceAPI is a stateful /v1/instances fake for the resource tests.
type fakeInstanceAPI struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance
	actions   []string
}

func newFakeInstanceAPI(t *testing.T, statuses map[string]string) (*fakeInstanceAPI, string) {
	t.Helper()
	f := &fakeInstanceAPI{instances: map[string]*fakeInstance{}}
	for id, s := range statuses {
		f.instances[id] = &fakeInstance{status: s}
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv.URL + "/v1"
}

func (f *fakeInstanceAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/instances/")
	id, action, _ := strings.Cut(rest, "/")
	inst := f.instances[id]
	if !ok || inst == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"Not found.","request_id":"r"}}`))
		return
	}

	if r.Method == http.MethodPost {
		f.actions = append(f.actions, action+" "+id)
		gone := inst.status == "TERMINATING" || inst.status == "TERMINATED"
		switch {
		case action == "terminate" && inst.status == "TERMINATED", action != "terminate" && gone:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"Instance is terminated.","request_id":"r"}}`))
			return
		case action == "start" && inst.startTo != nil:
			inst.next = inst.startTo
		case action == "start":
			inst.next = []string{"STARTING", "RUNNING"}
		case action == "stop":
			inst.next = []string{"STOPPING", "STOPPED"}
		case action == "terminate":
			inst.next = []string{"TERMINATING", "TERMINATED"}
		}
		w.WriteHeader(http.StatusCreated)
		f.writeInstance(w, id, inst)
		return
	}

	if len(inst.next) > 0 {
		inst.status, inst.next = inst.next[0], inst.next[1:]
	}
	f.writeInstance(w, id, inst)
}

func (f *fakeInstanceAPI) writeInstance(w http.ResponseWriter, id string, inst *fakeInstance) {
	reason := ""
	if inst.status == "ERRORED" {
		reason = "image pull failed"
	}
	_, _ = fmt.Fprintf(w, `{"instance_id":%q,"name":"web","status":%d,"region":"france-2","gpu_type":"RTX 4090",
		"size":"1 x GPU","cpu_count":8,"resources":"1 x RTX 4090","bandwidth":1000,"storage_used_gb":100,
		"hourly_rate":0.5,"started_at":{"seconds":"1793908800","nanos":0},"termination_reason":1,
		"guest_ready":1,"guest_ready_reason":%q}`, id, fakeStatusNumbers[inst.status], reason)
}

// set changes an instance behind Terraform's back, as Hive would.
func (f *fakeInstanceAPI) set(id, status string, next ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[id].status, f.instances[id].next = status, next
}

// failStarts makes later starts of id end ERRORED, like a failed image pull.
func (f *fakeInstanceAPI) failStarts(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[id].startTo = []string{"STARTING", "ERRORED"}
}

func (f *fakeInstanceAPI) status(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instances[id].status
}

func (f *fakeInstanceAPI) actionLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func fastPolling(t *testing.T) {
	t.Helper()
	old := pollInterval
	pollInterval = 10 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

const fakeID = "11111111-1111-1111-1111-111111111111"

func importBlock(id string) string {
	return fmt.Sprintf(`
import {
  to = hivenet_instance.web
  id = %q
}
`, id)
}

func instanceConfig(powerState string) string {
	return fmt.Sprintf(`
resource "hivenet_instance" "web" {
  power_state = %q
}
`, powerState)
}

func TestInstanceResource_importAndPowerState(t *testing.T) {
	fastPolling(t)
	f, endpoint := newFakeInstanceAPI(t, map[string]string{fakeID: "RUNNING"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if s := f.status(fakeID); s != "TERMINATED" {
				return fmt.Errorf("after destroy the instance is %s, want TERMINATED", s)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + importBlock(fakeID) + instanceConfig("stopped"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.web", "id", fakeID),
					resource.TestCheckResourceAttr("hivenet_instance.web", "power_state", "stopped"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "status", "STOPPED"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "name", "web"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "location", "france-2"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "cpu_count", "8"),
				),
			},
			{
				Config: fakeProviderConfig(endpoint) + instanceConfig("running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.web", "power_state", "running"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "status", "RUNNING"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "started_at", "2026-11-05T20:00:00Z"),
				),
			},
			{
				ResourceName:            "hivenet_instance.web",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})

	want := []string{"stop " + fakeID, "start " + fakeID, "terminate " + fakeID}
	if got := f.actionLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("API actions = %v, want %v", got, want)
	}
}

func TestInstanceResource_drift(t *testing.T) {
	fastPolling(t)
	f, endpoint := newFakeInstanceAPI(t, map[string]string{fakeID: "RUNNING"})
	cfg := fakeProviderConfig(endpoint) + instanceConfig("running")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + importBlock(fakeID) + instanceConfig("running"),
			},
			{
				// Hive stops it, e.g. when credits run out: the plan starts it again.
				PreConfig: func() { f.set(fakeID, "STOPPED") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hivenet_instance.web", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("hivenet_instance.web", "status", "RUNNING"),
			},
			{
				// Hive terminates it: refresh drops it from state and the plan
				// wants a create, which fails until the API can create.
				PreConfig:   func() { f.set(fakeID, "TERMINATED") },
				Config:      cfg,
				ExpectError: regexp.MustCompile(`Creating instances is not supported yet`),
			},
		},
	})
}

func TestInstanceResource_pausedAndErroredCountAsRunning(t *testing.T) {
	fastPolling(t)
	const pausedID, erroredID = "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	f, endpoint := newFakeInstanceAPI(t, map[string]string{pausedID: "PAUSED", erroredID: "ERRORED"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + fmt.Sprintf(`
import {
  to = hivenet_instance.paused
  id = %q
}

import {
  to = hivenet_instance.errored
  id = %q
}

resource "hivenet_instance" "paused" {
  power_state = "running"
}

resource "hivenet_instance" "errored" {
  power_state = "running"
}
`, pausedID, erroredID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.paused", "status", "PAUSED"),
					resource.TestCheckResourceAttr("hivenet_instance.paused", "power_state", "running"),
					resource.TestCheckResourceAttr("hivenet_instance.errored", "status", "ERRORED"),
					resource.TestCheckResourceAttr("hivenet_instance.errored", "power_state", "running"),
				),
			},
		},
	})

	for _, a := range f.actionLog() {
		if strings.HasPrefix(a, "start ") || strings.HasPrefix(a, "stop ") {
			t.Errorf("unexpected API action %q: PAUSED and ERRORED must not be started", a)
		}
	}
}

func TestInstanceResource_startTimeout(t *testing.T) {
	fastPolling(t)
	f, endpoint := newFakeInstanceAPI(t, map[string]string{fakeID: "STOPPED"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + importBlock(fakeID) + instanceConfig("stopped"),
			},
			{
				// The start never settles: the instance stays ERRORED.
				PreConfig: func() { f.failStarts(fakeID) },
				Config: fakeProviderConfig(endpoint) + `
resource "hivenet_instance" "web" {
  power_state = "running"
  timeouts {
    update = "1s"
  }
}
`,
				// Terraform wraps long error lines, hence (?s).
				ExpectError: regexp.MustCompile(`(?s)timed out after 1s waiting.*RUNNING; last status ERRORED.*image pull failed`),
			},
		},
	})
}

func TestInstanceResource_createNotSupported(t *testing.T) {
	_, endpoint := newFakeInstanceAPI(t, nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      fakeProviderConfig(endpoint) + instanceConfig("running"),
				ExpectError: regexp.MustCompile(`Creating instances is not supported yet`),
			},
		},
	})
}

// TestAccInstanceResource_fixture imports the test org's fixture instance,
// starts and stops it, and leaves it stopped. It never destroys it: the last
// step drops it from state with a removed block. Destroy is covered by the
// unit tests until the API can create instances.
func TestAccInstanceResource_fixture(t *testing.T) {
	id := acctest.FixtureInstanceID(t)
	// prevent_destroy stops the post-test destroy from terminating the
	// fixture when an earlier step fails.
	cfg := func(powerState string) string {
		return fmt.Sprintf(`
resource "hivenet_instance" "fixture" {
  power_state = %q

  lifecycle {
    prevent_destroy = true
  }
}
`, powerState)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// removed blocks with lifecycle destroy = false.
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_7_0)},
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
import {
  to = hivenet_instance.fixture
  id = %q
}
`, id) + cfg("stopped"),
				Check: resource.TestCheckResourceAttr("hivenet_instance.fixture", "status", "STOPPED"),
			},
			{
				Config: cfg("running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.fixture", "status", "RUNNING"),
					resource.TestCheckResourceAttrSet("hivenet_instance.fixture", "started_at"),
				),
			},
			{
				ResourceName:            "hivenet_instance.fixture",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "guest_ready"},
			},
			{
				// Leave it stopped: no compute cost, and each run resets the
				// 10-day timer after which Hive terminates stopped instances.
				Config: cfg("stopped"),
				Check:  resource.TestCheckResourceAttr("hivenet_instance.fixture", "status", "STOPPED"),
			},
			{
				Config: `
removed {
  from = hivenet_instance.fixture
  lifecycle {
    destroy = false
  }
}
`,
			},
		},
	})
}
