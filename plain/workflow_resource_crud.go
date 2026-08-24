package plain

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// stepsFromMap decodes the steps attribute into a plain Go map.
func stepsFromMap(ctx context.Context, m types.Map) (map[string]stepModel, diag.Diagnostics) {
	steps := map[string]stepModel{}
	if m.IsNull() || m.IsUnknown() {
		return steps, nil
	}

	diags := m.ElementsAs(ctx, &steps, false)
	return steps, diags
}

// sortedKeys gives the step map a deterministic order, so the same config
// produces the same request every time and diffs against Plain stay readable.
func sortedKeys(steps map[string]stepModel) []string {
	keys := make([]string, 0, len(steps))
	for k := range steps {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

// transitionKeys returns the step keys this step branches to, in the positional
// order Plain expects: [next] for ACTION/WAIT, [onTrue, onFalse] for CONDITION.
func (s stepModel) transitionKeys() []types.String {
	if s.Type.ValueString() == string(WorkflowStepTypeCondition) {
		return []types.String{s.OnTrue, s.OnFalse}
	}

	return []types.String{s.Next}
}

// ValidateConfig checks the step graph is internally consistent before any API
// call. Plain would reject most of this too, but only after a partial apply.
func (r *workflowResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config workflowModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values (references not yet resolved) can't be validated at plan time.
	if config.Steps.IsUnknown() {
		return
	}

	steps, diags := stepsFromMap(ctx, config.Steps)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	stepsPath := path("steps")

	if len(steps) > 0 && config.StartStep.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path("start_step"),
			"start_step is required when steps are defined",
			"A workflow with steps needs an entry point. Set start_step to one of the keys in steps.",
		)
	}

	if !config.StartStep.IsNull() && !config.StartStep.IsUnknown() {
		if _, ok := steps[config.StartStep.ValueString()]; !ok {
			resp.Diagnostics.AddAttributeError(
				path("start_step"),
				"start_step does not match any step",
				fmt.Sprintf("start_step is %q, but steps has no such key. Known keys: %v.",
					config.StartStep.ValueString(), sortedKeys(steps)),
			)
		}
	}

	for _, key := range sortedKeys(steps) {
		step := steps[key]
		stepPath := stepsPath.AtMapKey(key)
		isCondition := step.Type.ValueString() == string(WorkflowStepTypeCondition)

		// The branch attributes are mutually exclusive by step type: mixing them
		// would silently drop transitions when mapped to Plain's positional array.
		if isCondition && !step.Next.IsNull() {
			resp.Diagnostics.AddAttributeError(
				stepPath.AtName("next"),
				"next is not valid on a CONDITION step",
				fmt.Sprintf("Step %q is a CONDITION, which branches via on_true and on_false. Remove next.", key),
			)
		}

		if !isCondition {
			for _, attrName := range []string{"on_true", "on_false"} {
				value := step.OnTrue
				if attrName == "on_false" {
					value = step.OnFalse
				}
				if !value.IsNull() {
					resp.Diagnostics.AddAttributeError(
						stepPath.AtName(attrName),
						fmt.Sprintf("%s is only valid on a CONDITION step", attrName),
						fmt.Sprintf("Step %q is a %s, which continues via next. Remove %s.",
							key, step.Type.ValueString(), attrName),
					)
				}
			}
		}

		// Every transition must name a real step.
		names := []string{"next"}
		if isCondition {
			names = []string{"on_true", "on_false"}
		}
		for i, target := range step.transitionKeys() {
			if target.IsNull() || target.IsUnknown() {
				continue
			}
			if _, ok := steps[target.ValueString()]; !ok {
				resp.Diagnostics.AddAttributeError(
					stepPath.AtName(names[i]),
					"Transition target does not match any step",
					fmt.Sprintf("Step %q points %s at %q, but steps has no such key. Known keys: %v.",
						key, names[i], target.ValueString(), sortedKeys(steps)),
				)
			}
		}
	}
}

