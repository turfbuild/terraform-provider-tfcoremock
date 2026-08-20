// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// fakePrivateData is a test stand-in for the framework's private-state
// accessor (whose concrete type is framework-internal and cannot be
// constructed here). A nil map models the absent private state a create sees.
type fakePrivateData struct {
	data map[string][]byte
}

func (f *fakePrivateData) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	if f == nil || f.data == nil {
		return nil, nil
	}
	return f.data[key], nil
}

func (f *fakePrivateData) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	if f.data == nil {
		f.data = make(map[string][]byte)
	}
	f.data[key] = value
	return nil
}

func TestResource_assertPrivateMarker(t *testing.T) {
	present := &fakePrivateData{data: map[string][]byte{privateMarkerKey: markerBytes("my-id")}}
	absent := &fakePrivateData{}

	testCases := []struct {
		TestCase  string
		Strict    bool
		Private   *fakePrivateData
		WantID    string
		WantError bool
	}{
		{
			TestCase: "a create carries no marker",
			Strict:   true,
			Private:  absent,
		},
		{
			// The create half of a replace: the object being destroyed must not
			// lend its private state to the one taking its place.
			TestCase:  "a create carrying a marker is rejected",
			Strict:    true,
			Private:   present,
			WantError: true,
		},
		{
			TestCase: "an existing resource carries its own marker",
			Strict:   true,
			Private:  present,
			WantID:   "my-id",
		},
		{
			TestCase:  "an existing resource carrying no marker is rejected",
			Strict:    true,
			Private:   absent,
			WantID:    "my-id",
			WantError: true,
		},
		{
			TestCase:  "a nil accessor is treated as absent, not dereferenced",
			Strict:    true,
			Private:   nil,
			WantID:    "my-id",
			WantError: true,
		},
		{
			TestCase:  "an existing resource carrying another object's marker is rejected",
			Strict:    true,
			Private:   &fakePrivateData{data: map[string][]byte{privateMarkerKey: markerBytes("another-id")}},
			WantID:    "my-id",
			WantError: true,
		},
		{
			TestCase:  "a marker that does not parse is rejected",
			Strict:    true,
			Private:   &fakePrivateData{data: map[string][]byte{privateMarkerKey: []byte("not json")}},
			WantID:    "my-id",
			WantError: true,
		},
		{
			// Every assertion is off unless strict_private_state is set, so the
			// provider's default behavior is unchanged.
			TestCase: "nothing is asserted when strict_private_state is off",
			Strict:   false,
			Private:  present,
			WantID:   "another-id",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.TestCase, func(t *testing.T) {
			var diags diag.Diagnostics
			Resource{StrictPrivateState: testCase.Strict}.assertPrivateMarker(
				context.Background(), "test", testCase.Private, testCase.WantID, &diags)

			if diags.HasError() != testCase.WantError {
				t.Errorf("expected error %v but got diagnostics %v", testCase.WantError, diags)
			}
		})
	}
}

// The stamp writes the marker the assertions expect back, and only when
// strict_private_state is on — the provider's default behavior is unchanged.
func TestResource_stampPrivateMarker(t *testing.T) {
	private := &fakePrivateData{}
	var diags diag.Diagnostics
	Resource{StrictPrivateState: true}.stampPrivateMarker(context.Background(), private, "my-id", &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	Resource{StrictPrivateState: true}.assertPrivateMarker(context.Background(), "test", private, "my-id", &diags)
	if diags.HasError() {
		t.Errorf("the stamped marker must satisfy the assertion: %v", diags)
	}

	off := &fakePrivateData{}
	Resource{StrictPrivateState: false}.stampPrivateMarker(context.Background(), off, "my-id", &diags)
	if len(off.data) != 0 {
		t.Errorf("nothing should be stamped when strict_private_state is off, got %v", off.data)
	}
}

// A read that finds the object gone must leave no marker behind: the framework
// pre-populates the response's private state from the request, and the client
// carries what the read returns into the plan of the create that follows —
// where a marker beside a null prior is exactly what the assertion rejects.
func TestResource_clearPrivateMarker(t *testing.T) {
	private := &fakePrivateData{data: map[string][]byte{privateMarkerKey: markerBytes("my-id")}}
	var diags diag.Diagnostics
	Resource{StrictPrivateState: true}.clearPrivateMarker(context.Background(), private, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	// The cleared state is what a create must carry: no marker at all.
	Resource{StrictPrivateState: true}.assertPrivateMarker(context.Background(), "test", private, "", &diags)
	if diags.HasError() {
		t.Errorf("a cleared marker must satisfy the create rule: %v", diags)
	}

	off := &fakePrivateData{data: map[string][]byte{privateMarkerKey: markerBytes("my-id")}}
	Resource{StrictPrivateState: false}.clearPrivateMarker(context.Background(), off, &diags)
	if len(off.data) != 1 {
		t.Errorf("nothing should be cleared when strict_private_state is off, got %v", off.data)
	}
}
