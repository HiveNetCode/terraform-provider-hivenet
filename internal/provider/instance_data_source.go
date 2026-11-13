package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &instanceDataSource{}

func NewInstanceDataSource() datasource.DataSource {
	return &instanceDataSource{}
}

type instanceDataSource struct {
	client *client.Client
}

type instanceModel struct {
	ID                types.String  `tfsdk:"id"`
	Name              types.String  `tfsdk:"name"`
	Status            types.String  `tfsdk:"status"`
	Location          types.String  `tfsdk:"location"`
	GPUType           types.String  `tfsdk:"gpu_type"`
	Size              types.String  `tfsdk:"size"`
	CPUCount          types.Int64   `tfsdk:"cpu_count"`
	Resources         types.String  `tfsdk:"resources"`
	BandwidthMbps     types.Float64 `tfsdk:"bandwidth_mbps"`
	StorageGB         types.Float64 `tfsdk:"storage_gb"`
	HourlyRate        types.Float64 `tfsdk:"hourly_rate"`
	TotalCostIncurred types.Float64 `tfsdk:"total_cost_incurred"`
	TotalRuntimeHours types.Float64 `tfsdk:"total_runtime_hours"`
	StartedAt         types.String  `tfsdk:"started_at"`
	TerminatedAt      types.String  `tfsdk:"terminated_at"`
	TerminationReason types.String  `tfsdk:"termination_reason"`
	GuestReady        types.String  `tfsdk:"guest_ready"`
	GuestReadyReason  types.String  `tfsdk:"guest_ready_reason"`
}

func (d *instanceDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (d *instanceDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	computedFloat := func(desc string) schema.Float64Attribute {
		return schema.Float64Attribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one of your instances by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Instance ID (UUID).",
				Required:            true,
			},
			"name":   computedString("Display name."),
			"status": computedString("Lifecycle state, e.g. `RUNNING`, `STOPPED` or `TERMINATED`."),
			"location": computedString("Datacenter the instance runs in, e.g. `france-2`. " +
				"This is narrower than a region and cannot be used as the `region` filter of `hivenet_presets`."),
			"gpu_type": computedString("GPU model, e.g. `RTX 4090`."),
			"size":     computedString("Headline shape, e.g. `8 x GPU`."),
			"cpu_count": schema.Int64Attribute{
				MarkdownDescription: "Virtual CPUs allocated.",
				Computed:            true,
			},
			"resources":           computedString("Human-readable summary of the allocated hardware."),
			"bandwidth_mbps":      computedFloat("Allocated bandwidth, in Mbps."),
			"storage_gb":          computedFloat("Disk provisioned for the instance, in GB."),
			"hourly_rate":         computedFloat("Price per hour while running, in credits."),
			"total_cost_incurred": computedFloat("Total cost over the instance's life, in credits."),
			"total_runtime_hours": computedFloat("Total hours the instance has spent running."),
			"started_at":          computedString("When the instance last entered `RUNNING` (RFC 3339). Null if it never has."),
			"terminated_at":       computedString("When the instance was terminated (RFC 3339). Null if it has not been."),
			"termination_reason":  computedString("Why the instance was terminated, e.g. `USER_INITIATED` or `CREDIT_EXHAUSTION`. Null if it has not been."),
			"guest_ready":         computedString("Whether the guest OS is reachable: `READY`, `DEGRADED` or `UNKNOWN`."),
			"guest_ready_reason":  computedString("Explanation when the guest is not ready. Null otherwise."),
		},
	}
}

func (d *instanceDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *instanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireClient(d.client, &resp.Diagnostics) {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	inst, err := d.client.GetInstance(ctx, id.ValueString())
	if client.IsStatus(err, http.StatusNotFound) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Instance not found",
			"No instance "+id.ValueString()+" on this account.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading instance", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, instanceToModel(inst))...)
}

func instanceToModel(inst *client.Instance) instanceModel {
	m := instanceModel{
		ID:                types.StringValue(inst.ID),
		Name:              types.StringValue(inst.Name),
		Status:            types.StringValue(string(inst.Status)),
		Location:          types.StringValue(inst.Region),
		GPUType:           types.StringValue(inst.GPUType),
		Size:              types.StringValue(inst.Size),
		CPUCount:          types.Int64Value(inst.CPUCount),
		Resources:         types.StringValue(inst.Resources),
		BandwidthMbps:     types.Float64Value(inst.Bandwidth),
		StorageGB:         types.Float64Value(inst.StorageUsedGB),
		HourlyRate:        types.Float64Value(inst.HourlyRate),
		TotalCostIncurred: types.Float64Value(inst.TotalCostIncurred),
		TotalRuntimeHours: types.Float64Value(inst.TotalRuntimeHours),
		StartedAt:         timestampValue(inst.StartedAt),
		TerminatedAt:      timestampValue(inst.TerminatedAt),
		TerminationReason: types.StringNull(),
		GuestReady:        types.StringValue(string(inst.GuestReady)),
		GuestReadyReason:  types.StringNull(),
	}
	if inst.GuestReady == "" {
		m.GuestReady = types.StringValue("UNKNOWN")
	}
	// The protobuf zero value reads OTHER_UNKNOWN on live instances too.
	if !m.TerminatedAt.IsNull() && inst.TerminationReason != nil {
		m.TerminationReason = types.StringValue(string(*inst.TerminationReason))
	}
	if inst.GuestReadyReason != "" {
		m.GuestReadyReason = types.StringValue(inst.GuestReadyReason)
	}
	return m
}

// timestampValue is RFC 3339 in UTC, or null for a missing or zero time.
func timestampValue(t *client.Timestamp) types.String {
	if t == nil || t.IsZero() || t.Unix() == 0 {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// requireClient reports an error when the provider could not be configured,
// e.g. because token depends on a value only known after apply.
func requireClient(c *client.Client, diags *diag.Diagnostics) bool {
	if c != nil {
		return true
	}
	diags.AddError("Provider not configured",
		"The hivenet provider has no API client yet. If token or endpoint come from another resource, apply that first.")
	return false
}
