# Terraform Provider for Hivenet Compute

`terraform-provider-hivenet` lets you manage Hivenet Compute resources with Terraform or OpenTofu. It wraps the public API (`api.hivecompute.ai/v1`) so you can create, update, import and destroy containers and VMs, register SSH keys, and look up regions, presets and images from your configuration.

Status: in development, not yet published to the registries.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0 or [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.6
- [Go](https://golang.org/doc/install) >= 1.24 (to build)

## Developing the Provider

Build and install the binary into `$GOPATH/bin`:

```shell
make install
```

To use the local build, point Terraform at it in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "hivenetcode/hivenet" = "/Users/<you>/go/bin"
  }
  direct {}
}
```

Then `terraform plan` uses your build (skip `terraform init`) in any directory whose configuration requires `source = "hivenetcode/hivenet"`, as in `examples/provider/provider.tf`. Set `HIVENET_TOKEN`, and `HIVENET_ENDPOINT=https://staging-api.hivecompute.ai/v1` to try it against staging.

Other targets:

- `make generate` regenerates `docs/` from the schema and `examples/`
- `make lint` runs golangci-lint
- `make test` runs unit tests
- `make testacc` runs acceptance tests. Once resources exist these create real, billable instances; run them only against staging
- `make sweep` terminates leaked `tf-acc-*` instances

## Acceptance tests

Acceptance tests call the staging API with a dedicated test org that has a fixed credit balance. They refuse to run against production.

| Variable | Value |
|---|---|
| `HIVENET_ENDPOINT` | `https://staging-api.hivecompute.ai/v1` |
| `HIVENET_TOKEN` | The test org's API key (or your own staging key when running locally) |
| `HIVENET_ACC_CPU_PRESET` | Default preset for tests: the cheapest CPU preset |
| `HIVENET_ACC_GPU_PRESET` | Preset for tests that need a GPU |
| `HIVENET_ACC_GPU` | `1` to run GPU tests; they skip otherwise |
| `HIVENET_ACC_INSTANCE_ID` | ID of the fixture instance below; its test skips when unset |

Until the API can create instances, `hivenet_instance` is tested on one long-lived fixture: a CPU instance in the test org, created once in the console. The test imports it, starts and stops it, and leaves it stopped; it never destroys it. Do not name it with the `tf-acc-` prefix, or the sweeper terminates it. Hive terminates instances that stay stopped for 10 days, so if the nightly run is broken for that long, create a new fixture and update the `HIVENET_ACC_INSTANCE_ID` repository variable.

Name every test resource with the `tf-acc-` prefix (`acctest.NamePrefix`). The sweeper terminates anything carrying it.

In CI, `.github/workflows/acceptance.yml` runs on every push to `main`, nightly at 03:00 UTC, and on demand (tick **gpu** to include GPU tests). It reads the key from Vault over GitHub OIDC, using the `VAULT_ADDR` and `VAULT_SECRET_PATH` repository secrets, so it does not run on pull requests; run them locally from a branch. After the tests it sweeps leaked resources and writes the month's spend to the job summary.

## Releasing

Push a `v*` tag. The release workflow builds signed binaries for linux, darwin and windows (amd64, arm64) with GoReleaser. It needs the `GPG_PRIVATE_KEY` and `PASSPHRASE` repository secrets.
