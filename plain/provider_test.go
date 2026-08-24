package plain

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestProviderSchema asserts the provider schema is internally valid. The
// framework validates naming and attribute rules for us here.
func TestProviderSchema(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	resp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("provider schema invalid: %s", diags)
	}
}

// TestResourceSchemas asserts every registered resource has a valid schema and
// a type name prefixed with the provider name.
func TestResourceSchemas(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		metaResp := &fwresource.MetadataResponse{}
		r.Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "plain"}, metaResp)

		schemaResp := &fwresource.SchemaResponse{}
		r.Schema(ctx, fwresource.SchemaRequest{}, schemaResp)

		if schemaResp.Diagnostics.HasError() {
			t.Fatalf("%s: schema diagnostics: %s", metaResp.TypeName, schemaResp.Diagnostics)
		}
		if diags := schemaResp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s: schema invalid: %s", metaResp.TypeName, diags)
		}
	}
}
