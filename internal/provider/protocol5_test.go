// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These tests drive the provider through the protocol-5 server directly,
// with no Terraform binary: what they pin is that the provider can be served
// over protocol 5 at all, and that it behaves there as it does over 6.

// server5 is the protocol-5 server with the action RPCs, which plugin-go
// carries on an interface of their own.
type server5 interface {
	tfprotov5.ProviderServer
	tfprotov5.ActionServer
}

func protocol5Server(t *testing.T, resources string) server5 {
	t.Helper()
	server, err := providerserver.NewProtocol5WithError(NewForTestingWithProtocol("test", resources, 5)())()
	if err != nil {
		t.Fatalf("serving over protocol 5: %v", err)
	}
	withActions, ok := server.(server5)
	if !ok {
		t.Fatal("the protocol-5 server does not serve actions")
	}
	return withActions
}

func protocol5Schema(t *testing.T, server server5) *tfprotov5.GetProviderSchemaResponse {
	t.Helper()
	resp, err := server.GetProviderSchema(context.Background(), &tfprotov5.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if diags := errorDiagnostics5(resp.Diagnostics); diags != "" {
		t.Fatalf("GetProviderSchema over protocol 5: %s", diags)
	}
	return resp
}

func errorDiagnostics5(diags []*tfprotov5.Diagnostic) string {
	var out []string
	for _, d := range diags {
		if d.Severity == tfprotov5.DiagnosticSeverityError {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return strings.Join(out, "; ")
}

func attribute5(schema *tfprotov5.Schema, name string) *tfprotov5.SchemaAttribute {
	for _, a := range schema.Block.Attributes {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// object5 builds a value of an object type with the given attributes set and
// every other attribute null, as Terraform sends a configuration.
func object5(t *testing.T, typ tftypes.Type, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, ok := typ.(tftypes.Object)
	if !ok {
		t.Fatalf("%s is not an object type", typ)
	}
	vals := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		if v, ok := set[name]; ok {
			vals[name] = v
			continue
		}
		vals[name] = tftypes.NewValue(at, nil)
	}
	for name := range set {
		if _, ok := obj.AttributeTypes[name]; !ok {
			t.Fatalf("no attribute %q in %s", name, typ)
		}
	}
	return tftypes.NewValue(typ, vals)
}

func dynamic5(t *testing.T, typ tftypes.Type, v tftypes.Value) *tfprotov5.DynamicValue {
	t.Helper()
	dv, err := tfprotov5.NewDynamicValue(typ, v)
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func strings5(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, len(vals))
	for i, v := range vals {
		elems[i] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
}

func TestProtocol5Schema(t *testing.T) {
	resp := protocol5Schema(t, protocol5Server(t, ""))

	// The complex type's collections arrive as plain types: protocol 5 has
	// no nested attributes, so the type is all there is.
	for kind, schema := range map[string]*tfprotov5.Schema{
		"resource":    resp.ResourceSchemas["tfcoremock_complex_resource"],
		"data source": resp.DataSourceSchemas["tfcoremock_complex_resource"],
		"action":      actionSchema5(resp, "tfcoremock_complex_resource"),
	} {
		if schema == nil {
			t.Fatalf("no complex %s schema", kind)
		}
		list := attribute5(schema, "list")
		if list == nil {
			t.Fatalf("complex %s has no list attribute", kind)
		}
		want := tftypes.List{}
		if !list.Type.Is(want) {
			t.Errorf("complex %s list attribute type = %s, want a list", kind, list.Type)
		}
	}

	for _, name := range []string{"tfcoremock_simple_resource", "tfcoremock_cwd_file"} {
		if resp.ResourceSchemas[name] == nil {
			t.Errorf("no resource schema for %s", name)
		}
		if actionSchema5(resp, name) == nil {
			t.Errorf("no action schema for %s", name)
		}
	}
}

func actionSchema5(resp *tfprotov5.GetProviderSchemaResponse, name string) *tfprotov5.Schema {
	if a := resp.ActionSchemas[name]; a != nil {
		return a.Schema
	}
	return nil
}

// TestProtocol5RefusesNestedAttributes is why the provider flattens its
// schemas under protocol 5: the protocol-6 form, served over 5, is refused
// whole.
func TestProtocol5RefusesNestedAttributes(t *testing.T) {
	server, err := providerserver.NewProtocol5WithError(NewForTestingWithProtocol("test", "", 6)())()
	if err != nil {
		t.Fatalf("serving over protocol 5: %v", err)
	}
	resp, err := server.GetProviderSchema(context.Background(), &tfprotov5.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if diags := errorDiagnostics5(resp.Diagnostics); !strings.Contains(diags, "protocol version 5 cannot have Attributes set") {
		t.Fatalf("the nested form served over protocol 5: want the framework's refusal, got %q", diags)
	}
}

func TestProtocol5DynamicNestedObject(t *testing.T) {
	resources := `{
  "tfcoremock_nested": {
    "attributes": {
      "object": {
        "type": "object",
        "optional": true,
        "object": {
          "name": { "type": "string" },
          "tags": { "type": "list", "list": { "type": "object", "object": { "key": { "type": "string" } } } }
        }
      }
    }
  }
}`
	resp := protocol5Schema(t, protocol5Server(t, resources))
	if resp.ResourceSchemas["tfcoremock_nested"] == nil {
		t.Fatal("no schema for the dynamic resource")
	}
}

func TestProtocol5ActionRoundTrip(t *testing.T) {
	ctx := context.Background()
	server := protocol5Server(t, "")
	resp := protocol5Schema(t, server)

	providerType := resp.Provider.ValueType()
	configure := func(set map[string]tftypes.Value) {
		t.Helper()
		cr, err := server.ConfigureProvider(ctx, &tfprotov5.ConfigureProviderRequest{
			Config: dynamic5(t, providerType, object5(t, providerType, set)),
		})
		if err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}
		if diags := errorDiagnostics5(cr.Diagnostics); diags != "" {
			t.Fatalf("ConfigureProvider: %s", diags)
		}
	}
	configure(map[string]tftypes.Value{
		"use_only_state": tftypes.NewValue(tftypes.Bool, true),
		"fail_on_invoke": strings5("boom"),
		"warn_on_invoke": strings5("careful", "boom"),
	})

	actionType := actionSchema5(resp, "tfcoremock_simple_resource").ValueType()
	config := func(s string) *tfprotov5.DynamicValue {
		return dynamic5(t, actionType, object5(t, actionType, map[string]tftypes.Value{
			"string": tftypes.NewValue(tftypes.String, s),
		}))
	}

	vr, err := server.ValidateActionConfig(ctx, &tfprotov5.ValidateActionConfigRequest{
		ActionType: "tfcoremock_simple_resource",
		Config:     config("hello"),
	})
	if err != nil {
		t.Fatalf("ValidateActionConfig: %v", err)
	}
	if diags := errorDiagnostics5(vr.Diagnostics); diags != "" {
		t.Fatalf("ValidateActionConfig: %s", diags)
	}

	pr, err := server.PlanAction(ctx, &tfprotov5.PlanActionRequest{
		ActionType:         "tfcoremock_simple_resource",
		Config:             config("hello"),
		ClientCapabilities: &tfprotov5.PlanActionClientCapabilities{DeferralAllowed: true},
	})
	if err != nil {
		t.Fatalf("PlanAction: %v", err)
	}
	if diags := errorDiagnostics5(pr.Diagnostics); diags != "" || pr.Deferred != nil {
		t.Fatalf("PlanAction: diagnostics %q, deferred %v", diags, pr.Deferred)
	}

	invoke := func(s string) (progress []string, completed []*tfprotov5.Diagnostic) {
		t.Helper()
		stream, err := server.InvokeAction(ctx, &tfprotov5.InvokeActionRequest{
			ActionType: "tfcoremock_simple_resource",
			Config:     config(s),
		})
		if err != nil {
			t.Fatalf("InvokeAction(%s): %v", s, err)
		}
		done := 0
		for ev := range stream.Events {
			switch e := ev.Type.(type) {
			case tfprotov5.ProgressInvokeActionEventType:
				progress = append(progress, e.Message)
			case tfprotov5.CompletedInvokeActionEventType:
				completed = e.Diagnostics
				done++
			}
		}
		if done != 1 {
			t.Fatalf("InvokeAction(%s): %d completion events, want 1", s, done)
		}
		return progress, completed
	}

	progress, diags := invoke("hello")
	if len(progress) != 1 || !strings.Contains(progress[0], `"string":"hello"`) {
		t.Errorf("invoke hello: progress %q, want the one echo of the config", progress)
	}
	if len(diags) != 0 {
		t.Errorf("invoke hello: diagnostics %v, want none", diags)
	}

	_, diags = invoke("careful")
	if errorDiagnostics5(diags) != "" || len(diags) != 1 || diags[0].Severity != tfprotov5.DiagnosticSeverityWarning {
		t.Errorf("invoke careful: diagnostics %v, want the one warn_on_invoke warning", diags)
	}

	_, diags = invoke("boom")
	if got := errorDiagnostics5(diags); !strings.Contains(got, "action invocation failed") {
		t.Errorf("invoke boom: errors %q, want fail_on_invoke's", got)
	}
	warned := false
	for _, d := range diags {
		warned = warned || d.Severity == tfprotov5.DiagnosticSeverityWarning
	}
	if !warned {
		t.Error("invoke boom: the warning must ride beside the failure")
	}
}

// TestProtocol5WriteOnlyCapability pins that the write-only capability is
// carried and honoured on protocol 5's resource validation, as on 6's.
func TestProtocol5WriteOnlyCapability(t *testing.T) {
	ctx := context.Background()
	server := protocol5Server(t, "")
	resp := protocol5Schema(t, server)
	typ := resp.ResourceSchemas["tfcoremock_simple_resource"].ValueType()
	config := dynamic5(t, typ, object5(t, typ, map[string]tftypes.Value{
		"string_wo": tftypes.NewValue(tftypes.String, "secret"),
	}))

	for _, allowed := range []bool{true, false} {
		vr, err := server.ValidateResourceTypeConfig(ctx, &tfprotov5.ValidateResourceTypeConfigRequest{
			TypeName:           "tfcoremock_simple_resource",
			Config:             config,
			ClientCapabilities: &tfprotov5.ValidateResourceTypeConfigClientCapabilities{WriteOnlyAttributesAllowed: allowed},
		})
		if err != nil {
			t.Fatalf("ValidateResourceTypeConfig: %v", err)
		}
		if got := errorDiagnostics5(vr.Diagnostics) != ""; got == allowed {
			t.Errorf("write-only allowed=%v: error=%v (%s)", allowed, got, errorDiagnostics5(vr.Diagnostics))
		}
	}
}

func TestProtocolFromEnv(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
		err  bool
	}{
		{raw: "", want: 6},
		{raw: "6", want: 6},
		{raw: "5", want: 5},
		{raw: "7", err: true},
		{raw: "v5", err: true},
		{raw: " 5", err: true},
	} {
		t.Setenv(ProtocolEnvVarName, tc.raw)
		got, err := ProtocolFromEnv()
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("%s=%q: got (%d, %v), want (%d, error=%v)", ProtocolEnvVarName, tc.raw, got, err, tc.want, tc.err)
		}
	}
}

// TestCwdFileValidatesInTheWorkingDirectory pins tfcoremock_cwd_file on both
// protocols, as resource and as action: a relative path is resolved against
// the provider's working directory, and the error names that directory.
func TestCwdFileValidatesInTheWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "present.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s5 := protocol5Server(t, "")
	r5 := protocol5Schema(t, s5)
	plain6, err := providerserver.NewProtocol6WithError(NewForTesting("test", "")())()
	if err != nil {
		t.Fatal(err)
	}
	s6, ok := plain6.(interface {
		tfprotov6.ProviderServer
		tfprotov6.ActionServer
	})
	if !ok {
		t.Fatal("the protocol-6 server does not serve actions")
	}
	r6, err := s6.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}

	type check func(path string) string // returns the error diagnostics
	checks := map[string]check{
		"p5 resource": func(p string) string {
			typ := r5.ResourceSchemas["tfcoremock_cwd_file"].ValueType()
			vr, err := s5.ValidateResourceTypeConfig(ctx, &tfprotov5.ValidateResourceTypeConfigRequest{
				TypeName: "tfcoremock_cwd_file",
				Config:   dynamic5(t, typ, object5(t, typ, map[string]tftypes.Value{"path": tftypes.NewValue(tftypes.String, p)})),
			})
			if err != nil {
				t.Fatal(err)
			}
			return errorDiagnostics5(vr.Diagnostics)
		},
		"p5 action": func(p string) string {
			typ := actionSchema5(r5, "tfcoremock_cwd_file").ValueType()
			vr, err := s5.ValidateActionConfig(ctx, &tfprotov5.ValidateActionConfigRequest{
				ActionType: "tfcoremock_cwd_file",
				Config:     dynamic5(t, typ, object5(t, typ, map[string]tftypes.Value{"path": tftypes.NewValue(tftypes.String, p)})),
			})
			if err != nil {
				t.Fatal(err)
			}
			return errorDiagnostics5(vr.Diagnostics)
		},
		"p6 resource": func(p string) string {
			typ := r6.ResourceSchemas["tfcoremock_cwd_file"].ValueType()
			dv, err := tfprotov6.NewDynamicValue(typ, object5(t, typ, map[string]tftypes.Value{"path": tftypes.NewValue(tftypes.String, p)}))
			if err != nil {
				t.Fatal(err)
			}
			vr, err := s6.ValidateResourceConfig(ctx, &tfprotov6.ValidateResourceConfigRequest{TypeName: "tfcoremock_cwd_file", Config: &dv})
			if err != nil {
				t.Fatal(err)
			}
			return errorDiagnostics6(vr.Diagnostics)
		},
		"p6 action": func(p string) string {
			typ := r6.ActionSchemas["tfcoremock_cwd_file"].Schema.ValueType()
			dv, err := tfprotov6.NewDynamicValue(typ, object5(t, typ, map[string]tftypes.Value{"path": tftypes.NewValue(tftypes.String, p)}))
			if err != nil {
				t.Fatal(err)
			}
			vr, err := s6.ValidateActionConfig(ctx, &tfprotov6.ValidateActionConfigRequest{ActionType: "tfcoremock_cwd_file", Config: &dv})
			if err != nil {
				t.Fatal(err)
			}
			return errorDiagnostics6(vr.Diagnostics)
		},
	}

	for name, validate := range checks {
		if got := validate("present.txt"); got != "" {
			t.Errorf("%s, present relative file: %s", name, got)
		}
		got := validate("absent.txt")
		if !strings.Contains(got, "file not found") || !strings.Contains(got, dir) {
			t.Errorf("%s, absent relative file: %q, want a refusal naming %s", name, got, dir)
		}
	}
}

func errorDiagnostics6(diags []*tfprotov6.Diagnostic) string {
	var out []string
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return strings.Join(out, "; ")
}
