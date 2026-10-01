// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema/simple"
)

// TestDeferralFiresDataSourceIsItsOwnMark proves defer_on_read and
// defer_changes mark independently under defer_until_reload, as fail_on_read's
// two strikes do: a data source deferring an id writes its own mark, and the
// managed resource's mark keeps the name it always had.
func TestDeferralFiresDataSourceIsItsOwnMark(t *testing.T) {
	dir := t.TempDir()
	r := Resource{DeferChanges: []string{"shared"}, DeferUntilReloadDir: dir}

	if !deferralFires([]string{"shared"}, dir, "read-data", "shared") {
		t.Fatal("the data source's first read must defer")
	}
	if !r.deferralFires("shared") {
		t.Fatal("the managed resource must defer too: its mark is not the data source's")
	}
	for _, name := range []string{"shared", "read-data-shared"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("mark %s: %v", name, err)
		}
	}

	// A mark another process wrote clears the injection for this one.
	if err := os.WriteFile(filepath.Join(dir, "read-data-shared"), []byte("another-process\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if deferralFires([]string{"shared"}, dir, "read-data", "shared") {
		t.Fatal("a mark written by another process must clear the data source's deferral")
	}
	if !r.deferralFires("shared") {
		t.Fatal("clearing the data source's mark must leave the managed resource deferring")
	}
}

// TestDataSourceReadDefers proves defer_on_read reaches Read: the answer is a
// PROVIDER_CONFIG_UNKNOWN deferral with the state wholly unknown when the
// client allows deferrals, and "Invalid data source deferral" when it does not.
func TestDataSourceReadDefers(t *testing.T) {
	ctx := context.Background()
	sch, err := simple.Schema.ToTerraformDataSourceSchema()
	if err != nil {
		t.Fatal(err)
	}
	typ := sch.Type().TerraformType(ctx).(tftypes.Object)
	attrs := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	attrs["id"] = tftypes.NewValue(tftypes.String, "later")
	config := tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(typ, attrs)}
	d := DataSource{Name: "tfcoremock_simple_resource", InternalSchema: simple.Schema, DeferOnRead: []string{"later"}}

	for _, allowed := range []bool{true, false} {
		req := datasource.ReadRequest{Config: config}
		req.ClientCapabilities.DeferralAllowed = allowed
		resp := datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
		d.Read(ctx, req, &resp)

		if !allowed {
			if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Invalid data source deferral" {
				t.Errorf("deferral not allowed: diagnostics %v; want Invalid data source deferral", resp.Diagnostics)
			}
			if resp.Deferred != nil {
				t.Errorf("deferral not allowed: deferred %v; want none", resp.Deferred)
			}
			continue
		}
		if resp.Diagnostics.HasError() {
			t.Fatalf("deferral allowed: %v", resp.Diagnostics)
		}
		if resp.Deferred == nil || resp.Deferred.Reason != datasource.DeferredReasonProviderConfigUnknown {
			t.Errorf("deferral allowed: deferred %v; want PROVIDER_CONFIG_UNKNOWN", resp.Deferred)
		}
		if resp.State.Raw.IsKnown() {
			t.Errorf("deferral allowed: state %v; want it wholly unknown", resp.State.Raw)
		}
	}
}
