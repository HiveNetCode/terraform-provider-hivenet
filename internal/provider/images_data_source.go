package provider

import (
	"context"
	"regexp"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	imageTypeVM        = "vm"
	imageTypeContainer = "container"
)

var (
	_ datasource.DataSourceWithConfigure      = &imagesDataSource{}
	_ datasource.DataSourceWithValidateConfig = &imagesDataSource{}
)

func NewImagesDataSource() datasource.DataSource {
	return &imagesDataSource{}
}

type imagesDataSource struct {
	client *client.Client
}

type imagesModel struct {
	Type         types.String `tfsdk:"type"`
	Mine         types.Bool   `tfsdk:"mine"`
	NameRegex    types.String `tfsdk:"name_regex"`
	GPUSupported types.Bool   `tfsdk:"gpu_supported"`
	Images       []imageModel `tfsdk:"images"`
}

type imageModel struct {
	Type types.String `tfsdk:"type"`
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`

	OSFamily     types.String `tfsdk:"os_family"`
	GPUSupported types.Bool   `tfsdk:"gpu_supported"`
	LoginUser    types.String `tfsdk:"login_user"`
	MinDiskGB    types.Int64  `tfsdk:"min_disk_gb"`
	MinMemoryGB  types.Int64  `tfsdk:"min_memory_gb"`
	SecureBoot   types.Bool   `tfsdk:"secure_boot"`
	TPM          types.Bool   `tfsdk:"tpm"`

	SoftwareList []types.String `tfsdk:"software_list"`
	Registry     types.String   `tfsdk:"registry"`
	Image        types.String   `tfsdk:"image"`
	WorkloadType types.String   `tfsdk:"workload_type"`
	Command      types.String   `tfsdk:"command"`
	CreatedAt    types.String   `tfsdk:"created_at"`
}

func (d *imagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	vmOnly := func(desc string) string { return desc + " VM images only; null for containers." }
	containerOnly := func(desc string) string { return desc + " Container images only; null for VMs." }
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	computedBool := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{MarkdownDescription: desc, Computed: true}
	}
	computedInt := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the images of one type that the account can launch, for `image_type` and `image_id` on " +
			"`hivenet_instance`. VM images are left out when no location the account can use allows their OS family. " +
			"Windows images are listed but cannot be created through the API yet.",
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				MarkdownDescription: "`vm` or `container`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(imageTypeVM, imageTypeContainer)},
			},
			"mine": schema.BoolAttribute{
				MarkdownDescription: "List the container templates you saved instead of the Hivenet catalogue. " +
					"Only with `type = \"container\"`. A teammate's templates are never listed.",
				Optional: true,
			},
			"name_regex": schema.StringAttribute{
				MarkdownDescription: "Only images whose name matches this regular expression (RE2 syntax), e.g. `^Ubuntu`.",
				Optional:            true,
			},
			"gpu_supported": schema.BoolAttribute{
				MarkdownDescription: "Only VM images that can (`true`) or cannot (`false`) boot on a GPU preset. " +
					"Only with `type = \"vm\"`.",
				Optional: true,
			},
			"images": schema.ListNestedAttribute{
				MarkdownDescription: "Matching images, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type": computedString("`vm` or `container`."),
						"id":   computedString("Image ID, used as `image_id` when creating an instance, e.g. `ubuntu24.04`."),
						"name": computedString("Display name, e.g. `Ubuntu 24.04 LTS`."),

						"os_family":     computedString(vmOnly("`LINUX`, `WINDOWS` or `UNSPECIFIED`. Read any other value as unknown.")),
						"gpu_supported": computedBool(vmOnly("Whether the image can boot on a GPU preset.")),
						"login_user": computedString(vmOnly("The account a Linux image creates for you to log in with, e.g. " +
							"`ubuntu`. Empty when not known, and always for Windows.")),
						"min_disk_gb":   computedInt(vmOnly("Smallest preset storage the image boots with, in GB. `0` means no minimum.")),
						"min_memory_gb": computedInt(vmOnly("Smallest preset memory the image boots with, in GB. `0` means no minimum.")),
						"secure_boot":   computedBool(vmOnly("Whether instances boot with UEFI Secure Boot.")),
						"tpm":           computedBool(vmOnly("Whether instances get a virtual TPM.")),

						"software_list": schema.ListAttribute{
							MarkdownDescription: containerOnly("Software the template ships, e.g. `[\"python\", \"jupyter\"]`."),
							ElementType:         types.StringType,
							Computed:            true,
						},
						"registry":      computedString(containerOnly("Registry the image is pulled from, e.g. `docker.io`.")),
						"image":         computedString(containerOnly("Image reference, e.g. `jupyter/base-notebook:latest`.")),
						"workload_type": computedString(containerOnly("`CPU`, `GPU` or `UNSPECIFIED`. Read any other value as unknown.")),
						"command":       computedString(containerOnly("Command the container runs. Empty means the image's own.")),
						"created_at":    computedString(containerOnly("When the template was saved (RFC 3339). Null when not known.")),
					},
				},
			},
		},
	}
}

