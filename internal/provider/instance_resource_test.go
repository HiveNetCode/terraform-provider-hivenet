package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/acctest"
	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	sdkacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
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
	name   string
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
	created   int
	// createFails makes the next create answer with this status. With
	// createStillHappens, the instance is created anyway, as after a 500
	// whose outcome is unknown.
	createFails        int
	createStillHappens bool
}

func newFakeInstanceAPI(t *testing.T, statuses map[string]string) (*fakeInstanceAPI, string) {
	t.Helper()
	f := &fakeInstanceAPI{instances: map[string]*fakeInstance{}}
	for id, s := range statuses {
		f.instances[id] = &fakeInstance{name: "web", status: s}
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
	if r.URL.Path == "/v1/instances" {
		f.serveCollection(w, r)
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

// serveCollection lists instances, or creates one in CREATED that starts on
// the next GETs.
func (f *fakeInstanceAPI) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		search := r.URL.Query().Get("free_text_search")
		var items []string
		for id, inst := range f.instances {
			if strings.Contains(inst.name, search) {
				var b strings.Builder
				f.writeInstance(&b, id, inst)
				items = append(items, b.String())
			}
		}
		_, _ = fmt.Fprintf(w, `{"data":[%s],"pagination":{"next":null}}`, strings.Join(items, ","))
		return
	}

	var body struct {
		Name    string `json:"name"`
		ImageID string `json:"image_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.actions = append(f.actions, "create "+body.Name+" "+body.ImageID)
	status := f.createFails
	f.createFails = 0
	if status != 0 && !f.createStillHappens {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"ERR","message":"create failed","request_id":"r"}}`)
		return
	}
	f.created++
	id := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", f.created)
	inst := &fakeInstance{name: body.Name, status: "CREATED", next: []string{"STARTING", "RUNNING"}}
	f.instances[id] = inst
	if status != 0 {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"ERR","message":"create outcome unknown","request_id":"r"}}`)
		return
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"instance":`))
	f.writeInstance(w, id, inst)
	_, _ = w.Write([]byte(`}`))
}

func (f *fakeInstanceAPI) writeInstance(w io.Writer, id string, inst *fakeInstance) {
	reason := ""
	if inst.status == "ERRORED" {
		reason = "image pull failed"
	}
	_, _ = fmt.Fprintf(w, `{"instance_id":%q,"name":%q,"status":%d,"region":"france-2","gpu_type":"RTX 4090",
		"size":"1 x GPU","cpu_count":8,"resources":"1 x RTX 4090","bandwidth":1000,"storage_used_gb":100,
		"hourly_rate":0.5,"started_at":{"seconds":"1793908800","nanos":0},"termination_reason":1,
		"guest_ready":1,"guest_ready_reason":%q}`, id, inst.name, fakeStatusNumbers[inst.status], reason)
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

// failNextCreate makes the next create answer status; with stillCreates the
// instance is created anyway.
func (f *fakeInstanceAPI) failNextCreate(status int, stillCreates bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createFails, f.createStillHappens = status, stillCreates
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

func importBlock() string {
	return fmt.Sprintf(`
import {
  to = hivenet_instance.web
  id = %q
}
`, fakeID)
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
				Config: fakeProviderConfig(endpoint) + importBlock() + instanceConfig("stopped"),
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
				Config: fakeProviderConfig(endpoint) + importBlock() + instanceConfig("running"),
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
				// wants a create, which needs the create arguments.
				PreConfig:   func() { f.set(fakeID, "TERMINATED") },
				Config:      cfg,
				ExpectError: regexp.MustCompile(`"name" is required to create an instance`),
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
				Config: fakeProviderConfig(endpoint) + importBlock() + instanceConfig("stopped"),
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

func TestInstanceResource_createNeedsArguments(t *testing.T) {
	_, endpoint := newFakeInstanceAPI(t, nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      fakeProviderConfig(endpoint) + instanceConfig("running"),
				ExpectError: regexp.MustCompile(`"preset_id" is required to create an instance`),
			},
		},
	})
}

const fakePresetID = "99999999-9999-9999-9999-999999999999"

func createConfig(name, imageID, powerState string) string {
	return fmt.Sprintf(`
resource "hivenet_instance" "web" {
  name        = %q
  preset_id   = %q
  image_type  = "vm"
  image_id    = %q
  power_state = %q
}
`, name, fakePresetID, imageID, powerState)
}

func TestInstanceResource_createAndReplace(t *testing.T) {
	fastPolling(t)
	f, endpoint := newFakeInstanceAPI(t, nil)
	first, second := "aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if s := f.status(second); s != "TERMINATED" {
				return fmt.Errorf("after destroy the instance is %s, want TERMINATED", s)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + createConfig("web-1", "ubuntu24.04", "running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.web", "id", first),
					resource.TestCheckResourceAttr("hivenet_instance.web", "name", "web-1"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "status", "RUNNING"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "power_state", "running"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "preset_id", fakePresetID),
					resource.TestCheckResourceAttr("hivenet_instance.web", "image_id", "ubuntu24.04"),
				),
			},
			{
				// No update endpoint: a new image is a new instance.
				Config: fakeProviderConfig(endpoint) + createConfig("web-1", "debian12", "running"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hivenet_instance.web", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("hivenet_instance.web", "id", second),
			},
			{
				ResourceName:            "hivenet_instance.web",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "preset_id", "image_type", "image_id"},
			},
		},
	})

	want := []string{"create web-1 ubuntu24.04", "terminate " + first, "create web-1 debian12", "terminate " + second}
	if got := f.actionLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("API actions = %v, want %v", got, want)
	}
}

