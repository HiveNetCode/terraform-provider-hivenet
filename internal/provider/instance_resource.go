package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	powerStateRunning = "running"
	powerStateStopped = "stopped"

	statusRunning     client.Status = "RUNNING"
	statusStopped     client.Status = "STOPPED"
	statusErrored     client.Status = "ERRORED"
	statusPaused      client.Status = "PAUSED"
	statusTerminating client.Status = "TERMINATING"
	statusTerminated  client.Status = "TERMINATED"

	// A start can take 15 minutes, plus up to 5 to attach storage.
	defaultPowerTimeout     = 20 * time.Minute
	defaultTerminateTimeout = 20 * time.Minute
)

// pollInterval is how often waits re-read the instance. Unit tests shorten it.
var pollInterval = 10 * time.Second

var (
	_ resource.ResourceWithConfigure   = &instanceResource{}
	_ resource.ResourceWithImportState = &instanceResource{}
)

func NewInstanceResource() resource.Resource {
	return &instanceResource{}
}

type instanceResource struct {
	client *client.Client
}

// instanceResourceModel leaves out the data source's cost and runtime fields:
// they change on every refresh and would make every plan noisy.
type instanceResourceModel struct {
	ID            types.String   `tfsdk:"id"`
	PowerState    types.String   `tfsdk:"power_state"`
	Name          types.String   `tfsdk:"name"`
	Status        types.String   `tfsdk:"status"`
	Location      types.String   `tfsdk:"location"`
	GPUType       types.String   `tfsdk:"gpu_type"`
	Size          types.String   `tfsdk:"size"`
	CPUCount      types.Int64    `tfsdk:"cpu_count"`
	Resources     types.String   `tfsdk:"resources"`
	BandwidthMbps types.Float64  `tfsdk:"bandwidth_mbps"`
	StorageGB     types.Float64  `tfsdk:"storage_gb"`
	HourlyRate    types.Float64  `tfsdk:"hourly_rate"`
	StartedAt     types.String   `tfsdk:"started_at"`
	GuestReady    types.String   `tfsdk:"guest_ready"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (r *instanceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	stable := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			MarkdownDescription: desc,
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	stableFloat := func(desc string) schema.Float64Attribute {
		return schema.Float64Attribute{
			MarkdownDescription: desc,
			Computed:            true,
			PlanModifiers:       []planmodifier.Float64{float64planmodifier.UseStateForUnknown()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the power state of an existing instance.\n\n" +
			"The public API cannot create instances yet, so this resource only works on instances you " +
			"[import](#import). Applying a configuration for an instance that is not imported fails.\n\n" +
			"~> **Destroying this resource terminates the instance and permanently deletes its disk.** " +
			"Use `power_state = \"stopped\"` to pause billing instead, and consider `lifecycle { prevent_destroy = true }`.\n\n" +
			"If Hive terminates the instance on its own, for example after it has been stopped for 10 days, " +
			"the next refresh removes it from state with a warning.",
		Attributes: map[string]schema.Attribute{
			"id": stable("Instance ID (UUID)."),
			"power_state": schema.StringAttribute{
				MarkdownDescription: "`running` or `stopped`. Changing it starts or stops the instance and waits for it to settle. " +
					"Leave it unset to only track the instance. An instance Hive stopped on its own, " +
					"for example when credits ran out, shows up as a change back to `running`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf(powerStateRunning, powerStateStopped)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": stable("Display name."),
			"status": schema.StringAttribute{
				MarkdownDescription: "Lifecycle state, e.g. `RUNNING` or `STOPPED`. `PAUSED` means the host is unreachable; " +
					"`ERRORED` means the instance failed to start or crashed. Both count as `running` for `power_state`.",
				Computed: true,
			},
			"location": stable("Datacenter the instance runs in, e.g. `france-2`."),
			"gpu_type": stable("GPU model, e.g. `RTX 4090`."),
			"size":     stable("Headline shape, e.g. `8 x GPU`."),
			"cpu_count": schema.Int64Attribute{
				MarkdownDescription: "Virtual CPUs allocated.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"resources":      stable("Human-readable summary of the allocated hardware."),
			"bandwidth_mbps": stableFloat("Allocated bandwidth, in Mbps."),
			"storage_gb":     stableFloat("Disk provisioned for the instance, in GB."),
			"hourly_rate":    stableFloat("Price per hour while running, in credits."),
			"started_at": schema.StringAttribute{
				MarkdownDescription: "When the instance last entered `RUNNING` (RFC 3339). Null if it never has.",
				Computed:            true,
			},
			"guest_ready": schema.StringAttribute{
				MarkdownDescription: "Whether the guest OS is reachable: `READY`, `DEGRADED` or `UNKNOWN`.",
				Computed:            true,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Update:            true,
				Delete:            true,
				UpdateDescription: "How long to wait for a start or stop. Defaults to `20m`.",
				DeleteDescription: "How long to wait for termination. Defaults to `20m`.",
			}),
		},
	}
}

