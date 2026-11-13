package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Instances as the API really sends them: enums as numbers, timestamps
// as protobuf objects, and a zero termination_reason on a live instance.
const (
	fakeRunningInstance = `{"instance_id":"11111111-1111-1111-1111-111111111111","name":"web","status":3,
		"region":"france-2","gpu_type":"RTX 4090","size":"1 x GPU","cpu_count":8,"resources":"1 x RTX 4090",
		"bandwidth":1000,"storage_used_gb":100,"hourly_rate":0.5,"total_cost_incurred":12.25,"total_runtime_hours":24.5,
		"started_at":{"seconds":"1793908800","nanos":0},"termination_reason":0,"guest_ready":1,"guest_ready_reason":""}`
	fakeTerminatedInstance = `{"instance_id":"22222222-2222-2222-2222-222222222222","name":"old","status":6,
		"started_at":{"seconds":"1793908800","nanos":0},"terminated_at":{"seconds":"1793995200","nanos":0},
		"termination_reason":2,"guest_ready":0,"guest_ready_reason":"instance is terminated"}`
)

// fakeAPI serves the /v1 routes the data sources use and records User-Agents.
func fakeAPI(t *testing.T) (endpoint string, userAgents *[]string) {
	t.Helper()
	var agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.UserAgent())
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"Invalid API key.","request_id":"r"}}`))
			return
		}
		switch {
		case r.URL.Path == "/v1/regions":
			_, _ = w.Write([]byte(`[{"name":"UAE"},{"name":"FR"},{"name":"US"}]`))
		case r.URL.Path == "/v1/presets" && r.URL.Query().Get("region") == "FR":
			_, _ = w.Write([]byte(`{"data":[{"id":"p-fr","region":"FR","location":"france-2","gpu":"8×","cpu":"32",
				"memory":"192","storage":"500","hourly_price":"3.2000","price_discounted":""},
				{"id":"p-live","region":"FR","location":"france-2","gpu":"1×","cpu":"8 vCPU","memory":"32 GB",
				"storage":"100 GB","hourly_price":"€1.50/h","price_discounted":"€1.20/h"}],"pagination":{"next":null,"size":200}}`))
		case r.URL.Path == "/v1/presets":
			_, _ = w.Write([]byte(`{"data":[],"pagination":{"next":null,"size":200}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/instances/1111"):
			_, _ = w.Write([]byte(fakeRunningInstance))
		case strings.HasPrefix(r.URL.Path, "/v1/instances/2222"):
			_, _ = w.Write([]byte(fakeTerminatedInstance))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"Not found.","request_id":"r"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", &agents
}

func fakeProviderConfig(endpoint string) string {
	return `
provider "hivenet" {
  endpoint = "` + endpoint + `"
  token    = "test-token"
}
`
}

func TestDataSources_fakeAPI(t *testing.T) {
	endpoint, agents := fakeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + `
data "hivenet_regions" "all" {}

data "hivenet_presets" "fr" {
  region = "FR"
}

data "hivenet_instance" "running" {
  id = "11111111-1111-1111-1111-111111111111"
}

data "hivenet_instance" "terminated" {
  id = "22222222-2222-2222-2222-222222222222"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hivenet_regions.all", "names.#", "3"),
					resource.TestCheckResourceAttr("data.hivenet_regions.all", "names.1", "FR"),

					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.#", "2"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.0.id", "p-fr"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.0.gpu", "8×"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.0.cpu", "32"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.0.memory_gb", "192"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.0.hourly_price", "3.2"),
					resource.TestCheckNoResourceAttr("data.hivenet_presets.fr", "presets.0.price_discounted"),
					// The shape the API sends today.
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.1.cpu", "8"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.1.memory_gb", "32"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.1.storage_gb", "100"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.1.hourly_price", "1.5"),
					resource.TestCheckResourceAttr("data.hivenet_presets.fr", "presets.1.price_discounted", "1.2"),

					resource.TestCheckResourceAttr("data.hivenet_instance.running", "name", "web"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "status", "RUNNING"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "location", "france-2"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "cpu_count", "8"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "hourly_rate", "0.5"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "started_at", "2026-11-05T20:00:00Z"),
					resource.TestCheckNoResourceAttr("data.hivenet_instance.running", "terminated_at"),
					resource.TestCheckNoResourceAttr("data.hivenet_instance.running", "termination_reason"),
					resource.TestCheckResourceAttr("data.hivenet_instance.running", "guest_ready", "READY"),
					resource.TestCheckNoResourceAttr("data.hivenet_instance.running", "guest_ready_reason"),

					resource.TestCheckResourceAttr("data.hivenet_instance.terminated", "status", "TERMINATED"),
					resource.TestCheckResourceAttr("data.hivenet_instance.terminated", "terminated_at", "2026-11-06T20:00:00Z"),
					resource.TestCheckResourceAttr("data.hivenet_instance.terminated", "termination_reason", "CREDIT_EXHAUSTION"),
					resource.TestCheckResourceAttr("data.hivenet_instance.terminated", "guest_ready", "UNKNOWN"),
					resource.TestCheckResourceAttr("data.hivenet_instance.terminated", "guest_ready_reason", "instance is terminated"),
				),
			},
		},
	})

	for _, ua := range *agents {
		if ua != "terraform-provider-hivenet/test" {
			t.Errorf("User-Agent = %q, want terraform-provider-hivenet/test", ua)
		}
	}
}

func TestDataSources_fakeAPIErrors(t *testing.T) {
	endpoint, _ := fakeAPI(t)
	t.Setenv(envToken, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(endpoint) + `
data "hivenet_instance" "missing" {
  id = "33333333-3333-3333-3333-333333333333"
}
`,
				ExpectError: regexp.MustCompile(`Instance not found`),
			},
			{
				Config: `
provider "hivenet" {
  endpoint = "` + endpoint + `"
}

data "hivenet_regions" "all" {}
`,
				ExpectError: regexp.MustCompile(`Missing API key`),
			},
			{
				Config: `
provider "hivenet" {
  endpoint = "` + endpoint + `"
  token    = "wrong"
}

data "hivenet_regions" "all" {}
`,
				ExpectError: regexp.MustCompile(`UNAUTHORIZED`),
			},
		},
	})
}

func TestAccDataSources_staging(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "hivenet_regions" "all" {}

data "hivenet_presets" "all" {}

data "hivenet_presets" "first_region" {
  region = data.hivenet_regions.all.names[0]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("data.hivenet_regions.all", "names.#", atLeastOne),
					resource.TestCheckResourceAttrWith("data.hivenet_presets.all", "presets.#", atLeastOne),
					resource.TestCheckResourceAttrSet("data.hivenet_presets.all", "presets.0.id"),
					resource.TestCheckResourceAttrSet("data.hivenet_presets.all", "presets.0.hourly_price"),
					resource.TestCheckResourceAttrSet("data.hivenet_presets.first_region", "presets.#"),
				),
			},
			{
				// No instances exist in the test org until hivenet_instance can
				// create them, so this checks routing and auth via the 404 path.
				Config: `
data "hivenet_instance" "missing" {
  id = "00000000-0000-4000-8000-000000000000"
}
`,
				ExpectError: regexp.MustCompile(`Instance not found`),
			},
		},
	})
}

func atLeastOne(v string) error {
	if v == "" || v == "0" {
		return errEmptyList
	}
	return nil
}

var errEmptyList = errors.New("expected at least one element")
