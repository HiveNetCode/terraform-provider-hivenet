package provider

import (
	"context"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &regionsDataSource{}

func NewRegionsDataSource() datasource.DataSource {
	return &regionsDataSource{}
}

type regionsDataSource struct {
	client *client.Client
}

type regionsModel struct {
	Names types.List `tfsdk:"names"`
}

func (d *regionsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the region codes the API accepts, e.g. `FR`. Use one as the `region` filter of `hivenet_presets`. " +
			"A region being listed does not mean it has capacity; `hivenet_presets` shows what can be launched.",
		Attributes: map[string]schema.Attribute{
			"names": schema.ListAttribute{
				MarkdownDescription: "Region codes.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (d *regionsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *regionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireClient(d.client, &resp.Diagnostics) {
		return
	}
	regions, err := d.client.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Listing regions", err.Error())
		return
	}

	names := make([]string, 0, len(regions))
	for _, r := range regions {
		names = append(names, r.Name)
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, regionsModel{Names: list})...)
}