func (r *instanceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Creating instances is not supported yet",
		"The public API has no create endpoint yet. Import an existing instance instead, with an import block "+
			"or `terraform import hivenet_instance.<name> <instance id>`.")
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireClient(r.client, &resp.Diagnostics) {
		return
	}
	var state instanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	inst, err := r.client.GetInstance(ctx, state.ID.ValueString())
	if client.IsStatus(err, http.StatusNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading instance", err.Error())
		return
	}

	switch inst.Status {
	case statusTerminating, statusTerminated:
		detail := fmt.Sprintf("Instance %s is %s, so it was removed from state. Its disk is deleted with it.", inst.ID, inst.Status)
		if inst.TerminationReason != nil && *inst.TerminationReason != "OTHER_UNKNOWN" {
			detail += fmt.Sprintf(" Reason: %s.", *inst.TerminationReason)
		}
		resp.Diagnostics.AddWarning("Instance terminated outside Terraform", detail)
		resp.State.RemoveResource(ctx)
		return
	case statusPaused:
		resp.Diagnostics.AddWarning("Instance is paused",
			fmt.Sprintf("The host of instance %s is unreachable. Hive resumes the instance when the host recovers.", inst.ID))
	case statusErrored:
		resp.Diagnostics.AddWarning("Instance is in an error state",
			strings.TrimSpace(fmt.Sprintf("Instance %s is ERRORED. %s", inst.ID, inst.GuestReadyReason)))
	}

	setFromInstance(&state, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireClient(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state instanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, defaultPowerTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	want := plan.PowerState.ValueString()
	var inst *client.Instance
	var err error
	if want != state.PowerState.ValueString() {
		inst, err = r.setPowerState(ctx, id, want, timeout)
	} else {
		// Only timeouts changed.
		inst, err = r.client.GetInstance(ctx, id)
	}
	if err != nil {
		resp.Diagnostics.AddError("Changing instance power state", err.Error())
		return
	}

	// Attributes the plan kept from state must not change here, or Terraform
	// rejects the apply; the next refresh picks up any drift in them.
	d := instanceToModel(inst)
	plan.Status, plan.StartedAt, plan.GuestReady = d.Status, d.StartedAt, d.GuestReady
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireClient(r.client, &resp.Diagnostics) {
		return
	}
	var state instanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, defaultTerminateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := r.client.TerminateInstance(ctx, id)
	// 409 means it is already TERMINATED.
	if client.IsStatus(err, http.StatusNotFound) || client.IsStatus(err, http.StatusConflict) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Terminating instance", err.Error())
		return
	}
	if _, err := waitForStatus(ctx, r.client, id, statusTerminated, timeout); err != nil {
		resp.Diagnostics.AddError("Terminating instance", err.Error())
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *instanceResource) setPowerState(ctx context.Context, id, want string, timeout time.Duration) (*client.Instance, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	target := statusRunning
	if want == powerStateRunning {
		err = r.client.StartInstance(ctx, id)
	} else {
		target = statusStopped
		err = r.client.StopInstance(ctx, id)
	}
	if client.IsStatus(err, http.StatusConflict) {
		return nil, fmt.Errorf("instance %s is terminated and cannot be set to %s: %w", id, want, err)
	}
	if err != nil {
		return nil, err
	}
	return waitForStatus(ctx, r.client, id, target, timeout)
}

// waitForStatus polls until the instance reaches target or ctx ends; callers
// put the whole operation's deadline on ctx and pass its length as timeout
// for the error message. Reaching TERMINATING or TERMINATED on the way to
// another target is an error. When target is TERMINATED, a 404 also counts as
// done and the returned instance is nil.
func waitForStatus(ctx context.Context, c *client.Client, id string, target client.Status, timeout time.Duration) (*client.Instance, error) {
	var last *client.Instance
	for {
		inst, err := c.GetInstance(ctx, id)
		switch {
		case err == nil:
			last = inst
			if inst.Status == target {
				return inst, nil
			}
			if target != statusTerminated && (inst.Status == statusTerminating || inst.Status == statusTerminated) {
				return nil, fmt.Errorf("instance %s became %s while waiting for %s", id, inst.Status, target)
			}
		case target == statusTerminated && client.IsStatus(err, http.StatusNotFound):
			return nil, nil
		case ctx.Err() == nil:
			return nil, err
		}

		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("timed out after %s waiting for instance %s to be %s", timeout, id, target)
			if last != nil {
				msg += fmt.Sprintf("; last status %s", last.Status)
				if last.GuestReadyReason != "" {
					msg += " (" + last.GuestReadyReason + ")"
				}
			}
			return nil, errors.New(msg)
		case <-time.After(pollInterval):
		}
	}
}

// powerStateOf maps a status to the power state it implies: pending, ERRORED
// and PAUSED count as running. ok is false for statuses this provider does
// not know.
func powerStateOf(s client.Status) (state string, ok bool) {
	switch s {
	case "CREATED", "DEPLOYED", "STARTING", statusRunning, statusErrored, statusPaused:
		return powerStateRunning, true
	case "STOPPING", statusStopped:
		return powerStateStopped, true
	}
	return "", false
}

// setFromInstance copies inst into m. An unknown status keeps m's power state.
func setFromInstance(m *instanceResourceModel, inst *client.Instance) {
	d := instanceToModel(inst)
	m.ID = d.ID
	m.Name = d.Name
	m.Status = d.Status
	m.Location = d.Location
	m.GPUType = d.GPUType
	m.Size = d.Size
	m.CPUCount = d.CPUCount
	m.Resources = d.Resources
	m.BandwidthMbps = d.BandwidthMbps
	m.StorageGB = d.StorageGB
	m.HourlyRate = d.HourlyRate
	m.StartedAt = d.StartedAt
	m.GuestReady = d.GuestReady

	if ps, ok := powerStateOf(inst.Status); ok {
		m.PowerState = types.StringValue(ps)
	} else if m.PowerState.IsNull() || m.PowerState.IsUnknown() {
		m.PowerState = types.StringValue(powerStateRunning)
	}
}
