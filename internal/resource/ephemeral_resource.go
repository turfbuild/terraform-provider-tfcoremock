// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	ephemeral_schema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ ephemeral.EphemeralResource = EphemeralResource{}
var _ ephemeral.EphemeralResourceWithRenew = EphemeralResource{}
var _ ephemeral.EphemeralResourceWithClose = EphemeralResource{}

// EphemeralSecretPrefix prefixes the value this resource produces. The value is
// derived from the id rather than taken from the configuration on purpose: a
// caller can then predict the exact string without that string ever appearing
// in a configuration file, which is what makes "this value is nowhere on disk"
// an assertion about the value rather than about the configuration.
const EphemeralSecretPrefix = "tfcoremock-secret-"

// ephemeralPrivateKey names the private-state entry carrying the identity of an
// opened object. The framework creates a fresh resource instance per RPC and
// neither Renew nor Close is given the configuration, so private state is the
// only channel by which an open reaches its own renew and close — which also
// makes it the only place a client's private-state round-trip for ephemeral
// resources is observable at all.
const ephemeralPrivateKey = "tfcoremock_ephemeral"

// ephemeralAuditMu serializes the read-modify-write of an audit file. Opens for
// distinct addresses proceed concurrently within a single walk, and they may
// share an id, so the append has to be atomic with respect to this process.
var ephemeralAuditMu sync.Mutex

// EphemeralResource is an ephemeral resource whose whole lifecycle is left on
// disk. Every Open, Renew and Close appends a record to a JSON file, so a
// client's handling of the four ephemeral RPCs becomes a fact a test can read
// rather than a log line it has to trust.
//
// No released provider offers this. hashicorp/random does ship ephemeral
// resources, but Open-only: its Close is a framework no-op and it never asks to
// be renewed, so against it a lifecycle bug — a leaked object, a dropped
// private blob, a renewal that never fires — looks exactly like correct
// behavior. That is why this lives here.
type EphemeralResource struct {
	Name string

	// AuditDirectory is where lifecycle records are written, one file per id.
	// Empty disables recording, which is what `use_only_state` selects: there is
	// no resource directory to put them in.
	AuditDirectory string

	// FailOnOpen lists ids whose Open fails. It is the ephemeral counterpart of
	// FailOnCreate, and it exists for one case in particular: an open that
	// succeeds and is then followed by a failing one in the same walk, which is
	// how "the first object is still closed when the walk errors" gets proven.
	FailOnOpen []string
}

// EphemeralAuditEvent is one recorded lifecycle call.
type EphemeralAuditEvent struct {
	// Sequence numbers the events within one file, from 1. Wall-clock time is
	// deliberately not recorded: the ordering is the whole point and a
	// timestamp would only make the file harder to compare.
	Sequence int `json:"sequence"`

	// Event is "open", "renew" or "close".
	Event string `json:"event"`
}

// EphemeralAudit is the on-disk lifecycle record for one id.
//
// It accumulates across every open of that id, so a client that opens the same
// object repeatedly leaves repeated open/close pairs here. That is not noise:
// how many times a client opens an object, and whether every open was matched
// by a close, are exactly the properties worth reading back.
type EphemeralAudit struct {
	Id     string                `json:"id"`
	Events []EphemeralAuditEvent `json:"events"`
}

// ephemeralPrivate is what an open hands to its own renew and close.
type ephemeralPrivate struct {
	Id string `json:"id"`
}

type ephemeralSecretModel struct {
	Id           types.String `tfsdk:"id"`
	Value        types.String `tfsdk:"value"`
	RenewAfterMs types.Int64  `tfsdk:"renew_after_ms"`
}

func (e EphemeralResource) Metadata(ctx context.Context, request ephemeral.MetadataRequest, response *ephemeral.MetadataResponse) {
	response.TypeName = e.Name
}

func (e EphemeralResource) Schema(ctx context.Context, request ephemeral.SchemaRequest, response *ephemeral.SchemaResponse) {
	response.Schema = ephemeral_schema.Schema{
		Description:         "An ephemeral resource that produces a value which must never be written down, and records its own open/renew/close lifecycle to disk so that a client's handling of it can be verified.",
		MarkdownDescription: "An ephemeral resource that produces a value which must never be written down, and records its own open/renew/close lifecycle to disk so that a client's handling of it can be verified.",
		Attributes: map[string]ephemeral_schema.Attribute{
			"id": ephemeral_schema.StringAttribute{
				Required:            true,
				Description:         "Names the lifecycle record this object writes, and determines the value it produces.",
				MarkdownDescription: "Names the lifecycle record this object writes, and determines the value it produces.",
			},
			"value": ephemeral_schema.StringAttribute{
				Computed:            true,
				Description:         "The produced value. Derived from `id` so that a caller can predict it without it appearing in any configuration.",
				MarkdownDescription: "The produced value. Derived from `id` so that a caller can predict it without it appearing in any configuration.",
			},
			"renew_after_ms": ephemeral_schema.Int64Attribute{
				Optional:            true,
				Description:         "If set, the object asks to be renewed this many milliseconds after it is opened; a value of 0 or less asks for a renewal immediately. Renewal is requested once — the renewal itself names no further deadline. If unset, no renewal is ever requested.",
				MarkdownDescription: "If set, the object asks to be renewed this many milliseconds after it is opened; a value of `0` or less asks for a renewal immediately. Renewal is requested once — the renewal itself names no further deadline. If unset, no renewal is ever requested.",
			},
		},
	}
}

