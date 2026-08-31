// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	provider_schema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	tfresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/client"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/resource"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema/complex"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema/dynamic"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema/simple"
)

var _ provider.Provider = &tfcoremockProvider{}
var _ provider.ProviderWithActions = &tfcoremockProvider{}
var _ provider.ProviderWithListResources = &tfcoremockProvider{}
var _ provider.ProviderWithEphemeralResources = &tfcoremockProvider{}

const (
	description = `The 'tfcoremock' provider is intended to aid with testing the Terraform core libraries and the Terraform CLI. This provider should allow users to define all possible Terraform configurations and run them through the Terraform core platform.

The provider supplies two static resources:

- 'tfcoremock_simple_resource'
- 'tfcoremock_complex_resource'
 
Users can then define additional dynamic resources by supplying a 'dynamic_resources.json' file alongside their root Terraform configuration. These dynamic resources can be used to model any Terraform configuration not covered by the provided static resources.

By default, all resources created by the provider are then converted into a human-readable JSON format and written out to the resource directory. This behaviour can be disabled by turning on the 'use_only_state' flag in the provider schema (this is useful when running the provider in a Terraform Cloud environment). The resource directory defaults to 'terraform.resource'.

All resources supplied by the provider (including the simple and complex resource as well as any dynamic resources) are duplicated into data sources. The data sources should be supplied in the JSON format that resources are written into. The provider looks into the data directory, which defaults to 'terraform.data'.

All resources (and data sources) supplied by the provider have an 'id' attribute that is generated if not set by the configuration. Dynamic resources cannot define an 'id' attribute as the provider will create one for them. The 'id' attribute is used as the name of the human-readable JSON files held in the resource and data directories.

Additionally, all resources are available to be queried via 'list' blocks. For now only the 'id' attribute is supported as a field to retrieve a specific instance. It is optional, so all resources of the specified type will be returned if the field is left blank.

The provider also supports actions (introduced in Terraform v1.14). All resources (both static and dynamic) are made available as action blocks, that can be plugged into any Terraform configuration. Unlike resources and data sources, actions have no 'id' associated with them as they are not written to disk.`

	markdownDescription = `The ''tfcoremock'' provider is intended to aid with testing the Terraform core libraries and the Terraform CLI. This provider should allow users to define all possible Terraform configurations and run them through the Terraform core platform.

The provider supplies two static resources:

- ''tfcoremock_simple_resource''
- ''tfcoremock_complex_resource''
 
Users can then define additional dynamic resources by supplying a ''dynamic_resources.json'' file alongside their root Terraform configuration. These dynamic resources can be used to model any Terraform configuration not covered by the provided static resources.

By default, all resources created by the provider are then converted into a human-readable JSON format and written out to the resource directory. This behaviour can be disabled by turning on the ''use_only_state'' flag in the provider schema (this is useful when running the provider in a Terraform Cloud environment). The resource directory defaults to ''terraform.resource''.

All resources supplied by the provider (including the simple and complex resource as well as any dynamic resources) are duplicated into data sources. The data sources should be supplied in the JSON format that resources are written into. The provider looks into the data directory, which defaults to ''terraform.data''.

All resources (and data sources) supplied by the provider have an ''id'' attribute that is generated if not set by the configuration. Dynamic resources cannot define an ''id'' attribute as the provider will create one for them. The ''id'' attribute is used as the name of the human-readable JSON files held in the resource and data directories.

Additionally, all resources are available to be queried via ''list'' blocks. For now only the ''id'' attribute is supported as a field to retrieve a specific instance. It is optional, so all resources of the specified type will be returned if the field is left blank.

The provider also supports actions (introduced in Terraform v1.14). All resources (both static and dynamic) are made available as action blocks, that can be plugged into any Terraform configuration. Unlike resources and data sources, actions have no ''id'' associated with them as they are not written to disk.`

	dynamicResourcesPathEnvVarName = "TFCOREMOCK_DYNAMIC_RESOURCES_FILE"

	// identitySchemaVersionEnvVarName selects the identity schema version the
	// provider declares. It is an environment variable rather than a config
	// attribute because GetResourceIdentitySchemas is an unconfigured RPC:
	// clients fetch schemas before ConfigureProvider, so a config attribute
	// would not be set in time.
	identitySchemaVersionEnvVarName = "TFCOREMOCK_IDENTITY_SCHEMA_VERSION"
)

type tfcoremockProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string

	// reader will read the dynamic resource definitions in the GetResource and
	// GetDataSources functions.
	reader dynamic.Reader

	// client is provided to the actual resources so that their states can be
	// recorded and written to a backend other than the terraform state.
	client client.Client

	failOnCreate []string
	failOnUpdate []string
	failOnRead   []string
	failOnDelete []string
	failOnInvoke []string
	failOnOpen   []string
	deferChanges []string

	// failOnceDirectory is where one-shot failure strikes are recorded when
	// fail_once is set; empty when the injections fail every call. Like
	// ephemeralAuditDirectory it is derived from the resource directory — a
	// strike must be observable from a second provider process, so it lives
	// beside the store rather than in memory.
	failOnceDirectory string

	// ephemeralAuditDirectory is where the ephemeral resource records its
	// lifecycle. It is derived from the resource directory rather than
	// configured separately, and is empty under use_only_state — that mode has
	// no resource directory, so there is nowhere to record.
	ephemeralAuditDirectory string

	strictIdentity bool

	strictPrivateState bool

	// identitySchemaVersion is read from the environment at construction; see
	// identitySchemaVersionEnvVarName.
	identitySchemaVersion int64
}

type providerData struct {
	ResourceDirectory types.String `tfsdk:"resource_directory"`
	DataDirectory     types.String `tfsdk:"data_directory"`
	UseOnlyState      types.Bool   `tfsdk:"use_only_state"`

	FailOnce     types.Bool `tfsdk:"fail_once"`
	FailOnCreate types.List `tfsdk:"fail_on_create"`
	FailOnUpdate types.List `tfsdk:"fail_on_update"`
	FailOnRead   types.List `tfsdk:"fail_on_read"`
	FailOnDelete types.List `tfsdk:"fail_on_delete"`
	FailOnInvoke types.List `tfsdk:"fail_on_invoke"`
	FailOnOpen   types.List `tfsdk:"fail_on_open"`

	DeferChanges types.List `tfsdk:"defer_changes"`

	// StrictIdentity turns the identity a client sends on the wire into an
	// observable: when true, resources fail the call if the prior identity does
	// not match what the protocol requires. Without it a client's identity
	// bookkeeping is invisible from the outside, because this provider derives
	// every identity it reports from its own state rather than echoing the one
	// it was given.
	StrictIdentity types.Bool `tfsdk:"strict_identity"`

	// StrictPrivateState turns a client's private-state bookkeeping into an
	// observable: when true, resources record a private-state marker on create
	// (and import) and fail any later call that does not carry it back exactly.
	// Without it the round-trip is invisible from the outside — the framework
	// pre-populates each response's private state from the request, so even a
	// client that dropped the blob entirely would look correct.
	StrictPrivateState types.Bool `tfsdk:"strict_private_state"`

	// DeferOnUnknownConfig makes this provider ACCEPT a configuration carrying
	// unknown values and then defer every resource served by it, with reason
	// PROVIDER_CONFIG_UNKNOWN — which is what terraform-plugin-sdk providers
	// (kubernetes, helm) do. Without it this provider takes the other route: an
	// unknown in one of the list-typed attributes fails Configure outright, so
	// the instance is never configured at all. Both are legal, they reach a
	// client through completely different paths, and a client that handles only
	// one of them is broken against half the ecosystem.
	DeferOnUnknownConfig types.Bool `tfsdk:"defer_on_unknown_config"`

	// Cluster is a block-typed config attribute (NestingList, encoded as a list
	// of objects) that exercises a downstream consumer's schema-aware
	// object->list-of-one coercion for a nested provider-config block — the
	// shape real providers such as helm ('kubernetes {}') use. It has no effect
	// on resource behavior; when a known 'host' is supplied with 'fail = true'
	// the provider fails Configure with a diagnostic echoing the host, so a
	// caller can prove the block content reached the provider. ('connection' is
	// a reserved root block name in Terraform, hence 'cluster'.)
	Cluster types.List `tfsdk:"cluster"`
}

