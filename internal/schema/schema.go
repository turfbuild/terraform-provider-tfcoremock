// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package schema

import (
	"errors"

	action_schema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	datasource_schema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	resource_schema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	resource_schema_planmodifier "github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	resource_schema_stringplanmodifier "github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// Schema defines an internal representation of a Terraform schema.
//
// It is designed to be read dynamically from a JSON object, allowing schemas,
// blocks and attributes to be defined dynamically by the user of the provider.
type Schema struct {
	Description         string               `json:"-"` // Dynamic resources don't need descriptions so hide them from the exposed JSON schema.
	MarkdownDescription string               `json:"-"` // Dynamic resources don't need descriptions so hide them from the exposed JSON schema.
	Attributes          map[string]Attribute `json:"attributes"`
	Blocks              map[string]Block     `json:"blocks"`
}

// AllAttributes returns the attributes for the dynamic schema, plus the
// required ID attribute that is attached to tfsdk.Schema objects automatically.
func (schema Schema) AllAttributes() map[string]Attribute {
	attributes := make(map[string]Attribute, 0)
	for key, attribute := range schema.Attributes {
		attributes[key] = attribute
	}
	attributes["id"] = Attribute{
		Type:     String,
		Optional: false,
		Required: false,
		Computed: true,
	}
	return attributes
}

// ToTerraformResourceSchema converts out representation of a Schema into a
// Terraform SDK tfsdk.Schema. It automatically creates and attaches a computed
// type called `id` that is required by every resource and data source in this
// provider.
func (schema Schema) ToTerraformResourceSchema() (resource_schema.Schema, error) {
	out := resource_schema.Schema{
		Description:         schema.Description,
		MarkdownDescription: schema.MarkdownDescription,
	}

	var err error
	if err = schema.validateAttributes(); err != nil {
		return out, err
	}

	if out.Attributes, err = attributesToTerraformResourceAttributes(schema.Attributes); err != nil {
		return out, err
	}
	out.Attributes["id"] = resource_schema.StringAttribute{
		Required: false,
		Optional: true,
		Computed: true,
		PlanModifiers: []resource_schema_planmodifier.String{
			resource_schema_stringplanmodifier.UseStateForUnknown(),
			resource_schema_stringplanmodifier.RequiresReplace(),
		},
	}

	if out.Blocks, err = blocksToTerraformResourceBlocks(schema.Blocks); err != nil {
		return out, err
	}

	return out, nil
}

// ToTerraformDataSourceSchema converts our representation of a Schema into a
// Terraform SDK tfsdk.Schema. It automatically creates and attaches a required
// attribute called `id` that is required by every resource and data source in
// this provider.
func (schema Schema) ToTerraformDataSourceSchema() (datasource_schema.Schema, error) {
	out := datasource_schema.Schema{
		Description:         schema.Description,
		MarkdownDescription: schema.MarkdownDescription,
	}

	var err error
	if err = schema.validateAttributes(); err != nil {
		return out, err
	}

	if out.Attributes, err = attributesToTerraformDataSourceAttributes(schema.Attributes); err != nil {
		return out, err
	}

	out.Attributes["id"] = datasource_schema.StringAttribute{
		Required: true,
		Optional: false,
		Computed: false,
	}

	if out.Blocks, err = blocksToTerraformDataSourceBlocks(schema.Blocks); err != nil {
		return out, err
	}

	return out, nil
}

func (schema Schema) ToTerraformActionSchema() (action_schema.Schema, error) {
	out := action_schema.Schema{
		Description:         schema.Description,
		MarkdownDescription: schema.MarkdownDescription,
	}

	var err error
	if err = schema.validateAttributes(); err != nil {
		return out, err
	}

	if out.Attributes, err = attributesToTerraformActionAttributes(schema.Attributes); err != nil {
		return out, err
	}

	if out.Blocks, err = blocksToTerraformActionBlocks(schema.Blocks); err != nil {
		return out, err
	}

	return out, nil
}

func (schema Schema) validateAttributes() error {
	if _, ok := schema.Attributes["id"]; ok {
		return errors.New("top level dynamic objects cannot define a value called `id` as the provider will generate an identifier for them")
	}
	return nil
}

// WithoutNestedAttributes returns a copy of the schema in which every list,
// map, set and object attribute — at any depth, inside blocks too — is built
// as a plain collection or object type rather than as a nested attribute.
//
// Plugin protocol 5 cannot express nested attributes: the framework refuses
// the whole provider schema if one is present. So a provider served over
// protocol 5 hands out this form. The cost is the one skip_nested_metadata
// always carried: an object's attributes are all required in configuration,
// as a plain object type's are.
func (schema Schema) WithoutNestedAttributes() Schema {
	out := schema
	out.Attributes = attributesWithoutNesting(schema.Attributes)
	out.Blocks = blocksWithoutNesting(schema.Blocks)
	return out
}

func attributesWithoutNesting(attributes map[string]Attribute) map[string]Attribute {
	if attributes == nil {
		return nil
	}
	out := make(map[string]Attribute, len(attributes))
	for name, attribute := range attributes {
		out[name] = attribute.withoutNesting()
	}
	return out
}

func blocksWithoutNesting(blocks map[string]Block) map[string]Block {
	if blocks == nil {
		return nil
	}
	out := make(map[string]Block, len(blocks))
	for name, block := range blocks {
		block.Attributes = attributesWithoutNesting(block.Attributes)
		block.Blocks = blocksWithoutNesting(block.Blocks)
		out[name] = block
	}
	return out
}

func (a Attribute) withoutNesting() Attribute {
	switch a.Type {
	case List, Map, Set, Object:
		a.SkipNestedMetadata = true
	}
	if a.List != nil {
		elem := a.List.withoutNesting()
		a.List = &elem
	}
	if a.Map != nil {
		elem := a.Map.withoutNesting()
		a.Map = &elem
	}
	if a.Set != nil {
		elem := a.Set.withoutNesting()
		a.Set = &elem
	}
	a.Object = attributesWithoutNesting(a.Object)
	return a
}
