package plain

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = (*workflowResource)(nil)
	_ resource.ResourceWithConfigure      = (*workflowResource)(nil)
	_ resource.ResourceWithImportState    = (*workflowResource)(nil)
	_ resource.ResourceWithValidateConfig = (*workflowResource)(nil)
)

func NewWorkflowResource() resource.Resource { return &workflowResource{} }

type workflowResource struct {
	client *Client
}

// workflowModel mirrors the resource schema.
//
// Steps are a map keyed by a practitioner-chosen local name rather than a list.
// Plain joins steps by server-assigned ID, which does not exist until after
// create, so config cannot reference IDs. The map key is the stable local handle
// that `start_step`, `next`, `on_true` and `on_false` refer to; the provider
// resolves keys to Plain IDs on write and back to keys on read. A map also makes
// key uniqueness structural and reordering a non-event.
type workflowModel struct {
	ID        types.String         `tfsdk:"id"`
	Name      types.String         `tfsdk:"name"`
	Trigger   jsontypes.Normalized `tfsdk:"trigger"`
	Published types.Bool           `tfsdk:"published"`
	Order     types.Int64          `tfsdk:"order"`
	StartStep types.String         `tfsdk:"start_step"`
	Steps     types.Map            `tfsdk:"steps"`
}

type stepModel struct {
	ID        types.String         `tfsdk:"id"`
	Type      types.String         `tfsdk:"type"`
	Name      types.String         `tfsdk:"name"`
	Payload   jsontypes.Normalized `tfsdk:"payload"`
	Next      types.String         `tfsdk:"next"`
	OnTrue    types.String         `tfsdk:"on_true"`
	OnFalse   types.String         `tfsdk:"on_false"`
	PositionX types.Float64        `tfsdk:"position_x"`
	PositionY types.Float64        `tfsdk:"position_y"`
}

// stepAttrTypes must match the schema below; used when re-building the map.
func stepAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"id":         types.StringType,
		"type":       types.StringType,
		"name":       types.StringType,
		"payload":    jsontypes.NormalizedType{},
		"next":       types.StringType,
		"on_true":    types.StringType,
		"on_false":   types.StringType,
		"position_x": types.Float64Type,
		"position_y": types.Float64Type,
	}
}

func (r *workflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow"
}

