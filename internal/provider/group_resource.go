package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// NewGroupResource is the resource factory used by the provider.
func NewGroupResource() resource.Resource { return &groupResource{} }

type groupResource struct {
	client *metabase.Client
}

type groupModel struct {
	ID    types.String `tfsdk:"id"`
	Name  types.String `tfsdk:"name"`
	Prune types.Bool   `tfsdk:"prune"`
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Metabase permissions group. " +
			"Removing this resource only forgets the group; set `prune = true` to delete it on destroy, which also drops every membership and grant it has. " +
			"The built-in groups (Administrators, All Users, Data Analysts) cannot be created, renamed or deleted: refer to them with the `metabase_permissions_group` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Numeric group id, as a string.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Group name. Unique within the instance.",
				Required:            true,
			},
			"prune": schema.BoolAttribute{
				MarkdownDescription: "When `true`, destroying this resource deletes the group. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.CreateGroup(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Create group failed", err.Error())
		return
	}
	plan.ID = types.StringValue(strconv.Itoa(g.ID))
	plan.Name = types.StringValue(g.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bad group id", err.Error())
		return
	}
	g, err := r.client.GetGroup(ctx, id)
	if metabase.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read group failed", err.Error())
		return
	}
	state.Name = types.StringValue(g.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bad group id", err.Error())
		return
	}
	if !plan.Name.Equal(state.Name) {
		if err := r.client.RenameGroup(ctx, id, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Rename group failed", err.Error())
			return
		}
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !state.Prune.ValueBool() {
		return
	}
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bad group id", err.Error())
		return
	}
	if err := r.client.DeleteGroup(ctx, id); err != nil && !metabase.IsNotFound(err) {
		resp.Diagnostics.AddError("Delete group failed", err.Error())
	}
}

// ImportState takes the numeric group id. Built-in groups are refused: they
// are not Terraform's to rename or delete.
func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.Atoi(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Bad import id", "expected a numeric group id")
		return
	}
	g, err := r.client.GetGroup(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Import group failed", err.Error())
		return
	}
	if g.MagicGroupType != nil {
		resp.Diagnostics.AddError("Built-in group",
			fmt.Sprintf("%q is a built-in group; refer to it with data \"metabase_permissions_group\" instead of importing it", g.Name))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("prune"), false)...)
}
