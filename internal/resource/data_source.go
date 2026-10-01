// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/client"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/data"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"
)

var _ datasource.DataSource = DataSource{}

type DataSource struct {
	Name           string
	InternalSchema schema.Schema
	Client         client.Client

	// FailOnRead and FailOnceDir are the managed resource's injection switches,
	// carried here so a data-source read can be made to fail on demand. The
	// strike key uses its own operation name, so failing a data read of an id
	// never consumes the one-shot a managed read of the same id would use.
	FailOnRead  []string
	FailOnceDir string

	// DeferOnRead lists ids whose read is deferred rather than performed, the
	// data-source analog of the managed resource's DeferChanges, and
	// DeferUntilReloadDir clears it on a plugin restart exactly as it clears
	// that one. See Read.
	DeferOnRead         []string
	DeferUntilReloadDir string
}

func (d DataSource) Metadata(ctx context.Context, request datasource.MetadataRequest, response *datasource.MetadataResponse) {
	response.TypeName = d.Name
}

func (d DataSource) Schema(ctx context.Context, request datasource.SchemaRequest, response *datasource.SchemaResponse) {
	var err error
	if response.Schema, err = d.InternalSchema.ToTerraformDataSourceSchema(); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic(fmt.Sprintf("failed to build data source schema for '%s'", d.Name), err.Error()))
	}
}

func (d DataSource) Read(ctx context.Context, request datasource.ReadRequest, response *datasource.ReadResponse) {
	resource := &data.Resource{
		ResourceType: d.Name,
	}

	response.Diagnostics.Append(request.Config.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}

	// The deferral a terraform-plugin-sdk provider configured with unknown
	// values gives a read (the kubernetes provider's data sources, whose
	// cluster address is not known yet): PROVIDER_CONFIG_UNKNOWN, with the
	// state wholly unknown as the framework's own provider-level deferral
	// sends it. The framework checks the client's capability only at
	// Configure, so the check is made here: a client that does not allow
	// deferrals fails the read, as a resource's deferral does, instead of
	// receiving an answer it never asked to handle.
	if deferralFires(d.DeferOnRead, d.DeferUntilReloadDir, "read-data", resource.GetId()) {
		if !request.ClientCapabilities.DeferralAllowed {
			response.Diagnostics.AddAttributeError(path.Root("id"), "Invalid data source deferral",
				fmt.Sprintf("The data source with id=%q is marked \"should be deferred\" via defer_on_read, but the client does not support deferrals.", resource.GetId()))
			return
		}
		response.State.Raw = tftypes.NewValue(request.Config.Schema.Type().TerraformType(ctx), tftypes.UnknownValue)
		response.Deferred = &datasource.Deferred{Reason: datasource.DeferredReasonProviderConfigUnknown}
		return
	}

	if forcedFailure(d.FailOnRead, d.FailOnceDir, "read-data", resource.GetId()) {
		response.Diagnostics.AddError("failed to read data source", "forced failure")
		return
	}

	data, err := d.Client.ReadDataSource(ctx, resource.GetId())
	if err != nil {
		response.Diagnostics.AddError("failed to read data source", err.Error())
		return
	}

	if data == nil {
		response.Diagnostics.AddError(
			"target data source does not exist",
			fmt.Sprintf("data source at %s could not be found in data directory", resource.GetId()))
	}

	typ := request.Config.Schema.Type().TerraformType(ctx)
	response.Diagnostics.Append(response.State.Set(ctx, data.WithType(typ.(tftypes.Object)))...)
}
