// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/computed"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/client"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/data"
	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"
)

var _ resource.Resource = Resource{}
var _ resource.ResourceWithIdentity = Resource{}
var _ resource.ResourceWithImportState = Resource{}
var _ resource.ResourceWithModifyPlan = Resource{}
var _ resource.ResourceWithUpgradeIdentity = Resource{}

type Resource struct {
	Name           string
	InternalSchema schema.Schema
	Client         client.Client

	FailOnDelete []string
	FailOnCreate []string
	FailOnRead   []string
	FailOnUpdate []string
	DeferChanges []string

	// FailOnceDir, when set, makes each FailOn* injection one-shot per
	// operation and resource ID: the first triggered call records a strike
	// file here and fails, and later calls for the same operation and ID
	// succeed. On disk rather than in memory so a second provider process over
	// the same store observes the strike. See forcedFailure.
	FailOnceDir string

	// DeferUntilReloadDir, when set, makes each DeferChanges injection clear on
	// a PLUGIN RESTART rather than never: the id keeps deferring for as long as
	// this provider process is the one answering, and stops the first time a
	// different process is asked. Empty means the static behavior — the id
	// defers every call, forever. See deferralFires.
	DeferUntilReloadDir string

	// IdentitySchemaVersion selects which version of the identity schema this
	// resource declares. See IdentitySchema.
	IdentitySchemaVersion int64

	// StrictIdentity makes the prior identity a client sends observable by
	// asserting it. This provider derives every identity it reports from its own
	// state, so an incoming identity otherwise leaves no trace: a client that
	// carried the wrong one — or none at all — would still see the right answer
	// come back. See assertPriorIdentity.
	StrictIdentity bool

	// StrictPrivateState makes a client's private-state round-trip observable:
	// create and import record a marker in private state, and every later call
	// that should carry the object's private state asserts it came back exactly.
	// The framework pre-populates each response's private state from the
	// request, so without the assertion even a client that dropped the blob
	// entirely would look correct. See assertPrivateMarker.
	StrictPrivateState bool
}

func (r Resource) Metadata(ctx context.Context, request resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = r.Name
}

func (r Resource) Schema(ctx context.Context, request resource.SchemaRequest, response *resource.SchemaResponse) {
	var err error
	if response.Schema, err = r.InternalSchema.ToTerraformResourceSchema(); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic(fmt.Sprintf("failed to build resource schema for '%s'", r.Name), err.Error()))
	}
}

// IdentitySchema describes the resource's identity at the version this provider
// was started with.
//
// Version 1 adds a `urn`, derived from the id, so that a client can be driven
// across an identity schema bump and exercise UpgradeIdentity. Keep the shape in
// step with data.Resource.Identity, which builds the matching value.
func (r Resource) IdentitySchema(ctx context.Context, request resource.IdentitySchemaRequest, response *resource.IdentitySchemaResponse) {
	attributes := map[string]identityschema.Attribute{
		"id": identityschema.StringAttribute{
			RequiredForImport: true,
			Description:       "The ID of the resource.",
		},
	}

	if r.IdentitySchemaVersion >= 1 {
		attributes["urn"] = identityschema.StringAttribute{
			// Derived from the id, so requiring it for import would make an
			// import block carry redundant information.
			OptionalForImport: true,
			Description:       "The URN of the resource, derived from its ID.",
		}
	}

	response.IdentitySchema = identityschema.Schema{
		Version:    r.IdentitySchemaVersion,
		Attributes: attributes,
	}
}

