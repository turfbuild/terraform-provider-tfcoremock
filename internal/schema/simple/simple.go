// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package simple

import "github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"

var (
	description         = "A simple resource that holds optional attributes for the five basic types: bool, number, string, float and integer."
	markdownDescription = "A simple resource that holds optional attributes for the five basic types: `bool`, `number`, `string`, `float`, and `integer`."

	Schema = schema.Schema{
		Description:         description,
		MarkdownDescription: markdownDescription,
		Attributes: map[string]schema.Attribute{
			"bool": {
				Description:         "An optional boolean attribute, can be true or false.",
				MarkdownDescription: "An optional boolean attribute, can be true or false.",
				Optional:            true,
				Type:                schema.Boolean,
			},
			"number": {
				Description:         "An optional number attribute, can be an integer or a float.",
				MarkdownDescription: "An optional number attribute, can be an integer or a float.",
				Optional:            true,
				Type:                schema.Number,
			},
			"string": {
				Description:         "An optional string attribute.",
				MarkdownDescription: "An optional string attribute.",
				Optional:            true,
				Type:                schema.String,
			},
			"float": {
				Description:         "An optional float attribute.",
				MarkdownDescription: "An optional float attribute.",
				Optional:            true,
				Type:                schema.Float,
			},
			"integer": {
				Description:         "An optional integer attribute.",
				MarkdownDescription: "An optional integer attribute.",
				Optional:            true,
				Type:                schema.Integer,
			},
			// A write-only attribute, so that the "value reaches the provider
			// but never reaches state" contract can be exercised end to end. No
			// released provider offers one without cloud credentials, which is
			// why it lives here.
			//
			// Replace is set because the interesting case is the one a
			// write-only attribute makes structurally invisible: prior and
			// planned are both null, so nothing downstream can tell from the
			// values alone that the secret changed. ModifyPlan compares what
			// was actually stored and reports this path as requires-replace.
			"string_wo": {
				Description:         "An optional write-only string attribute. Its value reaches the provider on every request but is never written to state.",
				MarkdownDescription: "An optional write-only string attribute. Its value reaches the provider on every request but is never written to state.",
				Optional:            true,
				Type:                schema.String,
				WriteOnly:           true,
				Replace:             true,
			},
		},
	}
)
