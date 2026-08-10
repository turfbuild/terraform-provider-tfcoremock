// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// identitySchema returns the identity schema a resource declares at the given
// version, as the framework would ask for it.
func identitySchema(t *testing.T, version int64) resource.IdentitySchemaResponse {
	t.Helper()

	response := resource.IdentitySchemaResponse{}
	Resource{IdentitySchemaVersion: version}.IdentitySchema(context.Background(), resource.IdentitySchemaRequest{}, &response)
	if response.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", response.Diagnostics)
	}
	return response
}

// identityValue builds an identity as the framework hands one to a resource.
func identityValue(t *testing.T, version int64, attributes map[string]tftypes.Value) *tfsdk.ResourceIdentity {
	t.Helper()

	schema := identitySchema(t, version).IdentitySchema
	typ := schema.Type().TerraformType(context.Background())

	raw := tftypes.NewValue(typ, nil)
	if attributes != nil {
		raw = tftypes.NewValue(typ, attributes)
	}

	return &tfsdk.ResourceIdentity{Raw: raw, Schema: schema}
}

// The declared schema and the value data.Resource.Identity builds must agree,
// or the framework rejects the value at runtime.
func TestResource_IdentitySchema(t *testing.T) {
	testCases := []struct {
		TestCase   string
		Version    int64
		Attributes []string
	}{
		{TestCase: "version 0 declares only the id", Version: 0, Attributes: []string{"id"}},
		{TestCase: "version 1 also declares the urn", Version: 1, Attributes: []string{"id", "urn"}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.TestCase, func(t *testing.T) {
			schema := identitySchema(t, testCase.Version).IdentitySchema

			if schema.GetVersion() != testCase.Version {
				t.Errorf("expected version %d but got %d", testCase.Version, schema.GetVersion())
			}

			attributes := schema.GetAttributes()
			if len(attributes) != len(testCase.Attributes) {
				t.Fatalf("expected attributes %v but got %v", testCase.Attributes, attributes)
			}
			for _, name := range testCase.Attributes {
				if _, ok := attributes[name]; !ok {
					t.Errorf("expected attribute %s to be declared", name)
				}
			}
		})
	}
}

// A resource at version 0 has nothing to upgrade from, so it must not offer an
// upgrader — the framework would otherwise advertise a migration to a version
// that is also the current one.
func TestResource_UpgradeIdentity_registration(t *testing.T) {
	if upgraders := (Resource{IdentitySchemaVersion: 0}).UpgradeIdentity(context.Background()); len(upgraders) != 0 {
		t.Errorf("expected no upgraders at version 0 but got %v", upgraders)
	}

	upgraders := Resource{IdentitySchemaVersion: 1}.UpgradeIdentity(context.Background())
	if _, ok := upgraders[0]; !ok || len(upgraders) != 1 {
		t.Errorf("expected exactly one upgrader, from version 0, but got %v", upgraders)
	}
}

// The upgrade must reconstruct the whole identity: the framework copies no
// prior data across, so anything the upgrader omits is lost.
func TestResource_UpgradeIdentity_derivesUrnFromId(t *testing.T) {
	upgrader := Resource{IdentitySchemaVersion: 1}.UpgradeIdentity(context.Background())[0]

	response := resource.UpgradeIdentityResponse{Identity: identityValue(t, 1, nil)}
	upgrader.IdentityUpgrader(context.Background(), resource.UpgradeIdentityRequest{
		RawIdentity: &tfprotov6.RawState{JSON: []byte(`{"id":"my-id"}`)},
	}, &response)

	if response.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", response.Diagnostics)
	}

	expected := identityValue(t, 1, map[string]tftypes.Value{
		"id":  tftypes.NewValue(tftypes.String, "my-id"),
		"urn": tftypes.NewValue(tftypes.String, "tfcoremock://my-id"),
	})
	if !response.Identity.Raw.Equal(expected.Raw) {
		t.Errorf("expected %s but got %s", expected.Raw, response.Identity.Raw)
	}
}

func TestResource_UpgradeIdentity_missingPriorIdentity(t *testing.T) {
	upgrader := Resource{IdentitySchemaVersion: 1}.UpgradeIdentity(context.Background())[0]

	response := resource.UpgradeIdentityResponse{Identity: identityValue(t, 1, nil)}
	upgrader.IdentityUpgrader(context.Background(), resource.UpgradeIdentityRequest{}, &response)

	if !response.Diagnostics.HasError() {
		t.Error("expected a diagnostic when no prior identity was supplied")
	}
}

func TestResource_assertPriorIdentity(t *testing.T) {
	present := identityValue(t, 0, map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.String, "my-id"),
	})
	absent := identityValue(t, 0, nil)

	testCases := []struct {
		TestCase  string
		Strict    bool
		Identity  *tfsdk.ResourceIdentity
		WantID    string
		WantError bool
	}{
		{
			TestCase: "a create carries no identity",
			Strict:   true,
			Identity: absent,
		},
		{
			// The create half of a replace: the object being destroyed must not
			// lend its identity to the one taking its place.
			TestCase:  "a create carrying an identity is rejected",
			Strict:    true,
			Identity:  present,
			WantError: true,
		},
		{
			TestCase: "an existing resource carries its own identity",
			Strict:   true,
			Identity: present,
			WantID:   "my-id",
		},
		{
			TestCase:  "an existing resource carrying no identity is rejected",
			Strict:    true,
			Identity:  absent,
			WantID:    "my-id",
			WantError: true,
		},
		{
			TestCase:  "a nil identity is treated as absent, not dereferenced",
			Strict:    true,
			Identity:  nil,
			WantID:    "my-id",
			WantError: true,
		},
		{
			TestCase:  "an existing resource carrying another object's identity is rejected",
			Strict:    true,
			Identity:  present,
			WantID:    "another-id",
			WantError: true,
		},
		{
			// Every assertion is off unless strict_identity is set, so the
			// provider's default behavior is unchanged.
			TestCase: "nothing is asserted when strict_identity is off",
			Strict:   false,
			Identity: present,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.TestCase, func(t *testing.T) {
			var diags diag.Diagnostics
			Resource{StrictIdentity: testCase.Strict}.assertPriorIdentity(
				context.Background(), "test", testCase.Identity, testCase.WantID, &diags)

			if diags.HasError() != testCase.WantError {
				t.Errorf("expected error %v but got diagnostics %v", testCase.WantError, diags)
			}
		})
	}
}
