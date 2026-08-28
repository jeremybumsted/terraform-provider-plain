package plain

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

func (r *workflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	steps, diags := stepsFromMap(ctx, plan.Steps)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Always create as a draft. Steps are added next, and Plain refuses to
	// restructure a published workflow — building it unpublished sidesteps that
	// entirely, then we publish at the end if asked.
	created, err := CreateWorkflow(ctx, r.client, &CreateWorkflowInput{
		Name:    plan.Name.ValueString(),
		Trigger: plan.Trigger.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create workflow", err.Error())
		return
	}
	if created.CreateWorkflow.Error != nil {
		resp.Diagnostics.Append(mutationDiags("Unable to create workflow", created.CreateWorkflow.Error, workflowAttributes)...)
		return
	}

	workflowID := created.CreateWorkflow.Workflow.Id

	// From here on the workflow exists. Any later failure must still write the ID
	// to state, or Terraform loses track of it and the next apply orphans it.
	saveID := func() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("id"), workflowID)...)
	}

	resolved := map[string]string{}
	if len(steps) > 0 {
		var err error
		resolved, err = r.syncSteps(ctx, workflowID, steps, plan.StartStep, nil)
		if err != nil {
			resp.Diagnostics.Append(mutationDiagsFor("Workflow created, but its steps could not be applied", err)...)
			saveID()
			return
		}
	}

	if plan.Published.ValueBool() {
		if err := r.setPublished(ctx, workflowID, true); err != nil {
			resp.Diagnostics.Append(mutationDiagsFor("Workflow created, but could not be published", err)...)
			saveID()
			return
		}
	}

	if !plan.Order.IsNull() && !plan.Order.IsUnknown() {
		if err := r.setOrder(ctx, workflowID, plan.Order.ValueInt64()); err != nil {
			resp.Diagnostics.Append(mutationDiagsFor("Workflow created, but its order could not be set", err)...)
			saveID()
			return
		}
	}

	// Read back rather than assuming: Plain assigns step IDs and order. The read
	// needs the ID-to-key map syncSteps just resolved, otherwise steps come back
	// keyed by Plain ID and the applied state will not match the plan.
	keys := invert(resolved)
	var state workflowModel
	found, readDiags := r.read(ctx, workflowID, keys, &state)
	state.Steps = matchEmptyStepsShape(plan.Steps, state.Steps)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		saveID()
		return
	}
	if !found {
		resp.Diagnostics.AddError("Workflow disappeared immediately after creation",
			"Plain reported the workflow was created but it could not be read back.")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	priorKeys, diags := priorKeysFromState(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var fresh workflowModel
	found, readDiags := r.read(ctx, state.ID.ValueString(), priorKeys, &fresh)
	fresh.Steps = matchEmptyStepsShape(state.Steps, fresh.Steps)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !found {
		tflog.Info(ctx, "workflow no longer exists; removing from state", map[string]any{
			"id": state.ID.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &fresh)...)
}

func (r *workflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	workflowID := state.ID.ValueString()

	planSteps, diags := stepsFromMap(ctx, plan.Steps)
	resp.Diagnostics.Append(diags...)
	stateSteps, diags := stepsFromMap(ctx, state.Steps)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	knownIDs, diags := knownIDsFromState(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	restructuring := graphChanged(planSteps, stateSteps, plan.StartStep, state.StartStep)
	wasPublished := state.Published.ValueBool()

	// Plain rejects step restructuring on a published workflow. Drop to draft
	// first so the practitioner does not have to stage this across two applies.
	if restructuring && wasPublished {
		tflog.Debug(ctx, "unpublishing workflow to restructure steps", map[string]any{"id": workflowID})
		if err := r.setPublished(ctx, workflowID, false); err != nil {
			resp.Diagnostics.Append(mutationDiagsFor("Unable to unpublish workflow before restructuring it", err)...)
			return
		}
	}

	resolved := knownIDs
	if restructuring || stepsContentChanged(planSteps, stateSteps) {
		synced, err := r.syncSteps(ctx, workflowID, planSteps, plan.StartStep, knownIDs)
		if err != nil {
			resp.Diagnostics.Append(mutationDiagsFor("Unable to update workflow steps", err)...)
			// The workflow may be sitting unpublished. Say so rather than leaving
			// the practitioner to discover their automation is silently off.
			if restructuring && wasPublished {
				resp.Diagnostics.AddWarning(
					"Workflow left unpublished",
					"The workflow was unpublished so its steps could be restructured, and the "+
						"restructure then failed. It is currently an inactive draft. Fix the error "+
						"and apply again to republish it.",
				)
			}
			return
		}
		resolved = synced
	}

	input := UpdateWorkflowInput{WorkflowId: workflowID}
	changed := false

	if !plan.Name.Equal(state.Name) {
		input.Name = &StringInput{Value: plan.Name.ValueString()}
		changed = true
	}
	if !plan.Trigger.Equal(state.Trigger) {
		input.Trigger = &StringInput{Value: plan.Trigger.ValueString()}
		changed = true
	}
	if !plan.Order.IsUnknown() && !plan.Order.Equal(state.Order) {
		input.Order = &IntInput{Value: int(plan.Order.ValueInt64())}
		changed = true
	}

	// Restore the published state last, so it reflects the finished graph.
	wantPublished := plan.Published.ValueBool()
	if wantPublished != wasPublished || (restructuring && wasPublished) {
		input.IsPublished = &BooleanInput{Value: wantPublished}
		changed = true
	}

	if changed {
		updated, err := UpdateWorkflow(ctx, r.client, &input)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update workflow", err.Error())
			return
		}
		if updated.UpdateWorkflow.Error != nil {
			resp.Diagnostics.Append(mutationDiags("Unable to update workflow", updated.UpdateWorkflow.Error, workflowAttributes)...)
			return
		}
	}

	keys := invert(resolved)
	var fresh workflowModel
	found, readDiags := r.read(ctx, workflowID, keys, &fresh)
	fresh.Steps = matchEmptyStepsShape(plan.Steps, fresh.Steps)
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Workflow disappeared during update",
			"The workflow could not be read back after being updated.")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &fresh)...)
}

