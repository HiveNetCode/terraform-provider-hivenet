package acctest

import "testing"

func TestEnvRefusesProduction(t *testing.T) {
	t.Setenv("HIVENET_TOKEN", "x")
	t.Setenv("HIVENET_ENDPOINT", "https://api.hivecompute.ai/v1")
	if _, _, err := Env(); err == nil {
		t.Fatal("expected production endpoint to be refused")
	}

	t.Setenv("HIVENET_ENDPOINT", "https://staging-api.hivecompute.ai/v1")
	if _, _, err := Env(); err != nil {
		t.Fatal(err)
	}
}

func TestGPUPresetSkipsByDefault(t *testing.T) {
	t.Setenv("HIVENET_ACC_GPU", "")
	t.Setenv("HIVENET_ACC_GPU_PRESET", "gpu-1")
	skipped := t.Run("gpu", func(t *testing.T) {
		GPUPreset(t)
		t.Error("expected skip")
	})
	if !skipped {
		t.Fatal("subtest failed instead of skipping")
	}

	t.Setenv("HIVENET_ACC_GPU", "1")
	if got := GPUPreset(t); got != "gpu-1" {
		t.Fatalf("GPUPreset = %q", got)
	}
}

func TestEnvRequiresBoth(t *testing.T) {
	t.Setenv("HIVENET_TOKEN", "")
	t.Setenv("HIVENET_ENDPOINT", "https://staging-api.hivecompute.ai/v1")
	if _, _, err := Env(); err == nil {
		t.Fatal("expected missing token to be refused")
	}
}
