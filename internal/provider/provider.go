// Package provider implements the terraform-provider-metabase Terraform
// provider: collections, permission groups, memberships and the two
// permission graphs of a Metabase instance, plus lookups for users,
// databases, groups and collections.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/understory-io/terraform-provider-metabase/internal/metabase"
)

// New returns a provider factory bound to the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &metabaseProvider{version: version} }
}

type metabaseProvider struct {
	version string
}

type providerModel struct {
	URL    types.String `tfsdk:"url"`
	APIKey types.String `tfsdk:"api_key"`
}

func (p *metabaseProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "metabase"
	resp.Version = p.version
}

func (p *metabaseProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages who can see what in Metabase: collections, permission groups, group memberships, " +
			"and the collection and data permission graphs.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				MarkdownDescription: "Base URL of the Metabase instance, e.g. `https://metabase.example.com`. Falls back to the `METABASE_URL` environment variable.",
				Optional:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Metabase API key. A key acts as the group it was created in, so it must be an Administrators key to write permissions. Falls back to the `METABASE_API_KEY` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *metabaseProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	url := data.URL.ValueString()
	if url == "" {
		url = os.Getenv("METABASE_URL")
	}
	if url == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Metabase URL",
			"Provide `url` in the provider block or set the METABASE_URL environment variable.")
	}
	key := data.APIKey.ValueString()
	if key == "" {
		key = os.Getenv("METABASE_API_KEY")
	}
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Metabase API key",
			"Provide `api_key` in the provider block or set the METABASE_API_KEY environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	client := metabase.New(url, key, metabase.WithUserAgent("terraform-provider-metabase/"+p.version))
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *metabaseProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCollectionResource,
		NewGroupResource,
		NewMembershipResource,
		NewCollectionPermissionsResource,
		NewDatabasePermissionsResource,
	}
}

func (p *metabaseProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewUserDataSource,
		NewDatabaseDataSource,
		NewCollectionDataSource,
		NewCollectionsDataSource,
		NewGroupDataSource,
		NewGroupsDataSource,
	}
}

// clientFrom pulls the configured client out of ProviderData, which is nil
// during validation before the provider is configured.
func clientFrom(data any, diags *diag.Diagnostics) *metabase.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*metabase.Client)
	if !ok {
		diags.AddError("Unexpected provider data", "expected *metabase.Client")
		return nil
	}
	return c
}
