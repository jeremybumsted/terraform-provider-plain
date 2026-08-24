// Package plain implements the Terraform provider for Plain, together with the
// generated GraphQL client it uses to talk to Plain's API.
package plain

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// defaultEndpoint is Plain's public GraphQL endpoint.
const defaultEndpoint = "https://core-api.uk.plain.com/graphql/v1"

var _ provider.Provider = (*plainProvider)(nil)

type plainProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &plainProvider{version: version}
	}
}

type plainProviderModel struct {
	APIKey   types.String `tfsdk:"api_key"`
	Endpoint types.String `tfsdk:"endpoint"`
}

func (p *plainProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "plain"
	resp.Version = p.version
}

func (p *plainProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage [Plain](https://www.plain.com) workspace configuration as code.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Plain API key. May also be set via the `PLAIN_API_KEY` environment variable. " +
					"The key needs the permissions matching the resources you manage.",
				Optional:  true,
				Sensitive: true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Plain GraphQL endpoint. May also be set via the `PLAIN_ENDPOINT` environment " +
					"variable. Defaults to `" + defaultEndpoint + "`.",
				Optional: true,
			},
		},
	}
}

func (p *plainProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config plainProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Config wins over environment, so a practitioner can override an ambient key.
	apiKey := os.Getenv("PLAIN_API_KEY")
	if !config.APIKey.IsNull() {
		apiKey = config.APIKey.ValueString()
	}

	endpoint := os.Getenv("PLAIN_ENDPOINT")
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	if config.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path("api_key"),
			"Unknown Plain API key",
			"The provider cannot be configured because api_key is not known until apply. "+
				"Set it from a known value, or use the PLAIN_API_KEY environment variable.",
		)
		return
	}

	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(
			path("api_key"),
			"Missing Plain API key",
			"Set the api_key provider attribute or the PLAIN_API_KEY environment variable.",
		)
		return
	}

	client := NewClient(endpoint, apiKey, p.version)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *plainProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewWorkflowResource,
		NewWorkflowRuleResource,
	}
}

func (p *plainProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
