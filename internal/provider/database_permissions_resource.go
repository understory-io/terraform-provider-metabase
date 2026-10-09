package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// NewDatabasePermissionsResource is the resource factory used by the provider.
func NewDatabasePermissionsResource() resource.Resource { return &databasePermissionsResource{} }

type databasePermissionsResource struct {
	client *metabase.Client
}

type databasePermissionsModel struct {
	ID            types.String `tfsdk:"id"`
	GroupID       types.Int64  `tfsdk:"group_id"`
	DatabaseID    types.Int64  `tfsdk:"database_id"`
	ViewData      types.String `tfsdk:"view_data"`
	CreateQueries types.String `tfsdk:"create_queries"`
	Download      types.String `tfsdk:"download"`
	DataModel     types.String `tfsdk:"data_model"`
	Details       types.String `tfsdk:"details"`
	Transforms    types.String `tfsdk:"transforms"`
}

// dataPerm describes one permission key of a (group, database) cell: its
// attribute, its key in the graph, the value Metabase means when the key is
// absent, and whether the wire value is wrapped as {"schemas": value}.
type dataPerm struct {
	attr    string
	key     string
	absent  string
	schemas bool
	values  []string
	doc     string
	get     func(*databasePermissionsModel) *types.String
}

var dataPerms = []dataPerm{
	{attr: "view_data", key: "view-data", absent: "", values: []string{"unrestricted", "blocked", "legacy-no-self-service", "impersonated", "sandboxed"},
		doc: "Whether the group can see data from this database at all. `unrestricted` on the open-source edition; the other values need Enterprise. Null leaves the key alone (the built-in Data Analysts group carries none).",
		get: func(m *databasePermissionsModel) *types.String { return &m.ViewData }},
	{attr: "create_queries", key: "create-queries", absent: "no", values: []string{"query-builder-and-native", "query-builder", "no"},
		doc: "Whether the group can write new questions: `query-builder-and-native` (including SQL), `query-builder`, or `no` (only open saved questions).",
		get: func(m *databasePermissionsModel) *types.String { return &m.CreateQueries }},
	{attr: "download", key: "download", absent: "none", schemas: true, values: []string{"full", "limited", "none"},
		doc: "Result downloads: `full`, `limited` (10,000 rows) or `none`.",
		get: func(m *databasePermissionsModel) *types.String { return &m.Download }},
	{attr: "data_model", key: "data-model", absent: "none", schemas: true, values: []string{"all", "none"},
		doc: "Whether the group can edit table metadata: `all` or `none`.",
		get: func(m *databasePermissionsModel) *types.String { return &m.DataModel }},
	{attr: "details", key: "details", absent: "no", values: []string{"yes", "no"},
		doc: "Whether the group can edit the connection details: `yes` or `no`.",
		get: func(m *databasePermissionsModel) *types.String { return &m.Details }},
	{attr: "transforms", key: "transforms", absent: "no", values: []string{"yes", "no"},
		doc: "Whether the group can write transforms against the database: `yes` or `no`. Needs `create_queries = \"query-builder-and-native\"`.",
		get: func(m *databasePermissionsModel) *types.String { return &m.Transforms }},
}

func (r *databasePermissionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_permissions"
}

func (r *databasePermissionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "`<group_id>:<database_id>`. Also the import id.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"group_id": schema.Int64Attribute{
			MarkdownDescription: "Permissions group id. Not the Administrators group, which always has full access.",
			Required:            true,
			PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
		},
		"database_id": schema.Int64Attribute{
			MarkdownDescription: "Database id, usually `data.metabase_database.<name>.id`.",
			Required:            true,
			PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
		},
	}
	for _, p := range dataPerms {
		a := schema.StringAttribute{
			MarkdownDescription: p.doc,
			Optional:            true,
			Validators:          []validator.String{stringvalidator.OneOf(p.values...)},
		}
		if p.absent != "" {
			a.MarkdownDescription += fmt.Sprintf(" Defaults to `%s`.", p.absent)
			a.Computed = true
			a.Default = stringdefault.StaticString(p.absent)
		}
		attrs[p.attr] = a
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "What one group may do with one database: its cell of `/api/permissions/graph`. " +
			"**Authoritative** for the cell: every permission key not set falls back to the lowest level, so a change made in the Metabase UI shows up as drift. " +
			"Only whole-database levels are supported; a cell the UI has made granular (per schema or table) is reported as an error rather than flattened. " +
			"Destroying this resource leaves the cell as it is.",
		Attributes: attrs,
	}
}

