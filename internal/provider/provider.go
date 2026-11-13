package provider

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	defaultEndpoint = "https://api.hivecompute.ai/v1"
	envToken        = "HIVENET_TOKEN"
	envEndpoint     = "HIVENET_ENDPOINT"
)

var _ provider.Provider = &HivenetProvider{}

// HivenetProvider defines the provider implementation.
type HivenetProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

type providerModel struct {
	Token    types.String `tfsdk:"token"`
	Endpoint types.String `tfsdk:"endpoint"`
}

func (p *HivenetProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hivenet"
	resp.Version = p.version
}

func (p *HivenetProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Hivenet provider manages Hivenet Compute resources through the public API.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				MarkdownDescription: "API key for the Hivenet Compute public API. Can also be set with the `" + envToken + "` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Base URL of the public API, including `/v1`. Defaults to `" + defaultEndpoint + "`. Can also be set with the `" + envEndpoint + "` environment variable.",
				Optional:            true,
			},
		},
	}
}

func (p *HivenetProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values come from attributes set by other resources; wait for apply.
	if cfg.Token.IsUnknown() || cfg.Endpoint.IsUnknown() {
		return
	}

	token := firstSet(cfg.Token, envToken, "")
	endpoint := firstSet(cfg.Endpoint, envEndpoint, defaultEndpoint)

	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing API key",
			"Set token in the provider block or the "+envToken+" environment variable.")
	}
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid endpoint",
			fmt.Sprintf("%q is not an http(s) URL, e.g. %s.", endpoint, defaultEndpoint))
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c := client.New(endpoint, token, "terraform-provider-hivenet/"+p.version)
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *HivenetProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewInstanceResource,
	}
}

func (p *HivenetProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewRegionsDataSource,
		NewPresetsDataSource,
		NewInstanceDataSource,
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HivenetProvider{
			version: version,
		}
	}
}

// firstSet returns the configured value, else the environment variable, else def.
func firstSet(v types.String, env, def string) string {
	if !v.IsNull() && v.ValueString() != "" {
		return v.ValueString()
	}
	if e := os.Getenv(env); e != "" {
		return e
	}
	return def
}

// configureClient is the shared DataSource/Resource Configure step.
func configureClient(providerData any, diags *diag.Diagnostics) *client.Client {
	if providerData == nil {
		return nil
	}
	c, ok := providerData.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T. Please report this to the provider developers.", providerData))
		return nil
	}
	return c
}
