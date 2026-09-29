package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/data"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"
)

var _ action.Action = Action{}
var _ action.ActionWithValidateConfig = Action{}

type Action struct {
	Name           string
	InternalSchema schema.Schema

	// FailOnInvoke lists `string` config values that, when matched by the invoked
	// action's `string` attribute, make the invocation fail — the action analog
	// of FailOnCreate (which keys off a resource id), for exercising
	// action_trigger on_failure behavior. Actions have no `id` (the simple
	// resource schema forbids one), so the freely-settable `string` attribute is
	// the selector.
	FailOnInvoke []string

	// WarnOnInvoke lists `string` config values that, when matched, make the
	// invocation succeed with a warning diagnostic beside its progress event —
	// for exercising how a client carries a provider's warnings (a real
	// provider warns on a pass it cannot fully vouch for). Checked after
	// FailOnInvoke, so a value in both lists fails and still warns.
	WarnOnInvoke []string

	// FailOnceDir, when set, makes FailOnInvoke one-shot per config `string`,
	// exactly as it does for the resource injections: the first triggered
	// invocation fails and records a strike, later ones succeed.
	FailOnceDir string
}

func (a Action) Metadata(ctx context.Context, request action.MetadataRequest, response *action.MetadataResponse) {
	response.TypeName = a.Name
}

func (a Action) Schema(ctx context.Context, request action.SchemaRequest, response *action.SchemaResponse) {
	var err error
	if response.Schema, err = a.InternalSchema.ToTerraformActionSchema(); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic(fmt.Sprintf("failed to build resource schema for '%s'", a.Name), err.Error()))
	}
}

// ValidateConfig checks the files the schema's MustExist attributes name; for
// a schema without any it does nothing.
func (a Action) ValidateConfig(ctx context.Context, request action.ValidateConfigRequest, response *action.ValidateConfigResponse) {
	validateMustExist(ctx, a.InternalSchema, request.Config, &response.Diagnostics)
}

func (a Action) Invoke(ctx context.Context, request action.InvokeRequest, response *action.InvokeResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.Config.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}

	var selector string
	v, selected := resource.Values["string"]
	if selected && v.String != nil {
		selector = *v.String
	} else {
		selected = false
	}

	// The warning is added first so that a value in both lists reports it
	// beside the error: a client must carry a provider's warnings whether or
	// not the invocation failed.
	if selected && slices.Contains(a.WarnOnInvoke, selector) {
		response.Diagnostics.AddWarning(
			"action invocation warned",
			fmt.Sprintf("the action with string=%q is configured to warn via warn_on_invoke", selector))
	}

	if selected && forcedFailure(a.FailOnInvoke, a.FailOnceDir, "invoke", selector) {
		response.Diagnostics.AddError(
			"action invocation failed",
			fmt.Sprintf("the action with string=%q is configured to fail via fail_on_invoke", selector))
		return
	}

	msg, err := json.Marshal(resource)
	if err != nil {
		response.Diagnostics.AddError("failed to marshal action data", err.Error())
		return
	}
	response.SendProgress(action.InvokeProgressEvent{
		Message: string(msg),
	})
}
