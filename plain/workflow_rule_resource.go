package plain

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = (*workflowRuleResource)(nil)
	_ resource.ResourceWithConfigure   = (*workflowRuleResource)(nil)
	_ resource.ResourceWithImportState = (*workflowRuleResource)(nil)
)

func NewWorkflowRuleResource() resource.Resource { return &workflowRuleResource{} }

type workflowRuleResource struct {
	client *Client
}

// workflowRuleModel mirrors the resource schema.
//
// A rule is flat: no steps, no graph, no key-to-ID resolution. Everything that
// makes plain_workflow complicated is absent here, and the only wrinkle is that
// Plain publishes rules with a toggle rather than a field.
type workflowRuleModel struct {
	ID        types.String         `tfsdk:"id"`
	Name      types.String         `tfsdk:"name"`
	Payload   jsontypes.Normalized `tfsdk:"payload"`
	Published types.Bool           `tfsdk:"published"`
	Order     types.Int64          `tfsdk:"order"`
}

func (r *workflowRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow_rule"
}

func (r *workflowRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Plain workflow rule: a single named JSON rule definition.\n\n" +
			"Workflow rules are Plain's older condition-plus-action automation model. Despite the " +
			"shared name they are unrelated to `plain_workflow`, which is the newer step-based model — " +
			"a rule has no steps and no graph. Use `plain_workflow` for new automation unless you are " +
			"managing rules that already exist.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Plain's ID for the workflow rule.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for the rule.",
				Required:            true,
			},
			"payload": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "JSON-encoded rule definition. Compared semantically, so formatting " +
					"and key order do not cause drift.\n\n" +
					"~> Plain does not publish a field reference for rule payloads, and its GraphQL schema " +
					"describes this field only as \"JSON-encoded payload of the rule definition\". Keeping " +
					"the payload in a file and reading it in with `file()` avoids guessing at a shape; to " +
					"get a starting point, read an existing rule's payload back from the API. Rule payloads " +
					"embed workspace-specific IDs, so one copied from another workspace references IDs that " +
					"do not exist here.",
				Required: true,
			},
			"published": schema.BoolAttribute{
				MarkdownDescription: "Whether the rule is live. Unpublished rules are inactive drafts. " +
					"Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"order": schema.Int64Attribute{
				MarkdownDescription: "Display order relative to other rules. Lower values appear first.",
				Optional:            true,
				Computed:            true,
			},
		},
	}
}

func (r *workflowRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("Expected *Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}

	r.client = client
}

func (r *workflowRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path("id"), req, resp)
}

func (r *workflowRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workflowRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// createWorkflowRule takes only name and payload, and the rule always starts
	// as a draft. Order and published state are follow-up calls.
	created, err := CreateWorkflowRule(ctx, r.client, &CreateWorkflowRuleInput{
		Name:    plan.Name.ValueString(),
		Payload: plan.Payload.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create workflow rule", err.Error())
		return
	}
	if created.CreateWorkflowRule.Error != nil {
		resp.Diagnostics.Append(mutationDiags("Unable to create workflow rule",
			created.CreateWorkflowRule.Error, workflowRuleAttributes)...)
		return
	}

	rule := created.CreateWorkflowRule.WorkflowRule
	ruleID := rule.Id

	// The rule exists from here on. Any later failure must still write the ID to
	// state, or Terraform loses track of it and the next apply orphans it.
	saveID := func() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("id"), ruleID)...)
	}

	if !plan.Order.IsNull() && !plan.Order.IsUnknown() {
		if err := r.setRuleOrder(ctx, ruleID, plan.Order.ValueInt64()); err != nil {
			resp.Diagnostics.AddError("Workflow rule created, but its order could not be set", err.Error())
			saveID()
			return
		}
	}

	if plan.Published.ValueBool() {
		if err := r.setRulePublished(ctx, ruleID, rule.PublishedAt != nil, true); err != nil {
			resp.Diagnostics.AddError("Workflow rule created, but could not be published", err.Error())
			saveID()
			return
		}
	}

	var state workflowRuleModel
	found, readDiags := r.readRule(ctx, ruleID, &state)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		saveID()
		return
	}
	if !found {
		resp.Diagnostics.AddError("Workflow rule disappeared immediately after creation",
			"Plain reported the rule was created but it could not be read back.")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workflowRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workflowRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var fresh workflowRuleModel
	found, readDiags := r.readRule(ctx, state.ID.ValueString(), &fresh)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !found {
		tflog.Info(ctx, "workflow rule no longer exists; removing from state", map[string]any{
			"id": state.ID.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &fresh)...)
}

func (r *workflowRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workflowRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ruleID := state.ID.ValueString()

	// Omitted fields are left unchanged, so send only what actually differs.
	input := UpdateWorkflowRuleInput{WorkflowRuleId: ruleID}
	changed := false

	if !plan.Name.Equal(state.Name) {
		input.Name = &StringInput{Value: plan.Name.ValueString()}
		changed = true
	}
	if !plan.Payload.Equal(state.Payload) {
		input.Payload = &StringInput{Value: plan.Payload.ValueString()}
		changed = true
	}
	if !plan.Order.IsUnknown() && !plan.Order.Equal(state.Order) {
		input.Order = &IntInput{Value: int(plan.Order.ValueInt64())}
		changed = true
	}

	if changed {
		updated, err := UpdateWorkflowRule(ctx, r.client, &input)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update workflow rule", err.Error())
			return
		}
		if updated.UpdateWorkflowRule.Error != nil {
			resp.Diagnostics.Append(mutationDiags("Unable to update workflow rule",
				updated.UpdateWorkflowRule.Error, workflowRuleAttributes)...)
			return
		}
	}

	// Publishing is a separate mutation, and unlike a workflow it cannot be
	// folded into the update. Do it last so it reflects the finished payload.
	if want, have := plan.Published.ValueBool(), state.Published.ValueBool(); want != have {
		if err := r.setRulePublished(ctx, ruleID, have, want); err != nil {
			resp.Diagnostics.AddError("Unable to change the published state of the workflow rule", err.Error())
			return
		}
	}

	var fresh workflowRuleModel
	found, readDiags := r.readRule(ctx, ruleID, &fresh)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Workflow rule disappeared during update",
			"The rule could not be read back after being updated.")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &fresh)...)
}

