package plain

import (
	"context"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// workflowIdentitySchema builds the resource's identity schema.
func workflowIdentitySchema(t *testing.T, ctx context.Context, r fwresource.Resource) identityschema.Schema {
	t.Helper()

	withIdentity, ok := r.(fwresource.ResourceWithIdentity)
	if !ok {
		t.Fatalf("%T does not implement resource.ResourceWithIdentity", r)
	}

	resp := &fwresource.IdentitySchemaResponse{}
	withIdentity.IdentitySchema(ctx, fwresource.IdentitySchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("identity schema: %v", resp.Diagnostics)
	}

	return resp.IdentitySchema
}

// nullIdentity returns an identity object with no data, which is what the
// framework hands a resource to write into.
func nullIdentity(ctx context.Context, sch identityschema.Schema) *tfsdk.ResourceIdentity {
	return &tfsdk.ResourceIdentity{
		Schema: sch,
		Raw:    tftypes.NewValue(sch.Type().TerraformType(ctx), nil),
	}
}

// identityWithID returns an identity object carrying id, which is what an
// import block's `identity` argument produces.
func identityWithID(ctx context.Context, sch identityschema.Schema, id tftypes.Value) *tfsdk.ResourceIdentity {
	return &tfsdk.ResourceIdentity{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id": id,
		}),
	}
}

// TestIdentitySchemaIsTheWorkflowID pins the identity to exactly one attribute.
//
// Identity is a contract with Terraform, not an implementation detail: adding an
// attribute to it changes how every existing state is matched to its remote
// object, so a second attribute appearing here should be a deliberate decision
// with a schema version bump behind it, not a drive-by edit.
func TestIdentitySchemaIsTheWorkflowID(t *testing.T) {
	ctx := context.Background()
	sch := workflowIdentitySchema(t, ctx, NewWorkflowResource())

	if len(sch.Attributes) != 1 {
		t.Fatalf("identity has %d attributes, want exactly 1: %v", len(sch.Attributes), sch.Attributes)
	}

	attr, ok := sch.Attributes["id"]
	if !ok {
		t.Fatalf("identity has no \"id\" attribute: %v", sch.Attributes)
	}

	str, ok := attr.(identityschema.StringAttribute)
	if !ok {
		t.Fatalf("identity \"id\" is %T, want identityschema.StringAttribute", attr)
	}
	if !str.RequiredForImport {
		t.Error("identity \"id\" is not RequiredForImport, so an import block could omit the only thing that names the workflow")
	}
	if str.Description == "" {
		t.Error("identity \"id\" has no description")
	}
}

// TestSetIdentityToleratesNilIdentity covers the guard in setIdentity. The
// framework populates the pointer on every request for a resource that supports
// identity, but unit tests build responses directly, and a nil there should be a
// no-op rather than a panic.
func TestSetIdentityToleratesNilIdentity(t *testing.T) {
	if diags := setIdentity(context.Background(), nil, "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"); diags.HasError() {
		t.Fatalf("setIdentity(nil) reported errors: %v", diags)
	}
}

// TestImportStateWritesIdentity asserts that both addressing modes leave the
// identity populated. Without this the import is still functional — the refresh
// that follows fills identity in — but the plan Terraform prints in between
// reports a null identity.
func TestImportStateWritesIdentity(t *testing.T) {
	ctx := context.Background()
	const id = "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"

	r := NewWorkflowResource()
	sch := workflowIdentitySchema(t, ctx, r)

	tests := map[string]fwresource.ImportStateRequest{
		"by id string": {ID: id, Identity: nullIdentity(ctx, sch)},
		"by identity": {
			Identity: identityWithID(ctx, sch, tftypes.NewValue(tftypes.String, id)),
		},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			resp := &fwresource.ImportStateResponse{
				State:    emptyWorkflowState(t, ctx, r),
				Identity: nullIdentity(ctx, sch),
			}
			r.(fwresource.ResourceWithImportState).ImportState(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("ImportState: %v", resp.Diagnostics)
			}

			var gotID types.String
			if diags := resp.Identity.GetAttribute(ctx, path("id"), &gotID); diags.HasError() {
				t.Fatalf("reading identity back: %v", diags)
			}
			if gotID.ValueString() != id {
				t.Errorf("identity id = %q, want %q", gotID.ValueString(), id)
			}
		})
	}
}

// TestImportStateByIdentityRejectsBadIDs covers the identity-addressed
// equivalent of the guard in TestImportStateRejectsNonWorkflowID: an identity
// carrying something that is not a workflow ID must produce the same diagnostic,
// not fall through to a Read against a nonsense address.
func TestImportStateByIdentityRejectsBadIDs(t *testing.T) {
	ctx := context.Background()

	r := NewWorkflowResource()
	sch := workflowIdentitySchema(t, ctx, r)

	tests := map[string]tftypes.Value{
		"null":    tftypes.NewValue(tftypes.String, nil),
		"empty":   tftypes.NewValue(tftypes.String, ""),
		"step id": tftypes.NewValue(tftypes.String, stepIDPrefix+"01HXXXXXXXXXXXXXXXXXXXXXXX"),
		"garbage": tftypes.NewValue(tftypes.String, "banana"),
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			resp := &fwresource.ImportStateResponse{
				State:    emptyWorkflowState(t, ctx, r),
				Identity: nullIdentity(ctx, sch),
			}
			r.(fwresource.ResourceWithImportState).ImportState(ctx, fwresource.ImportStateRequest{
				Identity: identityWithID(ctx, sch, value),
			}, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatalf("ImportState accepted %s identity", name)
			}
			if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, workflowIDPrefix) {
				t.Errorf("detail does not mention %q: %s", workflowIDPrefix, detail)
			}
		})
	}
}