func (r *workflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleted, err := DeleteWorkflow(ctx, r.client, &DeleteWorkflowInput{
		WorkflowId: state.ID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to delete workflow", err.Error())
		return
	}
	if deleted.DeleteWorkflow.Error != nil {
		// Deleting a workflow that is already gone comes back as HTTP 200 with a
		// payload error whose code is not_found. That is not a failure: the object
		// is already in the state the practitioner asked for, so a delete has to
		// treat it as a success. Otherwise a workflow someone removed in Plain's
		// UI makes `terraform destroy` fail — and specifically
		// `terraform destroy -refresh=false`, where Read never gets the chance to
		// drop it from state first.
		//
		// Only this code is benign. Everything else is still a real error.
		if deleted.DeleteWorkflow.Error.ErrorCode() == "not_found" {
			tflog.Info(ctx, "workflow was already gone in Plain; treating the delete as a success", map[string]any{
				"id": state.ID.ValueString(),
			})
			return
		}

		resp.Diagnostics.Append(mutationDiags("Unable to delete workflow", deleted.DeleteWorkflow.Error, workflowAttributes)...)
	}
}

func (r *workflowResource) setOrder(ctx context.Context, workflowID string, order int64) error {
	resp, err := UpdateWorkflow(ctx, r.client, &UpdateWorkflowInput{
		WorkflowId: workflowID,
		Order:      &IntInput{Value: int(order)},
	})
	if err != nil {
		return err
	}
	if resp.UpdateWorkflow.Error != nil {
		return resp.UpdateWorkflow.Error
	}

	return nil
}

// graphChanged reports whether the shape of the workflow changed — steps added
// or removed, retyped, or rewired. Payload-only edits do not count: they can be
// applied without unpublishing.
func graphChanged(plan, state map[string]stepModel, planStart, stateStart types.String) bool {
	if len(plan) != len(state) {
		return true
	}
	if !planStart.Equal(stateStart) {
		return true
	}

	for key, planStep := range plan {
		stateStep, ok := state[key]
		if !ok {
			return true
		}
		if !planStep.Type.Equal(stateStep.Type) ||
			!planStep.Next.Equal(stateStep.Next) ||
			!planStep.OnTrue.Equal(stateStep.OnTrue) ||
			!planStep.OnFalse.Equal(stateStep.OnFalse) {
			return true
		}
	}

	return false
}

// matchEmptyStepsShape preserves the caller's null-versus-empty distinction for
// a workflow that has no steps.
//
// read cannot make this call itself: it collapses "no steps" to a null map,
// because that is right for a config that omits the attribute entirely. But
// steps is Optional and not Computed, so Terraform requires the applied value
// to match the configured one exactly — a config that writes `steps = {}` and
// gets null back fails the apply with "Provider produced inconsistent result".
// Both spellings mean the same thing to Plain; only Terraform tells them apart.
//
// So the caller, which has the plan or the prior state, restores the shape.
// Anything other than "fresh is null and prior was a known empty map" is left
// alone.
func matchEmptyStepsShape(prior, fresh types.Map) types.Map {
	if !fresh.IsNull() || prior.IsNull() || prior.IsUnknown() {
		return fresh
	}
	if len(prior.Elements()) != 0 {
		return fresh
	}

	return types.MapValueMust(types.ObjectType{AttrTypes: stepAttrTypes()}, map[string]attr.Value{})
}

// stepsContentChanged reports whether anything about the steps needs writing,
// including the cosmetic and payload-only attributes graphChanged ignores.
func stepsContentChanged(plan, state map[string]stepModel) bool {
	if len(plan) != len(state) {
		return true
	}

	for key, planStep := range plan {
		stateStep, ok := state[key]
		if !ok {
			return true
		}
		if !planStep.Payload.Equal(stateStep.Payload) ||
			!planStep.Name.Equal(stateStep.Name) ||
			!planStep.PositionX.Equal(stateStep.PositionX) ||
			!planStep.PositionY.Equal(stateStep.PositionY) {
			return true
		}
	}

	return false
}

func invert(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[v] = k
	}

	return out
}