func (r *workflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Plain workflow: a trigger plus a graph of condition, action and wait steps.\n\n" +
			"The workflow owns its steps. Steps are declared in the `steps` map and joined by map key " +
			"via `start_step`, `next`, `on_true` and `on_false` — never by Plain's server-assigned step IDs, " +
			"which do not exist until after the workflow is created.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Plain's ID for the workflow.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for the workflow.",
				Required:            true,
			},
			"trigger": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "JSON-encoded trigger configuration, shaped `{\"type\": ...}`:\n\n" +
					"- `{\"type\":\"manual\"}` — runs only when triggered explicitly.\n" +
					"- `{\"type\":\"events\",\"events\":[...]}` — runs on domain events such as " +
					"`thread.thread_created` or `thread.thread_labels_changed`. At least one event.\n" +
					"- `{\"type\":\"schedule\",\"cron\":\"...\"}` — runs on a cron schedule, evaluated in UTC.\n\n" +
					"Build this with `jsonencode()`. Compared semantically, so formatting and key order do not cause drift.",
				Required: true,
			},
			"published": schema.BoolAttribute{
				MarkdownDescription: "Whether the workflow is live. Unpublished workflows are inactive drafts. " +
					"Defaults to `false`.\n\n" +
					"Plain forbids restructuring a published workflow's steps, so when the step graph changes " +
					"the provider unpublishes, applies the change, and republishes automatically.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"order": schema.Int64Attribute{
				MarkdownDescription: "Display order relative to other workflows. Lower values appear first.",
				Optional:            true,
				Computed:            true,
			},
			"start_step": schema.StringAttribute{
				MarkdownDescription: "Key of the step to execute first. Must be a key in `steps`. " +
					"Required whenever `steps` is non-empty.",
				Optional: true,
			},
			"steps": schema.MapNestedAttribute{
				MarkdownDescription: "The workflow's steps, keyed by a local name you choose. Keys are used " +
					"only within this resource to wire the graph together; Plain never sees them.",
				Optional: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Plain's ID for this step.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "One of `CONDITION`, `ACTION`, or `WAIT`.\n\n" +
								"`CONDITION` steps branch via `on_true`/`on_false`; `ACTION` and `WAIT` steps " +
								"continue via `next`.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("CONDITION", "ACTION", "WAIT"),
							},
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Optional display name for the step.",
							Optional:            true,
						},
						"payload": schema.StringAttribute{
							CustomType: jsontypes.NormalizedType{},
							MarkdownDescription: "JSON-encoded step configuration. Always an object with " +
								"`version: 1` and a `type` discriminator.\n\n" +
								"- `CONDITION` — a leaf check (`contains_label`, `thread_field_equals`, " +
								"`priority_equals`, `assigned_to`, ...) or a combinator (`and`, `or`, `not`, `else_if`).\n" +
								"- `ACTION` — e.g. `apply_labels`, `set_priority`, `set_status`, `assign_to_user`, " +
								"`send_message`, `send_http_request`, `snooze_thread`.\n" +
								"- `WAIT` — `{\"duration\": <seconds>, \"cancelCondition\": <condition>}`.\n\n" +
								"~> Payloads embed workspace-specific IDs (label types, users, tiers). Build them " +
								"from references to other resources or data sources — a hard-coded ID from another " +
								"workspace does not error, the step simply does nothing.",
							Required: true,
						},
						"next": schema.StringAttribute{
							MarkdownDescription: "For `ACTION` and `WAIT` steps: key of the next step. " +
								"Omit to end the workflow here. Must not be set on `CONDITION` steps.",
							Optional: true,
						},
						"on_true": schema.StringAttribute{
							MarkdownDescription: "For `CONDITION` steps: key of the step to run when the " +
								"condition matches. Omit to end this branch. Must not be set on other step types.",
							Optional: true,
						},
						"on_false": schema.StringAttribute{
							MarkdownDescription: "For `CONDITION` steps: key of the step to run when the " +
								"condition does not match. Omit to end this branch. Must not be set on other step types.",
							Optional: true,
						},
						"position_x": schema.Float64Attribute{
							MarkdownDescription: "X position on Plain's workflow canvas. Cosmetic; defaults to `0`.",
							Optional:            true,
							Computed:            true,
							Default:             float64default.StaticFloat64(0),
						},
						"position_y": schema.Float64Attribute{
							MarkdownDescription: "Y position on Plain's workflow canvas. Cosmetic; defaults to `0`.",
							Optional:            true,
							Computed:            true,
							Default:             float64default.StaticFloat64(0),
						},
					},
				},
			},
		},
	}
}

func (r *workflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *workflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)

	if !strings.HasPrefix(id, workflowIDPrefix) {
		detail := fmt.Sprintf(
			"Import a workflow by its Plain ID, which begins with %q — for example "+
				"wf_01HXXXXXXXXXXXXXXXXXXXXXXX. Got %q.",
			workflowIDPrefix, req.ID)

		// The most likely wrong paste is a step ID, so name that case.
		if strings.HasPrefix(id, stepIDPrefix) {
			detail += fmt.Sprintf(
				"\n\nThat looks like a workflow step ID (%q). Steps are not separately "+
					"importable — they belong to the workflow that owns them, so import "+
					"that workflow instead.", stepIDPrefix)
		}

		resp.Diagnostics.AddError("Invalid workflow import ID", detail)
		return
	}

	// Plain IDs are prefixed and globally unique, so the bare ID is enough.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("id"), id)...)
	tflog.Info(ctx, "importing workflow; step keys will be set to Plain step IDs", map[string]any{"id": id})
}
