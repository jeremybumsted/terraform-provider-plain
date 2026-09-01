package plain

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// IdentitySchema declares the workflow's resource identity: its Plain ID, and
// nothing else.
//
// Plain mints the ID at create and never changes it for the life of the
// workflow, and prefixed Plain IDs are globally unique, so one ID names at most
// one workflow. That is precisely what Terraform asks of an identity, and it is
// the same reason ImportState accepts a bare ID as a sufficient address.
//
// Declaring an identity is not free. From here on the framework rejects any
// Create, Read, Update or ImportState that returns without identity data —
// "Missing Resource Identity After Create" and friends — so every one of those
// writes it. Read is the one that matters for existing practitioners: state
// written before identity support carries none, and Read is where it is filled
// in on the next refresh.
func (r *workflowResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"id": identityschema.StringAttribute{
				RequiredForImport: true,
				Description:       "Plain's ID for the workflow, beginning with \"wf_\".",
			},
		},
	}
}

// setIdentity writes the workflow ID into the resource identity.
//
// The pointer is checked rather than assumed: the framework pre-populates it on
// every request for a resource that supports identity, but the unit tests build
// responses directly, and a nil identity there should be a no-op rather than a
// panic.
func setIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity, workflowID string) diag.Diagnostics {
	if identity == nil {
		return nil
	}

	return identity.SetAttribute(ctx, path("id"), workflowID)
}
