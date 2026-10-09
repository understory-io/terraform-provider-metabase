package provider

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// ---------------------------------------------------------------- metabase_user

// NewUserDataSource is the data source factory used by the provider.
func NewUserDataSource() datasource.DataSource { return &userDataSource{} }

type userDataSource struct{ client *metabase.Client }

type userModel struct {
	ID          types.String `tfsdk:"id"`
	Email       types.String `tfsdk:"email"`
	IsActive    types.Bool   `tfsdk:"is_active"`
	IsSuperuser types.Bool   `tfsdk:"is_superuser"`
	SSOSource   types.String `tfsdk:"sso_source"`
}

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks a Metabase user up by email. Users are not managed by this provider: with Google sign-in, " +
			"Metabase creates an account the first time someone signs in, so a membership for a person who has never signed in cannot be declared yet.",
		Attributes: map[string]schema.Attribute{
			"email":        schema.StringAttribute{Required: true, MarkdownDescription: "Email address; matched case-insensitively."},
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric user id, as a string."},
			"is_active":    schema.BoolAttribute{Computed: true, MarkdownDescription: "False when the user has been deactivated."},
			"is_superuser": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the user is in Administrators."},
			"sso_source":   schema.StringAttribute{Computed: true, MarkdownDescription: "`google` for Google sign-in accounts, null for password accounts."},
		},
	}
}

func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := d.client.FindUserByEmail(ctx, m.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("List users failed", err.Error())
		return
	}
	if u == nil {
		resp.Diagnostics.AddError("User not found",
			fmt.Sprintf("no Metabase user with email %q; they may not have signed in yet", m.Email.ValueString()))
		return
	}
	m.ID = types.StringValue(strconv.Itoa(u.ID))
	m.IsActive = types.BoolValue(u.IsActive)
	m.IsSuperuser = types.BoolValue(u.IsSuperuser)
	m.SSOSource = types.StringPointerValue(u.SSOSource)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// ------------------------------------------------------------ metabase_database

// NewDatabaseDataSource is the data source factory used by the provider.
func NewDatabaseDataSource() datasource.DataSource { return &databaseDataSource{} }

type databaseDataSource struct{ client *metabase.Client }

type databaseModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Engine types.String `tfsdk:"engine"`
}

func (d *databaseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (d *databaseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks a database connection up by its display name.",
		Attributes: map[string]schema.Attribute{
			"name":   schema.StringAttribute{Required: true, MarkdownDescription: "Display name of the connection in Metabase."},
			"id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric database id, as a string."},
			"engine": schema.StringAttribute{Computed: true, MarkdownDescription: "Driver, e.g. `clickhouse`, `postgres`."},
		},
	}
}

func (d *databaseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *databaseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dbs, err := d.client.ListDatabases(ctx)
	if err != nil {
		resp.Diagnostics.AddError("List databases failed", err.Error())
		return
	}
	for _, db := range dbs {
		if db.Name == m.Name.ValueString() {
			m.ID = types.StringValue(strconv.Itoa(db.ID))
			m.Engine = types.StringValue(db.Engine)
			resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
			return
		}
	}
	resp.Diagnostics.AddError("Database not found", fmt.Sprintf("no database named %q", m.Name.ValueString()))
}

// ---------------------------------------------------------- metabase_collection

// NewCollectionDataSource is the data source factory used by the provider.
func NewCollectionDataSource() datasource.DataSource { return &collectionDataSource{} }

type collectionDataSource struct{ client *metabase.Client }

