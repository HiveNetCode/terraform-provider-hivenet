// Package acctest holds settings shared by the acceptance tests, the sweeper
// and the cost report. Everything here talks to staging only.
package acctest

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
)

// NamePrefix marks resources created by acceptance tests. The sweeper deletes
// anything carrying it, so never use it for real resources.
const NamePrefix = "tf-acc-"

const productionHost = "api.hivecompute.ai"

// Env reads HIVENET_ENDPOINT and HIVENET_TOKEN and refuses production.
func Env() (endpoint, token string, err error) {
	endpoint, token = os.Getenv("HIVENET_ENDPOINT"), os.Getenv("HIVENET_TOKEN")
	if endpoint == "" || token == "" {
		return "", "", errors.New("HIVENET_ENDPOINT and HIVENET_TOKEN must be set to a staging endpoint and key")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("HIVENET_ENDPOINT: %w", err)
	}
	if u.Hostname() == productionHost {
		return "", "", fmt.Errorf("HIVENET_ENDPOINT points at production (%s); acceptance tests run against staging only", productionHost)
	}
	return endpoint, token, nil
}

// CPUPreset returns the default preset for acceptance tests.
func CPUPreset(t testing.TB) string {
	t.Helper()
	return requireEnv(t, "HIVENET_ACC_CPU_PRESET")
}

// GPUPreset skips t unless HIVENET_ACC_GPU=1, then returns the GPU preset.
// CI sets it only on manual runs.
func GPUPreset(t testing.TB) string {
	t.Helper()
	if os.Getenv("HIVENET_ACC_GPU") != "1" {
		t.Skip("GPU test: set HIVENET_ACC_GPU=1 to run")
	}
	return requireEnv(t, "HIVENET_ACC_GPU_PRESET")
}

// FixtureInstanceID skips t unless HIVENET_ACC_INSTANCE_ID is set, then
// returns it. It names a long-lived instance in the test org that tests import
// and start or stop but never destroy, until hivenet_instance can create its own.
func FixtureInstanceID(t testing.TB) string {
	t.Helper()
	id := os.Getenv("HIVENET_ACC_INSTANCE_ID")
	if id == "" {
		t.Skip("fixture test: set HIVENET_ACC_INSTANCE_ID to a staging instance to run")
	}
	return id
}

func requireEnv(t testing.TB, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s must be set for acceptance tests", name)
	}
	return v
}

// Client returns an API client for the acceptance environment.
func Client() (*client.Client, error) {
	endpoint, token, err := Env()
	if err != nil {
		return nil, err
	}
	return client.New(endpoint, token, "terraform-provider-hivenet-acctest"), nil
}