func (r *databasePermissionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *databasePermissionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *databasePermissionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.apply(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *databasePermissionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databasePermissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.GetDataGraph(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read data permissions failed", err.Error())
		return
	}
	gid, dbid := strconv.FormatInt(state.GroupID.ValueInt64(), 10), strconv.FormatInt(state.DatabaseID.ValueInt64(), 10)
	cell, ok := g.Groups[gid][dbid]
	if !ok {
		if _, groupExists := g.Groups[gid]; !groupExists {
			resp.State.RemoveResource(ctx)
			return
		}
		cell = map[string]any{}
	}
	for _, p := range dataPerms {
		v, err := p.read(cell)
		if err != nil {
			resp.Diagnostics.AddError("Unsupported data permission",
				fmt.Sprintf("group %s, database %s: %s", gid, dbid, err))
			return
		}
		if v == "" {
			*p.get(&state) = types.StringNull()
		} else {
			*p.get(&state) = types.StringValue(v)
		}
	}
	state.ID = types.StringValue(gid + ":" + dbid)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete only forgets the resource; see the resource description.
func (r *databasePermissionsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ImportState takes `<group_id>:<database_id>`.
func (r *databasePermissionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Bad import id", "expected <group_id>:<database_id>")
		return
	}
	gid, err1 := strconv.ParseInt(parts[0], 10, 64)
	dbid, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		resp.Diagnostics.AddError("Bad import id", "expected <group_id>:<database_id>, both numeric")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), gid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database_id"), dbid)...)
}

func (r *databasePermissionsResource) apply(ctx context.Context, planned tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	var plan databasePermissionsModel
	diags.Append(planned.Get(ctx, &plan)...)
	if diags.HasError() {
		return
	}
	gid, dbid := strconv.FormatInt(plan.GroupID.ValueInt64(), 10), strconv.FormatInt(plan.DatabaseID.ValueInt64(), 10)

	grp, err := r.client.GetGroup(ctx, int(plan.GroupID.ValueInt64()))
	if err != nil {
		diags.AddError("Read group failed", err.Error())
		return
	}
	if grp.IsAdmin() {
		diags.AddAttributeError(path.Root("group_id"), "Administrators group",
			"The Administrators group always has full access to every database and cannot be configured.")
		return
	}

	err = r.client.ModifyDataGraph(ctx, func(g *metabase.DataGraph) (map[string]map[string]map[string]any, error) {
		cell := g.Groups[gid][dbid]
		change := map[string]any{}
		for _, p := range dataPerms {
			want := p.get(&plan)
			if want.IsNull() || want.IsUnknown() {
				continue
			}
			have, err := p.read(cell)
			if err != nil {
				// Writing a whole-database level over a granular cell is a
				// deliberate flattening, which is what the config asks for.
				have = ""
			}
			if have != want.ValueString() {
				change[p.key] = p.wire(want.ValueString())
			}
		}
		if len(change) == 0 {
			return nil, nil
		}
		return map[string]map[string]map[string]any{gid: {dbid: change}}, nil
	})
	if err != nil {
		diags.AddError("Write data permissions failed", fmt.Sprintf("group %s, database %s: %s", gid, dbid, err))
		return
	}
	plan.ID = types.StringValue(gid + ":" + dbid)
	diags.Append(state.Set(ctx, plan)...)
}

// read returns the level the cell holds for this key: the absent default when
// the key is missing, an error when the value is granular.
func (p dataPerm) read(cell map[string]any) (string, error) {
	v, ok := cell[p.key]
	if !ok || v == nil {
		return p.absent, nil
	}
	if p.schemas {
		obj, isObj := v.(map[string]any)
		if !isObj {
			return "", fmt.Errorf("%s is %v, expected {\"schemas\": ...}", p.key, v)
		}
		v = obj["schemas"]
	}
	s, isStr := v.(string)
	if !isStr {
		return "", fmt.Errorf("%s is granular (per schema or table), which this provider does not model", p.key)
	}
	return s, nil
}

func (p dataPerm) wire(v string) any {
	if p.schemas {
		return map[string]any{"schemas": v}
	}
	return v
}