// UpgradeIdentity migrates an identity recorded against an older version of the
// identity schema. Identity is versioned separately from the resource schema,
// and only the provider knows how to read an older version's shape.
func (r Resource) UpgradeIdentity(ctx context.Context) map[int64]resource.IdentityUpgrader {
	if r.IdentitySchemaVersion < 1 {
		// Nothing to upgrade from at version 0.
		return nil
	}

	return map[int64]resource.IdentityUpgrader{
		// Version 0 recorded only the id; the urn is derived from it.
		0: {
			IdentityUpgrader: func(ctx context.Context, request resource.UpgradeIdentityRequest, response *resource.UpgradeIdentityResponse) {
				if request.RawIdentity == nil {
					response.Diagnostics.AddError("failed to upgrade identity", "no prior identity was supplied")
					return
				}

				rawType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"id": tftypes.String}}
				value, err := request.RawIdentity.Unmarshal(rawType)
				if err != nil {
					response.Diagnostics.AddError("failed to upgrade identity", err.Error())
					return
				}

				var raw map[string]tftypes.Value
				if err := value.As(&raw); err != nil {
					response.Diagnostics.AddError("failed to upgrade identity", err.Error())
					return
				}

				var id *string
				if err := raw["id"].As(&id); err != nil {
					response.Diagnostics.AddError("failed to upgrade identity", err.Error())
					return
				}
				if id == nil {
					response.Diagnostics.AddError("failed to upgrade identity", "the prior identity recorded no id")
					return
				}

				// Set the whole identity rather than attribute by attribute:
				// the framework hands the upgrader a response identity with a
				// schema but no value, so there is nothing for an attribute
				// write to be written into. Building it through
				// data.Resource.Identity also keeps one source of truth for the
				// shape at each version.
				upgraded := data.Resource{Values: map[string]data.Value{"id": {String: id}}}
				response.Diagnostics.Append(response.Identity.Set(ctx, upgraded.Identity(r.IdentitySchemaVersion))...)
			},
		},
	}
}

func (r Resource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.Plan.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}
	resource.ResourceType = r.Name

	// The root ID is a special computed value.
	if _, ok := resource.Values["id"]; !ok {
		id, err := uuid.GenerateUUID()
		if err != nil {
			response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to generate id", err.Error()))
			return
		}
		resource.Values["id"] = data.Value{
			String: &id,
		}
	}

	// Now go and do the rest of the computed values.
	if err := computed.GenerateComputedValues(resource, r.InternalSchema); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to generate computed values", err.Error()))
		return
	}

	// Write-only values live only in the config; see applyWriteOnlyFromConfig.
	r.applyWriteOnlyFromConfig(ctx, request.Config, resource, &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}

	if r.forcedFailure(r.FailOnCreate, "create", resource.GetId()) {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to create resource", "forced failure"))
		return
	}

	if err := r.Client.WriteResource(ctx, resource); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to write resource", err.Error()))
		return
	}

	response.Diagnostics.Append(response.State.Set(ctx, resource)...)
	response.Diagnostics.Append(response.Identity.Set(ctx, resource.Identity(r.IdentitySchemaVersion))...)
	// The create is the call that mints the object's private state; every later
	// call must hand this marker back.
	r.stampPrivateMarker(ctx, response.Private, resource.GetId(), &response.Diagnostics)
}

func (r Resource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.State.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}

	// A read refreshes an object that already exists, so it must carry that
	// object's identity and private state.
	r.assertPriorIdentity(ctx, "read this resource", request.Identity, resource.GetId(), &response.Diagnostics)
	r.assertPrivateMarker(ctx, "read this resource", request.Private, resource.GetId(), &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}

	if r.forcedFailure(r.FailOnRead, "read", resource.GetId()) {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to read resource", "forced failure"))
		return
	}

	data, err := r.Client.ReadResource(ctx, resource.GetId())
	if err != nil {
		if os.IsNotExist(err) {
			// This is a bit of weird one as it means we tried to read a file
			// that doesn't exist but Terraform thinks it does. We treat this
			// as "drift" and let the Terraform framework handle it.
			response.State.RemoveResource(ctx)
			response.Diagnostics.Append(response.Identity.Set(ctx, resource.Identity(r.IdentitySchemaVersion))...)
			// The private state describes an object that no longer exists, and
			// the client carries whatever this read returns straight into the
			// plan of the create that replaces it — where the marker would fail
			// the assertion this provider makes of a null prior.
			r.clearPrivateMarker(ctx, response.Private, &response.Diagnostics)
			return
		}
		response.Diagnostics.AddError("failed to read resource", err.Error())
		return
	}

	if data == nil {
		// The client returned a nil object with no error. This means it is
		// telling us to just rely on the state.
		data = resource
	}

	typ := request.State.Schema.Type().TerraformType(ctx)
	response.Diagnostics.Append(response.State.Set(ctx, data.WithType(typ.(tftypes.Object)))...)
	response.Diagnostics.Append(response.Identity.Set(ctx, data.Identity(r.IdentitySchemaVersion))...)
}

