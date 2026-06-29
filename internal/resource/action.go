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

func (a Action) Invoke(ctx context.Context, request action.InvokeRequest, response *action.InvokeResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.Config.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}

	if v, ok := resource.Values["string"]; ok && v.String != nil && slices.Contains(a.FailOnInvoke, *v.String) {
		response.Diagnostics.AddError(
			"action invocation failed",
			fmt.Sprintf("the action with string=%q is configured to fail via fail_on_invoke", *v.String))
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