func TestInstanceResource_createStopped(t *testing.T) {
	fastPolling(t)
	f, endpoint := newFakeInstanceAPI(t, nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + createConfig("web-1", "ubuntu24.04", "stopped"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hivenet_instance.web", "power_state", "stopped"),
					resource.TestCheckResourceAttr("hivenet_instance.web", "status", "STOPPED"),
				),
			},
		},
	})

	id := "aaaaaaaa-0000-0000-0000-000000000001"
	want := []string{"create web-1 ubuntu24.04", "stop " + id, "terminate " + id}
	if got := f.actionLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("API actions = %v, want %v", got, want)
	}
}

func TestInstanceResource_createOutcomeUnknown(t *testing.T) {
	fastPolling(t)
	for name, tc := range map[string]struct {
		stillCreates bool
		wantErr      *regexp.Regexp
	}{
		"created anyway is adopted": {stillCreates: true},
		"nothing created":           {wantErr: regexp.MustCompile(`No instance was created; it is safe to apply again`)},
	} {
		t.Run(name, func(t *testing.T) {
			// An instance with the same name that already existed is never adopted.
			f, endpoint := newFakeInstanceAPI(t, map[string]string{fakeID: "RUNNING"})
			f.instances[fakeID].name = "web-1"
			f.failNextCreate(http.StatusInternalServerError, tc.stillCreates)

			step := resource.TestStep{Config: fakeProviderConfig(endpoint) + createConfig("web-1", "ubuntu24.04", "running")}
			if tc.wantErr != nil {
				step.ExpectError = tc.wantErr
			} else {
				step.Check = resource.TestCheckResourceAttr("hivenet_instance.web", "id", "aaaaaaaa-0000-0000-0000-000000000001")
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{step},
			})

			creates := 0
			for _, a := range f.actionLog() {
				if strings.HasPrefix(a, "create ") {
					creates++
				}
			}
			if creates != 1 {
				t.Errorf("%d creates, want 1: a create with an unknown outcome must not be retried", creates)
			}
		})
	}
}

func TestInstanceResource_createSoldOut(t *testing.T) {
	f, endpoint := newFakeInstanceAPI(t, nil)
	f.failNextCreate(http.StatusConflict, false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      fakeProviderConfig(endpoint) + createConfig("web-1", "ubuntu24.04", "running"),
				ExpectError: regexp.MustCompile(`(?s)Nothing was created.*sold out`),
			},
		},
	})
}

func TestInstanceResource_importedThenCreateArguments(t *testing.T) {
	fastPolling(t)
	_, endpoint := newFakeInstanceAPI(t, map[string]string{fakeID: "RUNNING"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + importBlock() + instanceConfig("running"),
			},
			{
				// The API cannot tell the import what these were, so setting
				// them must not replace the instance.
				Config: fakeProviderConfig(endpoint) + createConfig("web", "ubuntu24.04", "running"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hivenet_instance.web", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("hivenet_instance.web", "id", fakeID),
			},
			{
				Config:             fakeProviderConfig(endpoint) + createConfig("web-renamed", "ubuntu24.04", "running"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hivenet_instance.web", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// TestAccInstanceResource_create creates a CPU instance, stops it, checks that
// a rename plans a replace, and destroys it. The test org needs a registered
// SSH key: without one the API refuses every create.
func TestAccInstanceResource_create(t *testing.T) {
	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skipf("acceptance test: set %s=1 to run", resource.EnvTfAcc)
	}
	preset := acctest.CPUPreset(t)
	imageType, imageID := acctest.Image()
	name := sdkacctest.RandomWithPrefix(acctest.NamePrefix + "create")
	cfg := func(name, powerState string) string {
		return fmt.Sprintf(`
resource "hivenet_instance" "test" {
  name        = %q
  preset_id   = %q
  image_type  = %q
  image_id    = %q
  power_state = %q
}
`, name, preset, imageType, imageID, powerState)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckInstancesTerminated,
		Steps: []resource.TestStep{
			{
				Config: cfg(name, "running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("hivenet_instance.test", "id"),
					resource.TestCheckResourceAttr("hivenet_instance.test", "name", name),
					resource.TestCheckResourceAttr("hivenet_instance.test", "status", "RUNNING"),
				),
			},
			{
				Config: cfg(name, "stopped"),
				Check:  resource.TestCheckResourceAttr("hivenet_instance.test", "status", "STOPPED"),
			},
			{
				ResourceName:            "hivenet_instance.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "guest_ready", "preset_id", "image_type", "image_id"},
			},
			{
				Config:             cfg(name+"-renamed", "stopped"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hivenet_instance.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// testAccCheckInstancesTerminated checks every hivenet_instance in s is gone.
func testAccCheckInstancesTerminated(s *terraform.State) error {
	c, err := acctest.Client()
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "hivenet_instance" {
			continue
		}
		inst, err := c.GetInstance(context.Background(), rs.Primary.ID)
		if client.IsStatus(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if inst.Status != "TERMINATED" {
			return fmt.Errorf("instance %s is %s after destroy, want TERMINATED", inst.ID, inst.Status)
		}
	}
	return nil
}

// TestAccInstanceResource_fixture imports the test org's fixture instance,
// starts and stops it, and leaves it stopped. It never destroys it: the last
// step drops it from state with a removed block.
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
