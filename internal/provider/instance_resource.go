package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	// A create adds scheduling and the image pull to a start.
	defaultCreateTimeout = 30 * time.Minute
)

var (
	instanceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	uuidPattern         = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// pollInterval is how often waits re-read the instance. Unit tests shorten it.
var pollInterval = 10 * time.Second

var (
	_ resource.ResourceWithConfigure   = &instanceResource{}
	_ resource.ResourceWithImportState = &instanceResource{}
	_ resource.ResourceWithModifyPlan  = &instanceResource{}
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
	PresetID      types.String   `tfsdk:"preset_id"`
	ImageType     types.String   `tfsdk:"image_type"`
	ImageID       types.String   `tfsdk:"image_id"`
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
		MarkdownDescription: "Creates an instance, or manages one you [import](#import), and sets its power state.\n\n" +
			"~> **Every change except `power_state` replaces the instance**: Terraform terminates it, deleting its disk, " +
			"and creates a new one with a new ID. The API cannot update an instance in place yet. " +
			"Consider `lifecycle { prevent_destroy = true }`, and use `power_state = \"stopped\"` to pause billing.\n\n" +
			"The instance gets the SSH key registered last on your account, in the console or through the API. " +
			"Without one, create fails. Connection details are not in the API yet: find them in the Hive Compute console.\n\n" +
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
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name: letters, digits, hyphens and underscores. Required to create. " +
					"Names are not unique. Changing it replaces the instance.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 255),
					stringvalidator.RegexMatches(instanceNamePattern, "must contain only letters, digits, hyphens and underscores"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"preset_id": createOnly("ID of the preset to create the instance from, from the `hivenet_presets` data source. "+
				"It sets the location, hardware and price. Required to create.",
				stringvalidator.RegexMatches(uuidPattern, "must be a preset ID (a UUID) from the hivenet_presets data source")),
			"image_type": schema.StringAttribute{
				MarkdownDescription: "`vm` or `container`. Required to create. " + createOnlyNote,
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{stringvalidator.OneOf("vm", "container")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), replaceOnChange()},
			},
			"image_id": createOnly("ID of the image, e.g. `ubuntu24.04`. It must suit the preset: a GPU preset needs a GPU image. " +
				"Windows images cannot be created through the API yet. Required to create."),
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
				Create:            true,
				Update:            true,
				Delete:            true,
				CreateDescription: "How long to wait for a new instance to reach `RUNNING`. Defaults to `30m`.",
				UpdateDescription: "How long to wait for a start or stop. Defaults to `20m`.",
				DeleteDescription: "How long to wait for termination. Defaults to `20m`.",
			}),
		},
	}
}

func (r *instanceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan requires the create arguments when the plan creates the instance.
// They are optional otherwise, so an imported instance needs none of them.
func (r *instanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var config instanceResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for attr, v := range map[string]types.String{
		"name": config.Name, "preset_id": config.PresetID, "image_type": config.ImageType, "image_id": config.ImageID,
	} {
		if v.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Missing argument to create an instance",
				fmt.Sprintf("%q is required to create an instance. To manage an existing instance instead, import it.", attr))
		}
	}
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireClient(r.client, &resp.Diagnostics) {
		return
	}
	var plan instanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := client.CreateInstanceRequest{
		Name:      plan.Name.ValueString(),
		PresetID:  plan.PresetID.ValueString(),
		ImageType: plan.ImageType.ValueString(),
		ImageID:   plan.ImageID.ValueString(),
	}
	stopAfter := plan.PowerState.ValueString() == powerStateStopped
	inst, err := r.createInstance(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Creating instance", err.Error())
		return
	}

	// Save the ID before waiting, so a failed wait leaves the instance in
	// state, tainted, instead of billing untracked.
	if got, err := r.client.GetInstance(ctx, inst.ID); err == nil {
		inst = got
	}
	setFromInstance(&plan, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	inst, err = waitForStatus(ctx, r.client, inst.ID, statusRunning, timeout)
	if err == nil && stopAfter {
		inst, err = r.setPowerState(ctx, plan.ID.ValueString(), powerStateStopped, timeout)
	}
	if err != nil {
		resp.Diagnostics.AddError("Waiting for the new instance", err.Error())
		return
	}
	setFromInstance(&plan, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// createInstance creates the instance. When the API cannot say whether it did,
// it never adopts or retries: names are not unique, so it lists the new
// instances with the same name for the user to check.
func (r *instanceResource) createInstance(ctx context.Context, body client.CreateInstanceRequest) (*client.Instance, error) {
	existing, err := r.client.ListInstances(ctx, client.ListInstancesOptions{FreeTextSearch: body.Name})
	if err != nil {
		return nil, fmt.Errorf("listing instances before the create: %w", err)
	}
	before := map[string]bool{}
	for _, inst := range existing {
		before[inst.ID] = true
	}

	inst, err := r.client.CreateInstance(ctx, body)
	switch {
	case err == nil:
		return inst, nil
	case client.IsStatus(err, http.StatusNotFound):
		return nil, fmt.Errorf("%w\n\nThe preset or image does not exist, the preset's location does not offer the image, "+
			"or the account has no SSH key registered", err)
	case client.IsStatus(err, http.StatusForbidden):
		return nil, fmt.Errorf("%w\n\nNothing was created: the account cannot use the location of preset %s, "+
			"or cannot create instances now, for example because it is suspended. Try a preset in another location", err, body.PresetID)
	case client.IsStatus(err, http.StatusConflict):
		return nil, fmt.Errorf("%w\n\nNothing was created: the account lacks credit, the preset is sold out in its location, "+
			"or the preset changed during the call. Retry later or pick another preset", err)
	case !client.IsCreateOutcomeUnknown(err):
		return nil, err
	}

	// Check even if the apply was cancelled: the create may still have happened.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	after, listErr := r.client.ListInstances(checkCtx, client.ListInstancesOptions{FreeTextSearch: body.Name})
	if listErr != nil {
		return nil, fmt.Errorf("%w\n\nThe instance may have been created, and checking failed: %v. "+
			"Look for an instance named %q before applying again, or the next apply may create a second one", err, listErr, body.Name)
	}
	var ids []string
	for _, inst := range after {
		if inst.Name == body.Name && !before[inst.ID] && inst.Status != statusTerminating && inst.Status != statusTerminated {
			ids = append(ids, inst.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w\n\nThe instance may have been created: none named %q has appeared yet, but the create "+
			"can still complete. Check your instances before applying again, or the next apply may create a second one", err, body.Name)
	}
	return nil, fmt.Errorf("%w\n\nThe instance may have been created. New instances named %q: %s. "+
		"If one is this resource's, import it instead of applying again; terminate any you do not want", err, body.Name, strings.Join(ids, ", "))
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
	// An imported instance has no create arguments to keep.
	for _, v := range []*types.String{&plan.PresetID, &plan.ImageType, &plan.ImageID} {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
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

const createOnlyNote = "Not returned by the API, so it is empty after an import, and setting it then does not replace the instance. " +
	"Changing it later replaces the instance; removing it from the configuration keeps the value."

func createOnly(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc + " " + createOnlyNote,
		Optional:            true,
		Computed:            true,
		Validators:          validators,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), replaceOnChange()},
	}
}

// replaceOnChange replaces the instance when a known create argument changes,
// but not when it is first set on an imported instance.
func replaceOnChange() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing it replaces the instance.",
		"Changing it replaces the instance.",
	)
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
