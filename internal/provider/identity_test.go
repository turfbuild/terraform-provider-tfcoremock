// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
)

// A bad value falls back to version 0 rather than failing to start: the
// variable exists to drive tests, and a provider that refused to launch over a
// typo would be harder to diagnose than one that reports the version it chose.
func TestIdentitySchemaVersionFromEnv(t *testing.T) {
	testCases := []struct {
		TestCase string
		Value    string
		Expected int64
	}{
		{TestCase: "unset defaults to the original version", Value: "", Expected: 0},
		{TestCase: "zero is honored", Value: "0", Expected: 0},
		{TestCase: "a version is honored", Value: "1", Expected: 1},
		{TestCase: "a non-numeric value falls back", Value: "latest", Expected: 0},
		{TestCase: "a negative version falls back", Value: "-1", Expected: 0},
	}

	for _, testCase := range testCases {
		t.Run(testCase.TestCase, func(t *testing.T) {
			t.Setenv(identitySchemaVersionEnvVarName, testCase.Value)

			if actual := identitySchemaVersionFromEnv(); actual != testCase.Expected {
				t.Errorf("expected %d but got %d", testCase.Expected, actual)
			}
		})
	}
}
