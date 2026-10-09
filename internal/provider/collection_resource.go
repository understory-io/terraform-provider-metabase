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

// NewCollectionResource is the resource factory used by the provider.
func NewCollectionResource() resource.Resource { return &collectionResource{} }

type collectionResource struct {
	client *metabase.Client
}

type collectionModel struct {
	ID          types.String `tfsdk:"id"`
	EntityID    types.String `tfsdk:"entity_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ParentID    types.Int64  `tfsdk:"parent_id"`
	Archived    types.Bool   `tfsdk:"archived"`
	Prune       types.Bool   `tfsdk:"prune"`
}

func (r *collectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_collection"
}

func (r *collectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A shared Metabase collection: its name, description, place in the tree and whether it is archived. " +
			"Personal collections, and collections inside them, are out of scope. " +
			"Removing this resource only forgets the collection; set `prune = true` to archive it (move it to the trash) on destroy. " +
			"Who can open the collection is `metabase_collection_permissions`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Numeric collection id, as a string.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"entity_id": schema.StringAttribute{
				MarkdownDescription: "The 21-character entity id. Stable across instances and serialisation, and accepted wherever Metabase takes a collection reference (`mb collection get <entity_id>`), so other repositories should refer to a collection by this rather than by the numeric id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description shown on the collection page. Null and empty are the same thing to Metabase and are both read back as null.",
				Optional:            true,
			},
			"parent_id": schema.Int64Attribute{
				MarkdownDescription: "Id of the parent collection. Null puts the collection at the root (\"Our analytics\"). Changing it moves the collection and everything in it.",
				Optional:            true,
			},
			"archived": schema.BoolAttribute{
				MarkdownDescription: "Whether the collection is in the trash. Archiving a collection archives its contents with it.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"prune": schema.BoolAttribute{
				MarkdownDescription: "When `true`, destroying this resource archives the collection. Defaults to `false`: destroy only removes it from state.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
		},
	}
}

func (r *collectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *collectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan collectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.Description.IsNull() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.ParentID.IsNull() {
		body["parent_id"] = plan.ParentID.ValueInt64()
	}
	col, err := r.client.CreateCollection(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Create collection failed", err.Error())
		return
	}
	if plan.Archived.ValueBool() {
		if col, err = r.client.UpdateCollection(ctx, col.ID, map[string]any{"archived": true}); err != nil {
			resp.Diagnostics.AddError("Archive collection failed", err.Error())
			return
		}
	}
	setCollection(&plan, col)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *collectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state collectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	col, err := r.client.GetCollection(ctx, state.ID.ValueString())
	if metabase.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read collection failed", err.Error())
		return
	}
	if col.PersonalOwnerID != nil {
		resp.Diagnostics.AddError("Personal collection", fmt.Sprintf("collection %d is a personal collection, which this provider does not manage", col.ID))
		return
	}
	setCollection(&state, col)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *collectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state collectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bad collection id", err.Error())
		return
	}

	body := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		if plan.Description.IsNull() {
			body["description"] = nil
		} else {
			body["description"] = plan.Description.ValueString()
		}
	}
	if !plan.ParentID.Equal(state.ParentID) {
		if plan.ParentID.IsNull() {
			body["parent_id"] = nil
		} else {
			body["parent_id"] = plan.ParentID.ValueInt64()
		}
	}
	if !plan.Archived.Equal(state.Archived) {
		body["archived"] = plan.Archived.ValueBool()
	}

	col, err := r.client.GetCollection(ctx, state.ID.ValueString())
	if len(body) > 0 {
		col, err = r.client.UpdateCollection(ctx, id, body)
	}
	if err != nil {
		resp.Diagnostics.AddError("Update collection failed", err.Error())
		return
	}
	plan.ID = state.ID
	setCollection(&plan, col)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *collectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state collectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !state.Prune.ValueBool() || state.Archived.ValueBool() {
		return
	}
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bad collection id", err.Error())
		return
	}
	if _, err := r.client.UpdateCollection(ctx, id, map[string]any{"archived": true}); err != nil && !metabase.IsNotFound(err) {
		resp.Diagnostics.AddError("Archive collection failed", err.Error())
	}
}

// ImportState accepts the numeric id or the entity id.
func (r *collectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	col, err := r.client.GetCollection(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Import collection failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.Itoa(col.ID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("prune"), false)...)
}

func setCollection(m *collectionModel, col *metabase.Collection) {
	m.ID = types.StringValue(strconv.Itoa(col.ID))
	m.EntityID = types.StringValue(col.EntityID)
	m.Name = types.StringValue(col.Name)
	if col.Description == nil || *col.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(*col.Description)
	}
	if p := col.ParentID(); p != nil {
		m.ParentID = types.Int64Value(int64(*p))
	} else {
		m.ParentID = types.Int64Null()
	}
	m.Archived = types.BoolValue(col.Archived)
}
