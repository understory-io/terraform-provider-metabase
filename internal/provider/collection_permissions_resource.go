package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// NewCollectionPermissionsResource is the resource factory used by the provider.
func NewCollectionPermissionsResource() resource.Resource { return &collectionPermissionsResource{} }

type collectionPermissionsResource struct {
	client *metabase.Client
}

type collectionPermissionsModel struct {
	ID           types.String `tfsdk:"id"`
	CollectionID types.String `tfsdk:"collection_id"`
	Access       types.Map    `tfsdk:"access"`
}

func (r *collectionPermissionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_collection_permissions"
}

func (r *collectionPermissionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Who can open one collection: the whole column of `/api/collection/graph` for that collection, as a map of group name to `read` or `write`. " +
			"**Authoritative** for the collection: a group left out of `access` has no access, so a grant added in the Metabase UI shows up as drift. " +
			"Administrators always have `write` and must not be listed. " +
			"Metabase does not inherit permissions at read time (a new sub-collection copies its parent's once, when created), so every collection needs its own resource. " +
			"Destroying this resource leaves the grants as they are; to revoke access, set `access = {}`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `collection_id`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"collection_id": schema.StringAttribute{
				MarkdownDescription: "Numeric collection id, or `root` for \"Our analytics\".",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"access": schema.MapAttribute{
				MarkdownDescription: "Group name -> `read` | `write`.",
				ElementType:         types.StringType,
				Required:            true,
				Validators: []validator.Map{
					mapvalidator.ValueStringsAre(stringvalidator.OneOf("read", "write")),
				},
			},
		},
	}
}

func (r *collectionPermissionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *collectionPermissionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *collectionPermissionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *collectionPermissionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state collectionPermissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	access, err := r.readAccess(ctx, state.CollectionID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read collection permissions failed", err.Error())
		return
	}
	m, d := types.MapValueFrom(ctx, types.StringType, access)
	resp.Diagnostics.Append(d...)
	state.Access = m
	state.ID = state.CollectionID
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete only forgets the resource. Revoking every group's access to a
// collection because a block was removed from a file is too easy to do by
// accident; `access = {}` says it on purpose.
func (r *collectionPermissionsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ImportState takes the collection id (numeric, or `root`).
func (r *collectionPermissionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != metabase.RootCollection {
		if _, err := strconv.Atoi(req.ID); err != nil {
			resp.Diagnostics.AddError("Bad import id", "expected a numeric collection id or `root`")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("collection_id"), req.ID)...)
}

// readAccess returns group name -> level for every non-admin group with more
// than "none" on the collection.
func (r *collectionPermissionsResource) readAccess(ctx context.Context, collection string) (map[string]string, error) {
	g, err := r.client.GetCollectionGraph(ctx)
	if err != nil {
		return nil, err
	}
	idx, err := loadGroups(ctx, r.client, keys(g.Groups))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for gid := range g.Groups {
		id, _ := strconv.Atoi(gid)
		grp, ok := idx.byID[id]
		if !ok || grp.IsAdmin() {
			continue
		}
		if lvl := g.Access(gid, collection); lvl != "none" {
			out[grp.Name] = lvl
		}
	}
	return out, nil
}

func (r *collectionPermissionsResource) apply(ctx context.Context, planned tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var plan collectionPermissionsModel
	diags.Append(planned.Get(ctx, &plan)...)
	if diags.HasError() {
		return
	}
	collection := plan.CollectionID.ValueString()
	want := map[string]string{}
	diags.Append(plan.Access.ElementsAs(ctx, &want, false)...)
	if diags.HasError() {
		return
	}

	err := r.client.ModifyCollectionGraph(ctx, func(g *metabase.CollectionGraph) (map[string]map[string]string, error) {
		idx, err := loadGroups(ctx, r.client, keys(g.Groups))
		if err != nil {
			return nil, err
		}
		desired := map[int]string{}
		for name, lvl := range want {
			id, err := idx.id(name)
			if err != nil {
				return nil, err
			}
			desired[id] = lvl
		}
		cells := map[string]map[string]string{}
		for id, grp := range idx.byID {
			if grp.IsAdmin() {
				continue
			}
			lvl, ok := desired[id]
			if !ok {
				lvl = "none"
			}
			gid := strconv.Itoa(id)
			if g.Access(gid, collection) != lvl {
				if cells[gid] == nil {
					cells[gid] = map[string]string{}
				}
				cells[gid][collection] = lvl
			}
		}
		return cells, nil
	})
	if err != nil {
		diags.AddError("Write collection permissions failed", fmt.Sprintf("collection %s: %s", collection, err))
		return
	}
	plan.ID = plan.CollectionID
	diags.Append(state.Set(ctx, plan)...)
}