type collectionDSModel struct {
	ID          types.String `tfsdk:"id"`
	EntityID    types.String `tfsdk:"entity_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ParentID    types.Int64  `tfsdk:"parent_id"`
	Archived    types.Bool   `tfsdk:"archived"`
}

func (d *collectionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_collection"
}

func (d *collectionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one collection by numeric id or entity id. Set exactly one of `id` and `entity_id`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Numeric collection id."},
			"entity_id":   schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "21-character entity id."},
			"name":        schema.StringAttribute{Computed: true},
			"description": schema.StringAttribute{Computed: true},
			"parent_id":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Null at the root."},
			"archived":    schema.BoolAttribute{Computed: true},
		},
	}
}

func (d *collectionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *collectionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m collectionDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ID.ValueString()
	if ref == "" {
		ref = m.EntityID.ValueString()
	}
	if ref == "" || (!m.ID.IsNull() && !m.EntityID.IsNull()) {
		resp.Diagnostics.AddError("Invalid lookup", "set exactly one of id and entity_id")
		return
	}
	col, err := d.client.GetCollection(ctx, ref)
	if err != nil {
		resp.Diagnostics.AddError("Read collection failed", err.Error())
		return
	}
	var rm collectionModel
	setCollection(&rm, col)
	m.ID, m.EntityID, m.Name, m.Description, m.ParentID, m.Archived = rm.ID, rm.EntityID, rm.Name, rm.Description, rm.ParentID, rm.Archived
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// --------------------------------------------------------- metabase_collections

// NewCollectionsDataSource is the data source factory used by the provider.
func NewCollectionsDataSource() datasource.DataSource { return &collectionsDataSource{} }

type collectionsDataSource struct{ client *metabase.Client }

type collectionsModel struct {
	IncludeArchived types.Bool `tfsdk:"include_archived"`
	IDs             types.Set  `tfsdk:"ids"`
	Collections     types.List `tfsdk:"collections"`
}

var collectionObjType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":        types.StringType,
	"entity_id": types.StringType,
	"name":      types.StringType,
	"parent_id": types.Int64Type,
	"archived":  types.BoolType,
}}

func (d *collectionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_collections"
}

func (d *collectionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every shared collection on the instance (personal collections and anything inside them excluded). " +
			"Meant for a `check` block that fails when a collection exists that no `metabase_collection` declares.",
		Attributes: map[string]schema.Attribute{
			"include_archived": schema.BoolAttribute{Optional: true, MarkdownDescription: "Include collections in the trash. Defaults to false."},
			"ids":              schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Collection ids."},
			"collections": schema.ListAttribute{Computed: true, ElementType: collectionObjType,
				MarkdownDescription: "`{id, entity_id, name, parent_id, archived}` per collection, ordered by id."},
		},
	}
}

func (d *collectionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *collectionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m collectionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cols, err := d.client.ListCollections(ctx)
	if err != nil {
		resp.Diagnostics.AddError("List collections failed", err.Error())
		return
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].ID < cols[j].ID })
	ids := []string{}
	objs := []attr.Value{}
	for _, c := range cols {
		if c.Archived && !m.IncludeArchived.ValueBool() {
			continue
		}
		var rm collectionModel
		setCollection(&rm, &c)
		ids = append(ids, rm.ID.ValueString())
		o, diags := types.ObjectValue(collectionObjType.AttrTypes, map[string]attr.Value{
			"id": rm.ID, "entity_id": rm.EntityID, "name": rm.Name, "parent_id": rm.ParentID, "archived": rm.Archived,
		})
		resp.Diagnostics.Append(diags...)
		objs = append(objs, o)
	}
	idSet, diags := types.SetValueFrom(ctx, types.StringType, ids)
	resp.Diagnostics.Append(diags...)
	list, diags := types.ListValue(collectionObjType, objs)
	resp.Diagnostics.Append(diags...)
	m.IDs, m.Collections = idSet, list
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// ---------------------------------------------------- metabase_permissions_group

// NewGroupDataSource is the data source factory used by the provider.
func NewGroupDataSource() datasource.DataSource { return &groupDataSource{} }

type groupDataSource struct{ client *metabase.Client }

type groupDSModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	BuiltIn        types.String `tfsdk:"built_in"`
	MemberUserIDs  types.Set    `tfsdk:"member_user_ids"`
	MemberAPIUsers types.Int64  `tfsdk:"api_key_member_count"`
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks a permissions group up by name, built-in groups included, and lists its active human members. " +
			"`member_user_ids` is what a `check` block compares against the declared memberships to catch someone added in the UI.",
		Attributes: map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true},
			"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric group id, as a string."},
			"built_in": schema.StringAttribute{Computed: true, MarkdownDescription: "`admin`, `all-internal-users` or `data-analyst` for Metabase's own groups, null otherwise."},
			"member_user_ids": schema.SetAttribute{Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Ids of the group's active human members. Deactivated users and API-key users are left out: neither can be managed through memberships."},
			"api_key_member_count": schema.Int64Attribute{Computed: true,
				MarkdownDescription: "How many API keys act as this group. A key's group is set when the key is created, not through memberships."},
		},
	}
}

func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The data graph names every group, including the Data Analysts group the
	// list leaves out while it is empty.
	graph, err := d.client.GetDataGraph(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read data permissions failed", err.Error())
		return
	}
	idx, err := loadGroups(ctx, d.client, keys(graph.Groups))
	if err != nil {
		resp.Diagnostics.AddError("List groups failed", err.Error())
		return
	}
	g, ok := idx.byName[m.Name.ValueString()]
	if !ok {
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("no permissions group named %q", m.Name.ValueString()))
		return
	}

	users, err := d.client.ListUsers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("List users failed", err.Error())
		return
	}
	human := map[int]bool{}
	for _, u := range users {
		human[u.ID] = u.IsActive
	}
	all, err := d.client.ListMemberships(ctx)
	if err != nil {
		resp.Diagnostics.AddError("List memberships failed", err.Error())
		return
	}
	// Empty, not nil: a nil slice becomes a null set, and a `for` over null
	// is an error in the configuration that reads it.
	ids := []string{}
	var apiKeys int64
	for _, ms := range all {
		if ms.GroupID != g.ID {
			continue
		}
		active, isHuman := human[ms.UserID]
		switch {
		case isHuman && active:
			ids = append(ids, strconv.Itoa(ms.UserID))
		case !isHuman && ms.UserID != internalUserID:
			apiKeys++
		}
	}
	sort.Strings(ids)
	set, diags := types.SetValueFrom(ctx, types.StringType, ids)
	resp.Diagnostics.Append(diags...)

	m.ID = types.StringValue(strconv.Itoa(g.ID))
	m.BuiltIn = types.StringPointerValue(g.MagicGroupType)
	m.MemberUserIDs = set
	m.MemberAPIUsers = types.Int64Value(apiKeys)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// internalUserID is the account Metabase uses for its own background work. It
// is in All Users, is not a person and is not an API key.
const internalUserID = 13371338

// --------------------------------------------------- metabase_permissions_groups

// NewGroupsDataSource is the data source factory used by the provider.
func NewGroupsDataSource() datasource.DataSource { return &groupsDataSource{} }

type groupsDataSource struct{ client *metabase.Client }

type groupsModel struct {
	Names types.Set `tfsdk:"names"`
}

func (d *groupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions_groups"
}

func (d *groupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The names of every permissions group, built-in ones included. Meant for a `check` block that fails when a group exists that the configuration does not declare.",
		Attributes: map[string]schema.Attribute{
			"names": schema.SetAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *groupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *groupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	graph, err := d.client.GetDataGraph(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Read data permissions failed", err.Error())
		return
	}
	idx, err := loadGroups(ctx, d.client, keys(graph.Groups))
	if err != nil {
		resp.Diagnostics.AddError("List groups failed", err.Error())
		return
	}
	names, diags := types.SetValueFrom(ctx, types.StringType, keys(idx.byName))
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, groupsModel{Names: names})...)
}
