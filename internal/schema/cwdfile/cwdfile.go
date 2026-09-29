// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package cwdfile

import "github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"

var (
	description         = "A resource whose configuration names a file that must exist when it is validated, resolved against the provider's working directory. It models a provider that reads its inputs (playbooks, key files) from a relative path, so a client can be held to launching the plugin in the configuration's directory."
	markdownDescription = "A resource whose configuration names a file that must exist when it is validated, resolved against the provider's working directory. It models a provider that reads its inputs (playbooks, key files) from a relative path, so a client can be held to launching the plugin in the configuration's directory."

	Schema = schema.Schema{
		Description:         description,
		MarkdownDescription: markdownDescription,
		Attributes: map[string]schema.Attribute{
			"path": {
				Description:         "A file that must exist at validation, relative to the provider's working directory unless absolute.",
				MarkdownDescription: "A file that must exist at validation, relative to the provider's working directory unless absolute.",
				Required:            true,
				Type:                schema.String,
				MustExist:           true,
			},
			// The selector the action injections (fail_on_invoke,
			// warn_on_invoke) key off, as on the simple resource.
			"string": {
				Description:         "An optional string attribute.",
				MarkdownDescription: "An optional string attribute.",
				Optional:            true,
				Type:                schema.String,
			},
		},
	}
)
