// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-provider-tfcoremock/internal/schema"
)

// validateMustExist checks that every top-level string attribute marked
// MustExist names a file that exists. A relative path is resolved by the
// operating system against the provider process's working directory, and the
// error names that directory, so a client that launched the plugin in the
// wrong place is told where it looked.
func validateMustExist(ctx context.Context, s schema.Schema, config tfsdk.Config, diags *diag.Diagnostics) {
	names := make([]string, 0, len(s.Attributes))
	for name, attr := range s.Attributes {
		if attr.MustExist && attr.Type == schema.String {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)

	for _, name := range names {
		var value types.String
		diags.Append(config.GetAttribute(ctx, path.Root(name), &value)...)
		if diags.HasError() {
			return
		}
		if value.IsNull() || value.IsUnknown() {
			continue
		}
		file := value.ValueString()
		if _, err := os.Stat(file); err != nil {
			wd, wdErr := os.Getwd()
			if wdErr != nil {
				wd = fmt.Sprintf("(unknown: %v)", wdErr)
			}
			diags.AddAttributeError(path.Root(name), "file not found",
				fmt.Sprintf("%q does not exist relative to the provider's working directory %s: %v", file, wd, err))
		}
	}
}