// syncSteps reconciles the workflow's entire step graph in one mutation.
//
// Plain joins steps by ID, but Terraform config names them by map key, so keys
// have to be resolved to IDs. Steps already in state keep their ID; new steps
// get one minted here, because bulkUpsertWorkflowSteps honours client-supplied
// IDs. That is what lets the whole graph — including transitions between steps
// that do not exist yet — go up in a single call.
//
// knownIDs maps step key to an existing Plain step ID; nil or missing entries
// mean the step does not exist yet. Returns the resolved key-to-ID map.
func (r *workflowResource) syncSteps(
	ctx context.Context,
	workflowID string,
	steps map[string]stepModel,
	startStep types.String,
	knownIDs map[string]string,
) (map[string]string, error) {
	keys := sortedKeys(steps)

	// A workflow with no steps: clear them, and clear the entry point with them.
	if len(keys) == 0 {
		_, err := r.bulkUpsert(ctx, BulkUpsertWorkflowStepsInput{
			WorkflowId:  workflowID,
			Steps:       []*BulkUpsertWorkflowStepInput{},
			StartStepId: nil,
		})
		return map[string]string{}, err
	}

	// Resolve every key to an ID up front — reusing what state knows, minting
	// the rest — so transitions can be written in the same pass.
	resolved := make(map[string]string, len(keys))
	minted := 0
	for _, key := range keys {
		if id, ok := knownIDs[key]; ok && id != "" {
			resolved[key] = id
			continue
		}
		resolved[key] = newStepID()
		minted++
	}

	if minted > 0 {
		tflog.Debug(ctx, "minted IDs for new workflow steps", map[string]any{
			"workflow_id": workflowID,
			"new_steps":   minted,
			"total_steps": len(keys),
		})
	}

	inputs := make([]*BulkUpsertWorkflowStepInput, 0, len(keys))
	for _, key := range keys {
		id := resolved[key]
		in := stepInput(steps[key], resolved)
		in.StepId = &id
		inputs = append(inputs, in)
	}

	var startID *string
	if !startStep.IsNull() {
		if id, ok := resolved[startStep.ValueString()]; ok {
			startID = &id
		}
	}

	if _, err := r.bulkUpsert(ctx, BulkUpsertWorkflowStepsInput{
		WorkflowId:  workflowID,
		Steps:       inputs,
		StartStepId: startID,
	}); err != nil {
		return nil, err
	}

	return resolved, nil
}

// stepInput builds the API input for one step. Transitions are emitted with the
// arity Plain expects for the step type, with nulls for terminal branches.
func stepInput(step stepModel, resolved map[string]string) *BulkUpsertWorkflowStepInput {
	targets := step.transitionKeys()
	transitions := make([]*string, len(targets))

	for i, target := range targets {
		if target.IsNull() {
			continue
		}
		if id, ok := resolved[target.ValueString()]; ok {
			transitions[i] = &id
		}
	}

	in := &BulkUpsertWorkflowStepInput{
		Type:        WorkflowStepType(step.Type.ValueString()),
		Payload:     step.Payload.ValueString(),
		Transitions: transitions,
	}

	if !step.Name.IsNull() {
		in.Name = step.Name.ValueStringPointer()
	}
	if !step.PositionX.IsNull() && !step.PositionX.IsUnknown() {
		in.PositionX = step.PositionX.ValueFloat64Pointer()
	}
	if !step.PositionY.IsNull() && !step.PositionY.IsUnknown() {
		in.PositionY = step.PositionY.ValueFloat64Pointer()
	}

	return in
}

func (r *workflowResource) bulkUpsert(
	ctx context.Context,
	input BulkUpsertWorkflowStepsInput,
) ([]*BulkUpsertWorkflowStepsBulkUpsertWorkflowStepsBulkUpsertWorkflowStepsOutputResultsBulkUpsertWorkflowStepResultItem, error) {
	resp, err := BulkUpsertWorkflowSteps(ctx, r.client, &input)
	if err != nil {
		return nil, err
	}
	if resp.BulkUpsertWorkflowSteps.Error != nil {
		return nil, resp.BulkUpsertWorkflowSteps.Error
	}

	return resp.BulkUpsertWorkflowSteps.Results, nil
}

// setPublished toggles the published state on its own. Plain refuses to
// restructure a published workflow, so callers unpublish, restructure, and
// republish rather than doing it in one update.
func (r *workflowResource) setPublished(ctx context.Context, workflowID string, published bool) error {
	resp, err := UpdateWorkflow(ctx, r.client, &UpdateWorkflowInput{
		WorkflowId:  workflowID,
		IsPublished: &BooleanInput{Value: published},
	})
	if err != nil {
		return err
	}
	if resp.UpdateWorkflow.Error != nil {
		return resp.UpdateWorkflow.Error
	}

	return nil
}