func (d *imagesDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var cfg imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateImagesConfig(cfg, &resp.Diagnostics)
}

// validateImagesConfig skips values that are still unknown; Terraform
// validates again once they are known.
func validateImagesConfig(cfg imagesModel, diags *diag.Diagnostics) {
	if isKnown(cfg.NameRegex) {
		if _, err := regexp.Compile(cfg.NameRegex.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("name_regex"), "Invalid name_regex", err.Error())
		}
	}
	if !isKnown(cfg.Type) {
		return
	}
	imageType := cfg.Type.ValueString()
	if imageType != imageTypeContainer && isKnown(cfg.Mine) && cfg.Mine.ValueBool() {
		diags.AddAttributeError(path.Root("mine"), "Invalid mine",
			"`mine` lists saved container templates, so it needs type = \"container\".")
	}
	if imageType != imageTypeVM && isKnown(cfg.GPUSupported) {
		diags.AddAttributeError(path.Root("gpu_supported"), "Invalid gpu_supported",
			"`gpu_supported` filters VM images, so it needs type = \"vm\".")
	}
}

func isKnown(v attr.Value) bool {
	return !v.IsNull() && !v.IsUnknown()
}

func (d *imagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireClient(d.client, &resp.Diagnostics) {
		return
	}
	var state imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var nameRe *regexp.Regexp
	if !state.NameRegex.IsNull() {
		var err error
		if nameRe, err = regexp.Compile(state.NameRegex.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name_regex"), "Invalid name_regex", err.Error())
			return
		}
	}

	images, err := d.client.ListImages(ctx, client.ListImagesOptions{
		Type: state.Type.ValueString(),
		Mine: state.Mine.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Listing images", err.Error())
		return
	}

	state.Images = make([]imageModel, 0, len(images))
	for _, img := range images {
		if nameRe != nil && !nameRe.MatchString(img.Name) {
			continue
		}
		if !state.GPUSupported.IsNull() && img.GPUSupported != state.GPUSupported.ValueBool() {
			continue
		}
		state.Images = append(state.Images, toImageModel(img, state.Type.ValueString()))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// toImageModel leaves the other type's fields null. imageType is the
// requested type, in case the API omits it on an item.
func toImageModel(img client.Image, imageType string) imageModel {
	m := imageModel{
		Type:         types.StringValue(imageType),
		ID:           types.StringValue(img.ID),
		Name:         types.StringValue(img.Name),
		OSFamily:     types.StringNull(),
		GPUSupported: types.BoolNull(),
		LoginUser:    types.StringNull(),
		MinDiskGB:    types.Int64Null(),
		MinMemoryGB:  types.Int64Null(),
		SecureBoot:   types.BoolNull(),
		TPM:          types.BoolNull(),
		Registry:     types.StringNull(),
		Image:        types.StringNull(),
		WorkloadType: types.StringNull(),
		Command:      types.StringNull(),
		CreatedAt:    types.StringNull(),
	}
	if imageType == imageTypeVM {
		m.OSFamily = types.StringValue(img.OSFamily)
		m.GPUSupported = types.BoolValue(img.GPUSupported)
		m.LoginUser = types.StringValue(img.LoginUser)
		m.MinDiskGB = types.Int64Value(img.MinDiskGB)
		m.MinMemoryGB = types.Int64Value(img.MinMemoryGB)
		m.SecureBoot = types.BoolValue(img.SecureBoot)
		m.TPM = types.BoolValue(img.TPM)
		return m
	}
	m.SoftwareList = make([]types.String, 0, len(img.SoftwareList))
	for _, s := range img.SoftwareList {
		m.SoftwareList = append(m.SoftwareList, types.StringValue(s))
	}
	m.Registry = types.StringValue(img.Registry)
	m.Image = types.StringValue(img.Image)
	m.WorkloadType = types.StringValue(img.WorkloadType)
	m.Command = types.StringValue(img.Command)
	m.CreatedAt = types.StringPointerValue(img.CreatedAt)
	return m
}