// clusterModel is the object shape of a single 'cluster' block element.
type clusterModel struct {
	Host types.String `tfsdk:"host"`
	Fail types.Bool   `tfsdk:"fail"`
}

func (m *tfcoremockProvider) Configure(ctx context.Context, request provider.ConfigureRequest, response *provider.ConfigureResponse) {
	var data providerData
	response.Diagnostics.Append(request.Config.Get(ctx, &data)...)
	if response.Diagnostics.HasError() {
		return
	}

	// Take the configuration as it stands and defer everything served by it,
	// rather than refusing it. Checked before anything reads the config, since
	// the point is that the unknown values are never inspected.
	if data.DeferOnUnknownConfig.ValueBool() && !request.Config.Raw.IsFullyKnown() {
		if !request.ClientCapabilities.DeferralAllowed {
			response.Diagnostics.AddError(
				"Cannot defer on unknown configuration",
				"defer_on_unknown_config is set and the configuration carries unknown values, "+
					"but this client did not announce deferral support.")
			return
		}
		response.Deferred = &provider.Deferred{Reason: provider.DeferredReasonProviderConfigUnknown}
		return
	}

	if data.UseOnlyState.ValueBool() {
		directory := "terraform.data"
		if !data.DataDirectory.IsNull() {
			directory = data.DataDirectory.ValueString()
		}

		m.client = client.State{
			DataDirectory: directory,
		}
		m.ephemeralAuditDirectory = ""
	} else {
		dataDirectory := "terraform.data"
		resourceDirectory := "terraform.resource"

		if !data.DataDirectory.IsNull() {
			dataDirectory = data.DataDirectory.ValueString()
		}

		if !data.ResourceDirectory.IsNull() {
			resourceDirectory = data.ResourceDirectory.ValueString()
		}

		m.client = client.Local{
			ResourceDirectory: resourceDirectory,
			DataDirectory:     dataDirectory,
		}
		// A subdirectory rather than the resource directory itself: the local
		// client treats every *.json file directly inside it as a managed
		// resource, and a lifecycle record is not one.
		m.ephemeralAuditDirectory = filepath.Join(resourceDirectory, "ephemeral")
	}

	if data.FailOnce.ValueBool() {
		if data.UseOnlyState.ValueBool() {
			// The state client stores nothing, so a strike could only live in
			// this process's memory — where a second plugin launch (or another
			// tool over the same store) would never see it, and the "fails
			// once" promise would silently become "fails once per process".
			response.Diagnostics.AddError(
				"fail_once requires a persistent store",
				"fail_once records each strike beside the resource directory so a later call — "+
					"from any process — sees it; with use_only_state there is nowhere to record. "+
					"Unset use_only_state (and optionally set resource_directory) to use fail_once.")
			return
		}
		// Same placement rule as the ephemeral audit directory: a subdirectory,
		// because a strike record is not a managed resource.
		m.failOnceDirectory = filepath.Join(m.client.(client.Local).ResourceDirectory, "fail-once")
	}

	failOnDelete, failOnDeleteDiags := parseStringList(ctx, data.FailOnDelete, "fail_on_delete")
	failOnCreate, failOnCreateDiags := parseStringList(ctx, data.FailOnCreate, "fail_on_create")
	failOnRead, failOnReadDiags := parseStringList(ctx, data.FailOnRead, "fail_on_read")
	failOnUpdate, failOnUpdateDiags := parseStringList(ctx, data.FailOnUpdate, "fail_on_update")
	failOnInvoke, failOnInvokeDiags := parseStringList(ctx, data.FailOnInvoke, "fail_on_invoke")
	failOnOpen, failOnOpenDiags := parseStringList(ctx, data.FailOnOpen, "fail_on_open")
	deferChanges, deferChangesDiags := parseStringList(ctx, data.DeferChanges, "defer_changes")

	response.Diagnostics.Append(failOnDeleteDiags...)
	response.Diagnostics.Append(failOnCreateDiags...)
	response.Diagnostics.Append(failOnReadDiags...)
	response.Diagnostics.Append(failOnUpdateDiags...)
	response.Diagnostics.Append(failOnInvokeDiags...)
	response.Diagnostics.Append(failOnOpenDiags...)
	response.Diagnostics.Append(deferChangesDiags...)

	m.failOnDelete = failOnDelete
	m.failOnCreate = failOnCreate
	m.failOnRead = failOnRead
	m.failOnUpdate = failOnUpdate
	m.failOnInvoke = failOnInvoke
	m.failOnOpen = failOnOpen
	m.deferChanges = deferChanges
	m.strictIdentity = data.StrictIdentity.ValueBool()
	m.strictPrivateState = data.StrictPrivateState.ValueBool()

	// The 'cluster' nested block is inert except as an observation channel:
	// when a known host is supplied with fail = true, fail Configure with a
	// diagnostic echoing the host — proving the block content reached the
	// provider (rather than being silently dropped). An unknown host is
	// tolerated (not an error), so a deferral-driving unknown config still
	// configures cleanly.
	if !data.Cluster.IsNull() && !data.Cluster.IsUnknown() {
		var clusters []clusterModel
		response.Diagnostics.Append(data.Cluster.ElementsAs(ctx, &clusters, false)...)
		if response.Diagnostics.HasError() {
			return
		}
		for _, cluster := range clusters {
			if cluster.Host.IsUnknown() {
				continue
			}
			if cluster.Fail.ValueBool() {
				response.Diagnostics.AddError(
					"cluster block received",
					fmt.Sprintf("cluster.host = %q", cluster.Host.ValueString()),
				)
			}
		}
	}
}

