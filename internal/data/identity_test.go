// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package data

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func identityResource(id string) Resource {
	return Resource{
		Values: map[string]Value{
			"id": {String: &id},
		},
	}
}

// The identity value is hand-rolled rather than built from a struct, so the
// framework type-checks it against the declared identity schema. These cases
// pin the object type at each version; a mismatch with Resource.IdentitySchema
// is rejected at runtime rather than at compile time.
func TestResource_Identity(t *testing.T) {
	testCases := []struct {
		TestCase   string
		Version    int64
		Attributes map[string]tftypes.Value
	}{
		{
			TestCase: "version 0 records only the id",
			Version:  0,
			Attributes: map[string]tftypes.Value{
				"id": tftypes.NewValue(tftypes.String, "my-id"),
			},
		},
		{
			TestCase: "version 1 adds a urn derived from the id",
			Version:  1,
			Attributes: map[string]tftypes.Value{
				"id":  tftypes.NewValue(tftypes.String, "my-id"),
				"urn": tftypes.NewValue(tftypes.String, "tfcoremock://my-id"),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.TestCase, func(t *testing.T) {
			attributeTypes := make(map[string]tftypes.Type, len(testCase.Attributes))
			for name := range testCase.Attributes {
				attributeTypes[name] = tftypes.String
			}
			expected := tftypes.NewValue(tftypes.Object{AttributeTypes: attributeTypes}, testCase.Attributes)

			actual := identityResource("my-id").Identity(testCase.Version)
			if !actual.Equal(expected) {
				t.Errorf("expected %s but got %s", expected, actual)
			}
		})
	}
}

// A version beyond the newest one this provider knows about still produces the
// newest shape, so an out-of-range TFCOREMOCK_IDENTITY_SCHEMA_VERSION cannot
// produce a value that fails to type-check against the declared schema.
func TestResource_IdentityClampsToNewestVersion(t *testing.T) {
	newest := identityResource("my-id").Identity(1)
	beyond := identityResource("my-id").Identity(99)

	if !beyond.Type().Equal(newest.Type()) {
		t.Errorf("expected type %s but got %s", newest.Type(), beyond.Type())
	}
}

func TestUrn(t *testing.T) {
	if actual := Urn("my-id"); actual != "tfcoremock://my-id" {
		t.Errorf("expected tfcoremock://my-id but got %s", actual)
	}
}
