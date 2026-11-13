package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &presetsDataSource{}

func NewPresetsDataSource() datasource.DataSource {
	return &presetsDataSource{}
}

type presetsDataSource struct {
	client *client.Client
}

type presetsModel struct {
	Region         types.String  `tfsdk:"region"`
	SKU            types.String  `tfsdk:"sku"`
	FreeTextSearch types.String  `tfsdk:"free_text_search"`
	Presets        []presetModel `tfsdk:"presets"`
}

type presetModel struct {
	ID              types.String  `tfsdk:"id"`
	Region          types.String  `tfsdk:"region"`
	Location        types.String  `tfsdk:"location"`
	GPU             types.String  `tfsdk:"gpu"`
	CPU             types.Float64 `tfsdk:"cpu"`
	MemoryGB        types.Float64 `tfsdk:"memory_gb"`
	StorageGB       types.Float64 `tfsdk:"storage_gb"`
	HourlyPrice     types.Float64 `tfsdk:"hourly_price"`
	PriceDiscounted types.Float64 `tfsdk:"price_discounted"`
}

func (d *presetsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_presets"
}

func (d *presetsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the hardware presets instances can run on, with their hourly price. " +
			"The same hardware may be offered in several regions at different prices.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Only presets offered in this region, a name from `hivenet_regions`. An unknown value returns no presets rather than an error.",
				Optional:            true,
			},
			"sku": schema.StringAttribute{
				MarkdownDescription: "Only presets with this SKU name, e.g. `8x-RTX-4090`.",
				Optional:            true,
			},
			"free_text_search": schema.StringAttribute{
				MarkdownDescription: "Match against SKU name, GPU model and datacenter location, e.g. `RTX 4090`. " +
					"The GPU model is not one of the returned attributes, so this is the way to filter on it.",
				Optional: true,
			},
			"presets": schema.ListNestedAttribute{
				MarkdownDescription: "Matching presets.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Preset ID, used when creating an instance.",
							Computed:            true,
						},
						"region": schema.StringAttribute{
							MarkdownDescription: "Region the preset is offered in. Usually a region code; a datacenter with no region mapping reports its location name instead.",
							Computed:            true,
						},
						"location": schema.StringAttribute{
							MarkdownDescription: "Datacenter within the region.",
							Computed:            true,
						},
						"gpu": schema.StringAttribute{
							MarkdownDescription: "GPU count as the API shows it, e.g. `8×`. The model is not included.",
							Computed:            true,
						},
						"cpu": schema.Float64Attribute{
							MarkdownDescription: "Virtual CPUs.",
							Computed:            true,
						},
						"memory_gb": schema.Float64Attribute{
							MarkdownDescription: "Memory, in GB.",
							Computed:            true,
						},
						"storage_gb": schema.Float64Attribute{
							MarkdownDescription: "Storage, in GB.",
							Computed:            true,
						},
						"hourly_price": schema.Float64Attribute{
							MarkdownDescription: "List price per hour, in credits.",
							Computed:            true,
						},
						"price_discounted": schema.Float64Attribute{
							MarkdownDescription: "Discounted price per hour, in credits. Null when no discount is active.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *presetsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *presetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireClient(d.client, &resp.Diagnostics) {
		return
	}
	var state presetsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	presets, err := d.client.ListPresets(ctx, client.ListPresetsOptions{
		Region:         state.Region.ValueString(),
		SKU:            state.SKU.ValueString(),
		FreeTextSearch: state.FreeTextSearch.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Listing presets", err.Error())
		return
	}

	state.Presets = make([]presetModel, 0, len(presets))
	for _, p := range presets {
		state.Presets = append(state.Presets, presetModel{
			ID:              types.StringValue(p.ID),
			Region:          types.StringValue(p.Region),
			Location:        types.StringValue(p.Location),
			GPU:             types.StringValue(p.GPU),
			CPU:             parseNumber(p.ID, "cpu", p.CPU, &resp.Diagnostics),
			MemoryGB:        parseNumber(p.ID, "memory", p.Memory, &resp.Diagnostics),
			StorageGB:       parseNumber(p.ID, "storage", p.Storage, &resp.Diagnostics),
			HourlyPrice:     parseNumber(p.ID, "hourly_price", p.HourlyPrice, &resp.Diagnostics),
			PriceDiscounted: parseNumber(p.ID, "price_discounted", p.PriceDiscounted, &resp.Diagnostics),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// parseNumber turns the API's numeric strings into numbers. Empty is null
// (price_discounted uses it for "no discount"); anything else unparseable is
// null plus a warning, so one odd preset doesn't fail the whole read.
func parseNumber(presetID, field, s string, diags *diag.Diagnostics) types.Float64 {
	s = stripUnits(s)
	if s == "" {
		return types.Float64Null()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		diags.AddWarning("Unexpected preset value",
			fmt.Sprintf("Preset %s has %s = %q, which is not a number; it is left null.", presetID, field, s))
		return types.Float64Null()
	}
	return types.Float64Value(f)
}

// The API currently sends display strings ("€1.50/h", "8 vCPU", "32 GB")
// instead of the documented plain numbers; accept both.
func stripUnits(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "€")
	for _, unit := range []string{"/h", " vCPU", " GB", "×"} {
		s = strings.TrimSuffix(s, unit)
	}
	return strings.TrimSpace(s)
}