func (e EphemeralResource) Open(ctx context.Context, request ephemeral.OpenRequest, response *ephemeral.OpenResponse) {
	var config ephemeralSecretModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	id := config.Id.ValueString()

	// Checked before the record is written: a failed open produced no object,
	// so recording one would claim a lifecycle that never started.
	if slices.Contains(e.FailOnOpen, id) {
		response.Diagnostics.AddError(
			"ephemeral resource open failed",
			fmt.Sprintf("the ephemeral resource with id %q is configured to fail via fail_on_open", id))
		return
	}

	if err := e.record(id, "open"); err != nil {
		response.Diagnostics.AddError("failed to record the ephemeral open", err.Error())
		return
	}

	config.Value = types.StringValue(EphemeralSecretPrefix + id)
	response.Diagnostics.Append(response.Result.Set(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	private, err := json.Marshal(ephemeralPrivate{Id: id})
	if err != nil {
		response.Diagnostics.AddError("failed to marshal the ephemeral private state", err.Error())
		return
	}
	response.Diagnostics.Append(response.Private.SetKey(ctx, ephemeralPrivateKey, private)...)
	if response.Diagnostics.HasError() {
		return
	}

	if !config.RenewAfterMs.IsNull() {
		response.RenewAt = time.Now().Add(time.Duration(config.RenewAfterMs.ValueInt64()) * time.Millisecond)
	}
}

func (e EphemeralResource) Renew(ctx context.Context, request ephemeral.RenewRequest, response *ephemeral.RenewResponse) {
	id, ok := e.identify(ctx, request.Private, &response.Diagnostics)
	if !ok {
		return
	}
	if err := e.record(id, "renew"); err != nil {
		response.Diagnostics.AddError("failed to record the ephemeral renew", err.Error())
		return
	}
	// RenewAt is left zero: one renewal is enough to prove the RPC is wired,
	// and asking for another immediately would spin the client's renewal loop
	// for as long as the object stays open.
}

func (e EphemeralResource) Close(ctx context.Context, request ephemeral.CloseRequest, response *ephemeral.CloseResponse) {
	id, ok := e.identify(ctx, request.Private, &response.Diagnostics)
	if !ok {
		return
	}
	if err := e.record(id, "close"); err != nil {
		response.Diagnostics.AddError("failed to record the ephemeral close", err.Error())
		return
	}
}

// identify recovers the id an open recorded in private state. A missing or
// unreadable blob is an error rather than a skipped record, because the client
// dropping it is precisely the defect this resource exists to expose: without
// the assertion, a client that discarded the blob would produce a lifecycle
// record indistinguishable from one that carried it faithfully.
func (e EphemeralResource) identify(ctx context.Context, private ephemeralPrivateReader, diags *diag.Diagnostics) (string, bool) {
	if private == nil {
		diags.AddError(
			"missing ephemeral private state",
			"this client sent no private state back, so the object opened earlier cannot be identified")
		return "", false
	}

	raw, getDiags := private.GetKey(ctx, ephemeralPrivateKey)
	diags.Append(getDiags...)
	if diags.HasError() {
		return "", false
	}
	if len(raw) == 0 {
		diags.AddError(
			"missing ephemeral private state",
			fmt.Sprintf("this client sent private state back without the %q entry recorded when the object was opened", ephemeralPrivateKey))
		return "", false
	}

	var stored ephemeralPrivate
	if err := json.Unmarshal(raw, &stored); err != nil {
		diags.AddError("unreadable ephemeral private state", err.Error())
		return "", false
	}
	return stored.Id, true
}

// record appends one event to the id's audit file.
func (e EphemeralResource) record(id, event string) error {
	if e.AuditDirectory == "" {
		return nil
	}

	ephemeralAuditMu.Lock()
	defer ephemeralAuditMu.Unlock()

	jsonPath := filepath.Join(e.AuditDirectory, fmt.Sprintf("%s.json", id))

	var audit EphemeralAudit
	switch existing, err := os.ReadFile(jsonPath); {
	case err == nil:
		if err := json.Unmarshal(existing, &audit); err != nil {
			return fmt.Errorf("failed to read the existing record at %s: %w", jsonPath, err)
		}
	case !os.IsNotExist(err):
		return err
	}

	audit.Id = id
	audit.Events = append(audit.Events, EphemeralAuditEvent{
		Sequence: len(audit.Events) + 1,
		Event:    event,
	})

	jsonData, err := json.MarshalIndent(&audit, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.AuditDirectory, 0700); err != nil {
		return err
	}
	return os.WriteFile(jsonPath, jsonData, 0644)
}

// ephemeralPrivateReader is the read half of the framework's private-state
// type. Renew and Close receive that type by pointer and the framework's own
// package is internal, so this narrows it to what identify actually calls and
// lets the same code serve both.
type ephemeralPrivateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}
