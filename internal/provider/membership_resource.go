package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// NewMembershipResource is the resource factory used by the provider.
func NewMembershipResource() resource.Resource { return &membershipResource{} }

type membershipResource struct {
	client *metabase.Client
}

type membershipModel struct {
	ID             types.String `tfsdk:"id"`
	GroupID        types.Int64  `tfsdk:"group_id"`
	UserID         types.Int64  `tfsdk:"user_id"`
	IsGroupManager types.Bool   `tfsdk:"is_group_manager"`
}

func (r *membershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions_group_membership"
}

func (r *membershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One user's membership of one permissions group. Destroying it removes the user from the group. " +
			"Membership of All Users is automatic and cannot be managed. Membership of Administrators makes the user a Metabase admin. " +
			"Resolve the user with the `metabase_user` data source; this provider never creates or deactivates users.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<group_id>:<user_id>`. Also the import id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.Int64Attribute{
				MarkdownDescription: "Permissions group id.",
				Required:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"user_id": schema.Int64Attribute{
				MarkdownDescription: "User id, usually `data.metabase_user.<name>.id`.",
				Required:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"is_group_manager": schema.BoolAttribute{
				MarkdownDescription: "Whether the user manages the group's membership. Group managers are a Metabase Enterprise feature; on the open-source edition this is always `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
		},
	}
}

func (r *membershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *membershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gid, uid := int(plan.GroupID.ValueInt64()), int(plan.UserID.ValueInt64())

	existing, err := r.client.FindMembership(ctx, gid, uid)
	if err != nil {
		resp.Diagnostics.AddError("Read memberships failed", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddError("Membership already exists",
			fmt.Sprintf("user %d is already in group %d; import it with id %d:%d", uid, gid, gid, uid))
		return
	}
	if err := r.client.AddMembership(ctx, gid, uid, plan.IsGroupManager.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Add membership failed", err.Error())
		return
	}
	plan.ID = types.StringValue(fmt.Sprintf("%d:%d", gid, uid))
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *membershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.client.FindMembership(ctx, int(state.GroupID.ValueInt64()), int(state.UserID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Read memberships failed", err.Error())
		return
	}
	if m == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.IsGroupManager = types.BoolValue(m.IsGroupManager)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *membershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.client.FindMembership(ctx, int(plan.GroupID.ValueInt64()), int(plan.UserID.ValueInt64()))
	if err != nil || m == nil {
		resp.Diagnostics.AddError("Membership not found", fmt.Sprintf("%v", err))
		return
	}
	if m.IsGroupManager != plan.IsGroupManager.ValueBool() {
		if err := r.client.SetGroupManager(ctx, m.MembershipID, plan.IsGroupManager.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Update membership failed", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *membershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.client.FindMembership(ctx, int(state.GroupID.ValueInt64()), int(state.UserID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Read memberships failed", err.Error())
		return
	}
	if m == nil {
		return
	}
	if err := r.client.RemoveMembership(ctx, m.MembershipID); err != nil && !metabase.IsNotFound(err) {
		resp.Diagnostics.AddError("Remove membership failed", err.Error())
	}
}

// ImportState takes `<group_id>:<user_id>`.
func (r *membershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Bad import id", "expected <group_id>:<user_id>")
		return
	}
	gid, err1 := strconv.ParseInt(parts[0], 10, 64)
	uid, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		resp.Diagnostics.AddError("Bad import id", "expected <group_id>:<user_id>, both numeric")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), gid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), uid)...)
}