func parseStringList(ctx context.Context, value types.List, attr string) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics

	if value.IsNull() {
		return nil, diags
	}

	if value.IsUnknown() {
		diags.Append(diag.NewAttributeErrorDiagnostic(path.Root(attr), "value is unknown", "unknown values are not permitted"))
		return nil, diags
	}

	var types []types.String
	diags.Append(value.ElementsAs(ctx, &types, false)...)

	var elements []string
	for ix, element := range types {
		if element.IsNull() {
			diags.Append(diag.NewAttributeErrorDiagnostic(path.Root(attr).AtListIndex(ix), "element is null", "null values are not permitted"))
			continue
		}

		if element.IsUnknown() {
			diags.Append(diag.NewAttributeErrorDiagnostic(path.Root(attr).AtListIndex(ix), "element is null", "null values are not permitted"))
			continue
		}

		elements = append(elements, element.ValueString())
	}

	return elements, diags
}

func (m *tfcoremockProvider) Metadata(ctx context.Context, request provider.MetadataRequest, response *provider.MetadataResponse) {
	response.Version = m.version
	response.TypeName = "tfcoremock"
}

func (m *tfcoremockProvider) Resources(ctx context.Context) []func() tfresource.Resource {
	resources := []func() tfresource.Resource{
		func() tfresource.Resource {
			return resource.Resource{
				Name:           "tfcoremock_complex_resource",
				InternalSchema: complex.Schema(3),
				Client:         m.client,
				FailOnDelete:   m.failOnDelete,
				FailOnCreate:   m.failOnCreate,
				FailOnRead:     m.failOnRead,
				FailOnUpdate:   m.failOnUpdate,
				FailOnceDir:    m.failOnceDirectory,
				DeferChanges:   m.deferChanges,
				StrictIdentity: m.strictIdentity,

				StrictPrivateState: m.strictPrivateState,

				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		},
		func() tfresource.Resource {
			return resource.Resource{
				Name:           "tfcoremock_simple_resource",
				InternalSchema: simple.Schema,
				Client:         m.client,
				FailOnDelete:   m.failOnDelete,
				FailOnCreate:   m.failOnCreate,
				FailOnRead:     m.failOnRead,
				FailOnUpdate:   m.failOnUpdate,
				FailOnceDir:    m.failOnceDirectory,
				DeferChanges:   m.deferChanges,
				StrictIdentity: m.strictIdentity,

				StrictPrivateState: m.strictPrivateState,

				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		},
	}

	schemas, err := m.reader.Read()
	if err != nil {
		// This isn't ideal, as the plugin will tell the user this is a problem
		// with the provider. It's not though, this means the provided dynamic
		// resources file either wasn't valid JSON or didn't match our schema.
		//
		// We don't have a way to raise an error through the plugin at this
		// point in time though, so the only thing we can really do is panic.
		//
		// We add a lot of context to this panic to try and make the caller
		// realise exactly what the problem is.
		panic(fmt.Sprintf("The tfcoremock provider either failed to parse or failed to validate your dynamic resources file. "+
			"Terraform will say this is a problem in the provider, but in this case it is a problem in your dynamic resources file. "+
			"We have the following error from the parser, hopefully it provides additional context about the problem but these errors are not always helpful."+
			"\n\n%s\n", err.Error()))
	}

	for name, schema := range schemas {
		resourceName := name
		resourceSchema := schema
		resources = append(resources, func() tfresource.Resource {
			return resource.Resource{
				Name:           resourceName,
				InternalSchema: resourceSchema,
				Client:         m.client,
				FailOnDelete:   m.failOnDelete,
				FailOnCreate:   m.failOnCreate,
				FailOnRead:     m.failOnRead,
				FailOnUpdate:   m.failOnUpdate,
				FailOnceDir:    m.failOnceDirectory,
				DeferChanges:   m.deferChanges,
				StrictIdentity: m.strictIdentity,

				StrictPrivateState: m.strictPrivateState,

				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		})
	}

	return resources
}

func (m *tfcoremockProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	datasources := []func() datasource.DataSource{
		func() datasource.DataSource {
			return resource.DataSource{
				Name:           "tfcoremock_complex_resource",
				InternalSchema: complex.Schema(3),
				Client:         m.client,
				FailOnRead:     m.failOnRead,
				FailOnceDir:    m.failOnceDirectory,
			}
		},
		func() datasource.DataSource {
			return resource.DataSource{
				Name:           "tfcoremock_simple_resource",
				InternalSchema: simple.Schema,
				Client:         m.client,
				FailOnRead:     m.failOnRead,
				FailOnceDir:    m.failOnceDirectory,
			}
		},
	}

	schemas, err := m.reader.Read()
	if err != nil {
		// This isn't ideal, as the plugin will tell the user this is a problem
		// with the provider. It's not though, this means the provided dynamic
		// resources file either wasn't valid JSON or didn't match our schema.
		//
		// We don't have a way to raise an error through the plugin at this
		// point in time though, so the only thing we can really do is panic.
		//
		// We add a lot of context to this panic to try and make the caller
		// realise exactly what the problem is.
		panic(fmt.Sprintf("The tfcoremock provider either failed to parse or failed to validate your dynamic resources file. "+
			"Terraform will say this is a problem in the provider, but in this case it is a problem in your dynamic resources file. "+
			"We have the following error from the parser, hopefully it provides additional context about the problem but these errors are not always helpful."+
			"\n\n%s\n", err.Error()))
	}

	for name, schema := range schemas {
		datasourceName := name
		datasourceSchema := schema
		datasources = append(datasources, func() datasource.DataSource {
			return resource.DataSource{
				Name:           datasourceName,
				InternalSchema: datasourceSchema,
				Client:         m.client,
				FailOnRead:     m.failOnRead,
				FailOnceDir:    m.failOnceDirectory,
			}
		})
	}

	return datasources
}

func (m *tfcoremockProvider) Actions(ctx context.Context) []func() action.Action {
	actions := []func() action.Action{
		func() action.Action {
			return resource.Action{
				Name:           "tfcoremock_complex_resource",
				InternalSchema: complex.Schema(3),
				FailOnInvoke:   m.failOnInvoke,
			}
		},
		func() action.Action {
			return resource.Action{
				Name:           "tfcoremock_simple_resource",
				InternalSchema: simple.Schema,
				FailOnInvoke:   m.failOnInvoke,
			}
		},
	}

	schemas, err := m.reader.Read()
	if err != nil {
		// This isn't ideal, as the plugin will tell the user this is a problem
		// with the provider. It's not though, this means the provided dynamic
		// resources file either wasn't valid JSON or didn't match our schema.
		//
		// We don't have a way to raise an error through the plugin at this
		// point in time though, so the only thing we can really do is panic.
		//
		// We add a lot of context to this panic to try and make the caller
		// realise exactly what the problem is.
		panic(fmt.Sprintf("The tfcoremock provider either failed to parse or failed to validate your dynamic resources file. "+
			"Terraform will say this is a problem in the provider, but in this case it is a problem in your dynamic resources file. "+
			"We have the following error from the parser, hopefully it provides additional context about the problem but these errors are not always helpful."+
			"\n\n%s\n", err.Error()))
	}

	for name, schema := range schemas {
		actionName := name
		actionSchema := schema
		actions = append(actions, func() action.Action {
			return resource.Action{
				Name:           actionName,
				InternalSchema: actionSchema,
				FailOnInvoke:   m.failOnInvoke,
			}
		})
	}

	return actions
}

// EphemeralResources returns the provider's ephemeral resources.
//
// There is exactly one, and it is not derived from the dynamic-resource file
// the way resources, data sources and actions are. An ephemeral resource is not
// a shape to be varied — it is a lifecycle to be observed — so a single
// purpose-built type with a fixed schema says more than an arbitrary number of
// generated ones would.
func (m *tfcoremockProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		func() ephemeral.EphemeralResource {
			return resource.EphemeralResource{
				Name:           "tfcoremock_ephemeral_secret",
				AuditDirectory: m.ephemeralAuditDirectory,
				FailOnOpen:     m.failOnOpen,
			}
		},
	}
}

func (m *tfcoremockProvider) ListResources(ctx context.Context) []func() list.ListResource {
	listResources := []func() list.ListResource{
		func() list.ListResource {
			return resource.ListResource{
				Name:                  "tfcoremock_complex_resource",
				InternalSchema:        complex.Schema(3),
				Client:                m.client,
				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		},
		func() list.ListResource {
			return resource.ListResource{
				Name:                  "tfcoremock_simple_resource",
				InternalSchema:        simple.Schema,
				Client:                m.client,
				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		},
	}

	schemas, err := m.reader.Read()
	if err != nil {
		// This isn't ideal, as the plugin will tell the user this is a problem
		// with the provider. It's not though, this means the provided dynamic
		// resources file either wasn't valid JSON or didn't match our schema.
		//
		// We don't have a way to raise an error through the plugin at this
		// point in time though, so the only thing we can really do is panic.
		//
		// We add a lot of context to this panic to try and make the caller
		// realise exactly what the problem is.
		panic(fmt.Sprintf("The tfcoremock provider either failed to parse or failed to validate your dynamic resources file. "+
			"Terraform will say this is a problem in the provider, but in this case it is a problem in your dynamic resources file. "+
			"We have the following error from the parser, hopefully it provides additional context about the problem but these errors are not always helpful."+
			"\n\n%s\n", err.Error()))
	}

	for name, schema := range schemas {
		listResourceName := name
		listResourceSchema := schema
		listResources = append(listResources, func() list.ListResource {
			return resource.ListResource{
				Name:                  listResourceName,
				InternalSchema:        listResourceSchema,
				Client:                m.client,
				IdentitySchemaVersion: m.identitySchemaVersion,
			}
		})
	}

	return listResources
}

func (m *tfcoremockProvider) Schema(ctx context.Context, request provider.SchemaRequest, response *provider.SchemaResponse) {
	response.Schema = provider_schema.Schema{
		Description:         description,
		MarkdownDescription: strings.ReplaceAll(markdownDescription, "''", "`"),
		Attributes: map[string]provider_schema.Attribute{
			"resource_directory": provider_schema.StringAttribute{
				Description:         "The directory that the provider should use to write the human-readable JSON files for each managed resource. If `use_only_state` is set to `true` then this value does not matter. Defaults to `terraform.resource`.",
				MarkdownDescription: "The directory that the provider should use to write the human-readable JSON files for each managed resource. If `use_only_state` is set to `true` then this value does not matter. Defaults to `terraform.resource`.",
				Optional:            true,
			},
			"data_directory": provider_schema.StringAttribute{
				Description:         "The directory that the provider should use to read the human-readable JSON files for each requested data source. Defaults to `data.resource`.",
				MarkdownDescription: "The directory that the provider should use to read the human-readable JSON files for each requested data source. Defaults to `data.resource`.",
				Optional:            true,
			},
			"use_only_state": provider_schema.BoolAttribute{
				Description:         "If set to true the provider will rely only on the Terraform state file to load managed resources and will not write anything to disk. Defaults to `false`.",
				MarkdownDescription: "If set to true the provider will rely only on the Terraform state file to load managed resources and will not write anything to disk. Defaults to `false`.",
				Optional:            true,
			},
			"fail_once": provider_schema.BoolAttribute{
				Optional:            true,
				Description:         "If set to true, each fail_on_create/update/read/delete injection fires only once per operation and resource ID: the first triggered call fails and records a strike under a `fail-once` subdirectory of the resource directory, and later calls for the same operation and ID succeed. Because the strike is recorded on disk, a second provider process over the same resource directory observes it. Requires the resource directory; incompatible with `use_only_state`. Defaults to `false`.",
				MarkdownDescription: "If set to true, each `fail_on_create`/`update`/`read`/`delete` injection fires only once per operation and resource ID: the first triggered call fails and records a strike under a `fail-once` subdirectory of the resource directory, and later calls for the same operation and ID succeed. Because the strike is recorded on disk, a second provider process over the same resource directory observes it. Requires the resource directory; incompatible with `use_only_state`. Defaults to `false`.",
			},
			"fail_on_create": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any resources with an ID in this list will fail during the create phase.",
				MarkdownDescription: "If set, any resources with an ID in this list will fail during the create phase.",
			},
			"fail_on_update": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any resources with an ID in this list will fail during the update phase.",
				MarkdownDescription: "If set, any resources with an ID in this list will fail during the update phase.",
			},
			"fail_on_read": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any resources or data sources with an ID in this list will fail during the read phase. Under fail_once the two are separate one-shots: failing a data source read does not consume the strike a managed resource read of the same ID would.",
				MarkdownDescription: "If set, any resources or data sources with an ID in this list will fail during the read phase. Under `fail_once` the two are separate one-shots: failing a data source read does not consume the strike a managed resource read of the same ID would.",
			},
			"fail_on_delete": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any resources with an ID in this list will fail during the delete phase.",
				MarkdownDescription: "If set, any resources with an ID in this list will fail during the delete phase.",
			},
			"fail_on_invoke": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any action whose config `string` attribute is in this list will fail when invoked.",
				MarkdownDescription: "If set, any action whose config `string` attribute is in this list will fail when invoked.",
			},
			"fail_on_open": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any ephemeral resources with an ID in this list will fail when opened.",
				MarkdownDescription: "If set, any ephemeral resources with an ID in this list will fail when opened.",
			},
			"defer_changes": provider_schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Description:         "If set, any resources with an ID in this list will have any changes deferred during the plan phase.",
				MarkdownDescription: "If set, any resources with an ID in this list will have any changes deferred during the plan phase.",
			},
			"defer_on_unknown_config": provider_schema.BoolAttribute{
				Optional:            true,
				Description:         "If set to true, a configuration carrying unknown values is ACCEPTED and every resource served by this provider is deferred with reason PROVIDER_CONFIG_UNKNOWN, the way terraform-plugin-sdk providers behave. Defaults to `false`, under which an unknown in a list-typed attribute fails Configure instead. Defaults to `false`.",
				MarkdownDescription: "If set to true, a configuration carrying unknown values is ACCEPTED and every resource served by this provider is deferred with reason `PROVIDER_CONFIG_UNKNOWN`, the way terraform-plugin-sdk providers behave. Defaults to `false`, under which an unknown in a list-typed attribute fails Configure instead.",
			},
			"strict_identity": provider_schema.BoolAttribute{
				Optional:            true,
				Description:         "If set to true, resources assert that the prior identity Terraform sends matches the protocol's rules: null when planning a create, and equal to the recorded id when reading. Defaults to `false`.",
				MarkdownDescription: "If set to true, resources assert that the prior identity Terraform sends matches the protocol's rules: null when planning a create, and equal to the recorded id when reading. Defaults to `false`.",
			},
			"strict_private_state": provider_schema.BoolAttribute{
				Optional:            true,
				Description:         "If set to true, resources record a private-state marker on create and import, and assert that every later plan, read, update, and delete hands that marker back: absent when planning a create, and matching the recorded id otherwise. Defaults to `false`.",
				MarkdownDescription: "If set to true, resources record a private-state marker on create and import, and assert that every later plan, read, update, and delete hands that marker back: absent when planning a create, and matching the recorded id otherwise. Defaults to `false`.",
			},
		},
		Blocks: map[string]provider_schema.Block{
			// A nested config block (NestingList, encoded as a list of objects),
			// modeling the shape real providers such as helm ('kubernetes {}')
			// use for cluster-connection settings. Present so downstream
			// consumers can exercise object->list-of-one coercion for a
			// block-typed provider config attribute. Has no effect on resource
			// behavior. ('connection' is a reserved root block name, hence
			// 'cluster'.)
			"cluster": provider_schema.ListNestedBlock{
				Description:         "Optional cluster settings, modeling a provider whose config uses a nested block. When `host` is known and `fail = true`, the provider fails Configure with a diagnostic echoing the host.",
				MarkdownDescription: "Optional cluster settings, modeling a provider whose config uses a nested block. When `host` is known and `fail = true`, the provider fails Configure with a diagnostic echoing the host.",
				NestedObject: provider_schema.NestedBlockObject{
					Attributes: map[string]provider_schema.Attribute{
						"host": provider_schema.StringAttribute{
							Optional:            true,
							Description:         "Connection host. Echoed back in a diagnostic when `fail = true`.",
							MarkdownDescription: "Connection host. Echoed back in a diagnostic when `fail = true`.",
						},
						"fail": provider_schema.BoolAttribute{
							Optional:            true,
							Description:         "When true and `host` is known, the provider fails Configure with a diagnostic echoing the host.",
							MarkdownDescription: "When true and `host` is known, the provider fails Configure with a diagnostic echoing the host.",
						},
					},
				},
			},
		},
	}
}

// identitySchemaVersionFromEnv reads the identity schema version the provider
// should declare. An unset or unparseable value leaves it at 0, the version
// resources have always declared.
func identitySchemaVersionFromEnv() int64 {
	raw := os.Getenv(identitySchemaVersionEnvVarName)
	if len(raw) == 0 {
		return 0
	}

	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version < 0 {
		return 0
	}

	return version
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		dynamicResourcesPath := "dynamic_resources.json"
		if dynamicResourcesPathEnvVar := os.Getenv(dynamicResourcesPathEnvVarName); len(dynamicResourcesPathEnvVar) > 0 {
			dynamicResourcesPath = dynamicResourcesPathEnvVar
		}

		return &tfcoremockProvider{
			version:               version,
			reader:                dynamic.FileReader{File: dynamicResourcesPath},
			identitySchemaVersion: identitySchemaVersionFromEnv(),
		}
	}
}

func NewForTesting(version string, resources string) func() provider.Provider {
	return func() provider.Provider {
		return &tfcoremockProvider{
			version:               version,
			reader:                dynamic.StringReader{Data: resources},
			identitySchemaVersion: identitySchemaVersionFromEnv(),
		}
	}
}