func (r *workflowRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workflowRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleted, err := DeleteWorkflowRule(ctx, r.client, &DeleteWorkflowRuleInput{
		WorkflowRuleId: state.ID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to delete workflow rule", err.Error())
		return
	}
	if deleted.DeleteWorkflowRule.Error != nil {
		resp.Diagnostics.Append(mutationDiags("Unable to delete workflow rule",
			deleted.DeleteWorkflowRule.Error, workflowRuleAttributes)...)
	}
}

// setRulePublished drives the rule to the requested published state.
//
// Plain exposes this as toggleWorkflowRulePublished — a flip, not an assignment,
// with no isPublished field on UpdateWorkflowRuleInput. A blind toggle would
// invert the wrong way whenever Terraform's idea of the current state is stale,
// so this checks first and verifies the result rather than assuming the flip
// landed where we wanted.
func (r *workflowRuleResource) setRulePublished(ctx context.Context, ruleID string, have, want bool) error {
	if have == want {
		return nil
	}

	resp, err := ToggleWorkflowRulePublished(ctx, r.client, &ToggleWorkflowRulePublishedInput{
		WorkflowRuleId: ruleID,
	})
	if err != nil {
		return err
	}
	if resp.ToggleWorkflowRulePublished.Error != nil {
		return resp.ToggleWorkflowRulePublished.Error
	}

	if got := resp.ToggleWorkflowRulePublished.WorkflowRule.PublishedAt != nil; got != want {
		// Someone changed the rule out from under us between plan and apply.
		// Report it rather than toggling again and racing them.
		return fmt.Errorf(
			"toggling published on %s left it published=%t, wanted published=%t; "+
				"the rule was most likely changed outside Terraform. Run terraform refresh and apply again",
			ruleID, got, want)
	}

	return nil
}

func (r *workflowRuleResource) setRuleOrder(ctx context.Context, ruleID string, order int64) error {
	resp, err := UpdateWorkflowRule(ctx, r.client, &UpdateWorkflowRuleInput{
		WorkflowRuleId: ruleID,
		Order:          &IntInput{Value: int(order)},
	})
	if err != nil {
		return err
	}
	if resp.UpdateWorkflowRule.Error != nil {
		return resp.UpdateWorkflowRule.Error
	}

	return nil
}

// readRule fetches the rule and maps it onto the model.
func (r *workflowRuleResource) readRule(ctx context.Context, ruleID string, model *workflowRuleModel) (found bool, diags diag.Diagnostics) {
	resp, err := GetWorkflowRule(ctx, r.client, ruleID)
	if err != nil {
		diags.AddError("Unable to read workflow rule", err.Error())
		return false, diags
	}

	rule := resp.WorkflowRule
	if rule == nil {
		return false, diags
	}

	model.ID = types.StringValue(rule.Id)
	model.Name = types.StringValue(rule.Name)
	model.Payload = jsontypes.NewNormalizedValue(rule.Payload)
	model.Published = types.BoolValue(rule.PublishedAt != nil)
	model.Order = types.Int64Value(int64(rule.Order))

	return true, diags
}