// read fetches the workflow and maps it onto the model. priorKeys maps Plain
// step IDs back to the local keys the config used; steps not found there (a
// fresh import, or a step added outside Terraform) fall back to keying by ID.
func (r *workflowResource) read(
	ctx context.Context,
	workflowID string,
	priorKeys map[string]string,
	model *workflowModel,
) (found bool, diags diag.Diagnostics) {
	resp, err := GetWorkflow(ctx, r.client, workflowID)
	if err != nil {
		diags.AddError("Unable to read workflow", err.Error())
		return false, diags
	}

	wf := resp.Workflow
	if wf == nil {
		return false, diags
	}

	model.ID = types.StringValue(wf.Id)
	model.Name = types.StringValue(wf.Name)
	model.Trigger = jsontypes.NewNormalizedValue(wf.Trigger)
	model.Published = types.BoolValue(wf.PublishedAt != nil)
	model.Order = types.Int64Value(int64(wf.Order))

	// Map IDs to local keys first: transitions reference IDs and must be
	// rendered back as keys.
	keyByID := map[string]string{}
	for _, step := range wf.Steps {
		if key, ok := priorKeys[step.Id]; ok {
			keyByID[step.Id] = key
			continue
		}
		keyByID[step.Id] = step.Id
	}

	keyFor := func(id *string) types.String {
		if id == nil {
			return types.StringNull()
		}
		if key, ok := keyByID[*id]; ok {
			return types.StringValue(key)
		}
		// A transition pointing outside this workflow's steps: surface the raw
		// ID rather than dropping it, so the drift is visible.
		return types.StringValue(*id)
	}

	elements := map[string]attr.Value{}
	for _, step := range wf.Steps {
		s := stepModel{
			ID:        types.StringValue(step.Id),
			Type:      types.StringValue(string(step.Type)),
			Name:      types.StringPointerValue(step.Name),
			Payload:   jsontypes.NewNormalizedValue(step.Payload),
			Next:      types.StringNull(),
			OnTrue:    types.StringNull(),
			OnFalse:   types.StringNull(),
			PositionX: types.Float64Value(step.PositionX),
			PositionY: types.Float64Value(step.PositionY),
		}

		if step.Type == WorkflowStepTypeCondition {
			if len(step.Transitions) > 0 {
				s.OnTrue = keyFor(step.Transitions[0])
			}
			if len(step.Transitions) > 1 {
				s.OnFalse = keyFor(step.Transitions[1])
			}
		} else if len(step.Transitions) > 0 {
			s.Next = keyFor(step.Transitions[0])
		}

		obj, objDiags := types.ObjectValueFrom(ctx, stepAttrTypes(), s)
		diags.Append(objDiags...)
		if diags.HasError() {
			return true, diags
		}

		elements[keyByID[step.Id]] = obj
	}

	if len(elements) == 0 {
		model.Steps = types.MapNull(types.ObjectType{AttrTypes: stepAttrTypes()})
	} else {
		stepMap, mapDiags := types.MapValue(types.ObjectType{AttrTypes: stepAttrTypes()}, elements)
		diags.Append(mapDiags...)
		model.Steps = stepMap
	}

	model.StartStep = keyFor(wf.StartStepId)

	return true, diags
}

// priorKeysFromState builds the Plain-step-ID to local-key map from state.
func priorKeysFromState(ctx context.Context, state workflowModel) (map[string]string, diag.Diagnostics) {
	steps, diags := stepsFromMap(ctx, state.Steps)
	if diags.HasError() {
		return nil, diags
	}

	keys := map[string]string{}
	for key, step := range steps {
		if !step.ID.IsNull() && !step.ID.IsUnknown() {
			keys[step.ID.ValueString()] = key
		}
	}

	return keys, diags
}

// knownIDsFromState maps local key to Plain step ID — the inverse of priorKeys.
func knownIDsFromState(ctx context.Context, state workflowModel) (map[string]string, diag.Diagnostics) {
	steps, diags := stepsFromMap(ctx, state.Steps)
	if diags.HasError() {
		return nil, diags
	}

	ids := map[string]string{}
	for key, step := range steps {
		if !step.ID.IsNull() && !step.ID.IsUnknown() {
			ids[key] = step.ID.ValueString()
		}
	}

	return ids, diags
}