func (r Resource) Update(ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.Plan.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}
	resource.ResourceType = r.Name

	// An update applies to an object that exists, so the planned private state
	// the apply delivers must carry the object's marker — this is what pins the
	// plan-response → apply threading on the client's side.
	r.assertPrivateMarker(ctx, "update this resource", request.Private, resource.GetId(), &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}

	if err := computed.GenerateComputedValues(resource, r.InternalSchema); err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to generate computed values", err.Error()))
		return
	}

	// Write-only values live only in the config; see applyWriteOnlyFromConfig.
	r.applyWriteOnlyFromConfig(ctx, request.Config, resource, &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}

	if r.forcedFailure(r.FailOnUpdate, "update", resource.GetId()) {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to update resource", "forced failure"))
		return
	}

	if err := r.Client.UpdateResource(ctx, resource); err != nil {
		response.Diagnostics.AddError("failed to update resource", err.Error())
		return
	}

	response.Diagnostics.Append(response.State.Set(ctx, resource)...)
	response.Diagnostics.Append(response.Identity.Set(ctx, resource.Identity(r.IdentitySchemaVersion))...)
	// A state-producing call returns the blob anew.
	r.stampPrivateMarker(ctx, response.Private, resource.GetId(), &response.Diagnostics)
}

// forcedFailure reports whether a FailOn* injection should fire for this
// operation and id. Without FailOnceDir the injection is static: every
// triggered call fails. With it the injection is one-shot per (op, id): the
// first triggered call records a strike file and fails, and later calls see
// the strike and succeed — which is what lets a client exercise a
// fail-then-retry-converges path against a deterministic provider. The strike
// is a file so that a second provider process over the same resource
// directory (a later plugin launch, or another tool applying the same
// configuration) observes it; strike files live in their own subdirectory and
// never appear as managed resources.
func (r Resource) forcedFailure(list []string, op string, id string) bool {
	return forcedFailure(list, r.FailOnceDir, op, id)
}

// processNonce identifies THIS plugin process. A restarted plugin — a new
// process for the same binary and the same store — gets a different one, which
// is the whole mechanism deferralFires keys on.
var processNonce = func() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A pid is a weaker identity (it can be reused) but the only thing
		// left; a constant here would make every process look like the same
		// one, which is the failure that matters.
		return fmt.Sprintf("pid-%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}()

// deferralFires reports whether a DeferChanges injection should fire for this
// id. Without DeferUntilReloadDir the injection is static: the id defers every
// call, forever, which is what a caller wants when it is testing that a
// deferral is reported at all.
//
// With it, the injection models the thing real providers actually do: cache
// what they discovered about the remote system somewhere their Configure call
// does not rebuild, so that re-sending an identical configuration changes
// nothing and only a fresh process looks again. (The kubernetes provider's API
// discovery RESTMapper is the specimen — after a CRD is applied, the running
// plugin still does not believe the new kind exists.) The mark records which
// process deferred: this process reading its own mark keeps deferring, and a
// different process reading it proceeds. A client that never recycles the
// plugin therefore never converges, and one that recycles per phase does — the
// difference being exactly what such a client needs to be able to prove.
func (r Resource) deferralFires(id string) bool {
	if !slices.Contains(r.DeferChanges, id) {
		return false
	}
	if r.DeferUntilReloadDir == "" {
		return true
	}
	mark := filepath.Join(r.DeferUntilReloadDir, url.PathEscape(id))
	if b, err := os.ReadFile(mark); err == nil {
		return strings.TrimSpace(string(b)) == processNonce
	}
	// Best-effort for the same reason forcedFailure's strike is: an unrecorded
	// mark leaves the static behavior, which is loudly visible, where a
	// swallowed failure-to-defer would not be.
	if err := os.MkdirAll(r.DeferUntilReloadDir, 0700); err == nil {
		_ = os.WriteFile(mark, []byte(processNonce+"\n"), 0600)
	}
	return true
}

// forcedFailure is the injection itself, free of the resource it fires on so
// the data source shares it verbatim. The op string is part of the strike key,
// so a managed read and a data-source read of the same id are independent
// one-shots.
func forcedFailure(list []string, failOnceDir, op, id string) bool {
	if !slices.Contains(list, id) {
		return false
	}
	if failOnceDir == "" {
		return true
	}
	strike := filepath.Join(failOnceDir, fmt.Sprintf("%s-%s", op, url.PathEscape(id)))
	if _, err := os.Stat(strike); err == nil {
		return false
	}
	// Best-effort on purpose: if the strike cannot be recorded the injection
	// simply keeps firing, which is the static behavior and loudly visible,
	// where a swallowed failure-to-fail would be neither.
	if err := os.MkdirAll(failOnceDir, 0700); err == nil {
		_ = os.WriteFile(strike, []byte(op+" "+id+"\n"), 0600)
	}
	return true
}

// writeOnlyNames returns the names of this resource's top-level write-only
// attributes, in declaration-independent (sorted) order.
func (r Resource) writeOnlyNames() []string {
	var out []string
	for name, attr := range r.InternalSchema.Attributes {
		if attr.WriteOnly {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// applyWriteOnlyFromConfig copies each write-only attribute's value out of the
// CONFIG and into the object the mock is about to store.
//
// The config is the only place the value exists. A write-only attribute is null
// in the plan and null in the state by definition, so a provider that reads it
// from request.Plan — as every other attribute here is read — would receive
// nothing. Real providers have the same obligation; making the mock honour it is
// what lets a test prove the value actually ARRIVED, rather than proving only
// that state does not contain it (which a completely unwired feature would also
// satisfy).
//
// The mock's data file is its "remote system", so the value landing there is the
// observable arrival. The framework nullifies the same attributes in the
// response state, so it does not follow the object back to the client.
func (r Resource) applyWriteOnlyFromConfig(ctx context.Context, config tfsdk.Config, res *data.Resource, diags *diag.Diagnostics) {
	for _, name := range r.writeOnlyNames() {
		var val types.String
		if d := config.GetAttribute(ctx, path.Root(name), &val); d.HasError() {
			diags.Append(d...)
			return
		}
		if val.IsNull() || val.IsUnknown() {
			delete(res.Values, name)
			continue
		}
		s := val.ValueString()
		res.Values[name] = data.Value{String: &s}
	}
}

// writeOnlyChanged reports whether any write-only attribute marked Replace has a
// configured value differing from the one the mock stored, and returns the paths
// that differ.
//
// This is the only way a change to a write-only attribute can be detected at
// all: prior state and planned state both hold null for it, so no equality test
// downstream of the provider can see the difference. Comparing against what was
// actually stored is a genuine comparison, not an inference — the mock is the
// remote system here, and it is the only party that knows the old value.
func (r Resource) writeOnlyReplacePaths(ctx context.Context, config tfsdk.Config, stored *data.Resource, diags *diag.Diagnostics) []path.Path {
	var out []path.Path
	for _, name := range r.writeOnlyNames() {
		if !r.InternalSchema.Attributes[name].Replace {
			continue
		}
		var val types.String
		if d := config.GetAttribute(ctx, path.Root(name), &val); d.HasError() {
			diags.Append(d...)
			return nil
		}
		var want *string
		if !val.IsNull() && !val.IsUnknown() {
			s := val.ValueString()
			want = &s
		}
		var have *string
		if stored != nil {
			if v, ok := stored.Values[name]; ok {
				have = v.String
			}
		}
		switch {
		case want == nil && have == nil:
		case want == nil || have == nil:
			out = append(out, path.Root(name))
		case *want != *have:
			out = append(out, path.Root(name))
		}
	}
	return out
}

func (r Resource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
	resource := &data.Resource{}
	response.Diagnostics.Append(request.State.Get(ctx, &resource)...)
	if response.Diagnostics.HasError() {
		return
	}

	// The delete is the last call the object sees, and it must still carry the
	// private state the most recent state-producing call returned.
	r.assertPrivateMarker(ctx, "delete this resource", request.Private, resource.GetId(), &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}

	if r.forcedFailure(r.FailOnDelete, "delete", resource.GetId()) {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("failed to delete resource", "forced failure"))
		return
	}

	if err := r.Client.DeleteResource(ctx, resource.GetId()); err != nil {
		response.Diagnostics.AddError("failed to delete resource", err.Error())
		return
	}

	response.State.RemoveResource(ctx)
	response.Diagnostics.Append(response.Identity.Set(ctx, resource.Identity(r.IdentitySchemaVersion))...)
}

func (r Resource) ImportState(ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse) {
	// The identity-aware passthrough handles both locators: it copies
	// request.ID into state when the practitioner imported by id, and otherwise
	// reads the id out of the supplied identity. ImportStatePassthroughID
	// handles only the former — on an identity-keyed import it writes nothing,
	// and because the framework's "missing import state" guard is itself gated
	// on a non-empty request.ID, an all-null state is returned with no
	// diagnostic. The read that follows then dereferences an absent id and
	// panics, taking the plugin process down.
	resource.ImportStatePassthroughWithIdentity(ctx, path.Root("id"), path.Root("id"), request, response)
	if response.Diagnostics.HasError() || response.Identity == nil {
		return
	}

	// Report the adopted object's identity. The framework pre-populates the
	// response identity from the request, so importing by identity already
	// carries one — but importing by id leaves it null, and the caller would
	// then have nothing to record until the read that follows. The id is in
	// state either way, and the identity derives from it.
	var id types.String
	response.Diagnostics.Append(response.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if response.Diagnostics.HasError() || id.IsNull() {
		return
	}

	value := id.ValueString()
	imported := data.Resource{Values: map[string]data.Value{"id": {String: &value}}}
	response.Diagnostics.Append(response.Identity.Set(ctx, imported.Identity(r.IdentitySchemaVersion))...)
	// An import brings an object under management, so it mints the private
	// state the same way create does — the client must carry it from adoption
	// onward.
	r.stampPrivateMarker(ctx, response.Private, value, &response.Diagnostics)
}

// assertPriorIdentity checks the identity a client sent alongside prior state
// against what the protocol requires, when strict_identity is on. op names the
// call for the diagnostic. A wantID of "" means the call must carry no identity
// at all, which is the rule for planning a create: the object does not exist
// yet, so there is nothing to identify — and on the create half of a replace,
// an identity leaking across from the object being destroyed would assert the
// new remote object is the old one.
func (r Resource) assertPriorIdentity(ctx context.Context, op string, identity *tfsdk.ResourceIdentity, wantID string, diags *diag.Diagnostics) {
	if !r.StrictIdentity {
		return
	}

	absent := identity == nil || identity.Raw.IsNull()

	if wantID == "" {
		if !absent {
			diags.AddError(
				"unexpected prior identity",
				fmt.Sprintf("strict_identity is set and this call to %s carried a prior identity, but the resource does not exist yet so there is nothing to identify.", op))
		}
		return
	}

	if absent {
		diags.AddError(
			"missing prior identity",
			fmt.Sprintf("strict_identity is set and this call to %s carried no prior identity, but the resource is recorded with id %q.", op, wantID))
		return
	}

	var got types.String
	diags.Append(identity.GetAttribute(ctx, path.Root("id"), &got)...)
	if diags.HasError() {
		return
	}

	if got.ValueString() != wantID {
		diags.AddError(
			"prior identity does not match",
			fmt.Sprintf("strict_identity is set and this call to %s carried the identity of %q, but the resource is recorded with id %q.", op, got.ValueString(), wantID))
	}
}

// privateMarkerKey is the private-state key strict_private_state records its
// marker under. The framework requires keys without a leading "." (reserved)
// and values that are valid JSON.
const privateMarkerKey = "state_marker"

// privateData is the surface of the framework's private-state accessor
// (*privatestate.ProviderData, carried on every request/response as Private)
// that the marker logic needs. An interface because the concrete type lives in
// the framework's internal/ tree; both of its methods are nil-receiver safe,
// so call sites pass request/response Private fields through unguarded.
type privateData interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// privateMarker is the JSON shape stored under privateMarkerKey. Deriving it
// from the object's own id makes the assertion per-object: a blob carried over
// from a different object fails, not just a missing one.
type privateMarker struct {
	ID string `json:"id"`
}

// markerBytes builds the private-state marker for one object id.
func markerBytes(id string) []byte {
	b, _ := json.Marshal(privateMarker{ID: id})
	return b
}

// stampPrivateMarker records the marker for id in a response's private state,
// when strict_private_state is on. Called by the calls that bring an object
// into being (create, import) and by update, which as a state-producing call
// returns the blob anew.
func (r Resource) stampPrivateMarker(ctx context.Context, private privateData, id string, diags *diag.Diagnostics) {
	if !r.StrictPrivateState {
		return
	}
	diags.Append(private.SetKey(ctx, privateMarkerKey, markerBytes(id))...)
}

// clearPrivateMarker drops the marker from a response's private state, when
// strict_private_state is on. The framework pre-populates each response's
// private state from the request, so a read that reports the object gone would
// otherwise hand back a null state carrying the marker of an object that no
// longer exists — a pairing this provider's own plan-time assertion rejects,
// and one no well-behaved provider produces. The private state belongs to the
// object; when the object is gone, so is it.
func (r Resource) clearPrivateMarker(ctx context.Context, private privateData, diags *diag.Diagnostics) {
	if !r.StrictPrivateState {
		return
	}
	diags.Append(private.SetKey(ctx, privateMarkerKey, nil)...)
}

// assertPrivateMarker checks the private state a client sent alongside prior
// state, when strict_private_state is on. op names the call for the
// diagnostic. A wantID of "" means the call must carry no marker at all, which
// is the rule for planning a create: no call has produced the object yet, so
// none can have returned private state for it — and on the create half of a
// replace, a blob leaking across from the object being destroyed would hand
// the new object the old one's private state.
func (r Resource) assertPrivateMarker(ctx context.Context, op string, private privateData, wantID string, diags *diag.Diagnostics) {
	if !r.StrictPrivateState {
		return
	}

	got, d := private.GetKey(ctx, privateMarkerKey)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	if wantID == "" {
		if got != nil {
			diags.AddError(
				"unexpected private state",
				fmt.Sprintf("strict_private_state is set and this call to %s carried a private-state marker, but the resource does not exist yet so no call can have returned one.", op))
		}
		return
	}

	if got == nil {
		diags.AddError(
			"missing private state",
			fmt.Sprintf("strict_private_state is set and this call to %s carried no private-state marker, but the resource is recorded with id %q: the private state returned when it was created was not handed back.", op, wantID))
		return
	}

	var marker privateMarker
	if err := json.Unmarshal(got, &marker); err != nil {
		diags.AddError(
			"malformed private state",
			fmt.Sprintf("strict_private_state is set and this call to %s carried a private-state marker that does not parse: %s.", op, err))
		return
	}
	if marker.ID != wantID {
		diags.AddError(
			"private state does not match",
			fmt.Sprintf("strict_private_state is set and this call to %s carried the private-state marker of %q, but the resource is recorded with id %q.", op, marker.ID, wantID))
	}
}

// priorID returns the id recorded in prior state, and whether one was recorded.
func priorID(ctx context.Context, state tfsdk.State, diags *diag.Diagnostics) (string, bool) {
	if state.Raw.IsNull() {
		return "", false
	}

	prior := &data.Resource{}
	diags.Append(state.Get(ctx, &prior)...)
	if diags.HasError() {
		return "", false
	}

	if _, ok := prior.Values["id"]; !ok {
		return "", false
	}

	return prior.GetId(), true
}

func (r Resource) ModifyPlan(ctx context.Context, request resource.ModifyPlanRequest, response *resource.ModifyPlanResponse) {
	// A null plan is a destroy, which carries the prior identity and has no
	// rule of its own to check.
	if !request.Plan.Raw.IsNull() {
		// A null prior state means this plans a create — including the create
		// half of a replace, which is the case worth pinning.
		wantID, _ := priorID(ctx, request.State, &response.Diagnostics)
		if r.StrictIdentity {
			r.assertPriorIdentity(ctx, "plan a change to this resource", request.Identity, wantID, &response.Diagnostics)
			if response.Diagnostics.HasError() {
				return
			}
		}
		// The plan is where a client's prior private state becomes visible on
		// the wire: an update plan must carry the object's marker, and a create
		// plan must carry none.
		r.assertPrivateMarker(ctx, "plan a change to this resource", request.Private, wantID, &response.Diagnostics)
		if response.Diagnostics.HasError() {
			return
		}
	}

	// An update to a write-only attribute is invisible to every equality test
	// downstream: prior and planned both hold null for it by definition. Only
	// the remote system knows the old value, so the mock — which is the remote
	// system here — is the one that has to say the change happened, by naming
	// the path as requires-replace. Skipped on create and destroy, where the
	// action is not in question.
	if !request.Plan.Raw.IsNull() && !request.State.Raw.IsNull() {
		stored := &data.Resource{}
		if d := request.State.Get(ctx, &stored); !d.HasError() {
			if id, ok := stored.Values["id"]; ok && id.String != nil {
				if remote, err := r.Client.ReadResource(ctx, *id.String); err == nil && remote != nil {
					paths := r.writeOnlyReplacePaths(ctx, request.Config, remote, &response.Diagnostics)
					if response.Diagnostics.HasError() {
						return
					}
					response.RequiresReplace = append(response.RequiresReplace, paths...)
				}
			}
		}
	}

	res := &data.Resource{}
	response.Diagnostics.Append(request.Plan.Get(ctx, &res)...)
	if response.Diagnostics.HasError() {
		return
	}

	if _, ok := res.Values["id"]; !ok {
		// then resource is unknown or something, which means we can't check
		// it
		return
	}

	id := res.GetId()
	if r.deferralFires(id) {
		// Then we want to defer this change!

		if !request.ClientCapabilities.DeferralAllowed {
			response.Diagnostics.AddAttributeError(path.Root("id"), "Invalid resource deferral", "This `id` was marked as \"should be deferred\", but the current version of Terraform does not support deferrals.")
			return
		}

		response.Deferred = &resource.Deferred{
			// not technically true, but the closest we have
			Reason: resource.DeferredReasonResourceConfigUnknown,
		}
	}
}
