package hclhil

import (
	"fmt"
	"strings"

	"github.com/apparentlymart/go-cty/cty"
	"github.com/apparentlymart/go-zcl/zcl"
	hclast "github.com/hashicorp/hcl/hcl/ast"
)

// body is our implementation of zcl.Body in terms of an HCL ObjectList
type body struct {
	oli         *hclast.ObjectList
	hiddenNames map[string]struct{}
}

func (b *body) Content(schema *zcl.BodySchema) (*zcl.BodyContent, zcl.Diagnostics) {
	content, _, diags := b.content(schema, false)
	return content, diags
}

func (b *body) PartialContent(schema *zcl.BodySchema) (*zcl.BodyContent, zcl.Body, zcl.Diagnostics) {
	return b.content(schema, true)
}

func (b *body) content(schema *zcl.BodySchema, partial bool) (*zcl.BodyContent, zcl.Body, zcl.Diagnostics) {
	attrSchemas := make(map[string]zcl.AttributeSchema)
	blockSchemas := make(map[string]zcl.BlockHeaderSchema)
	for _, attrS := range schema.Attributes {
		attrSchemas[attrS.Name] = attrS
	}
	for _, blockS := range schema.Blocks {
		blockSchemas[blockS.Type] = blockS
	}

	attrs := make(zcl.Attributes)
	var blocks zcl.Blocks
	var diags zcl.Diagnostics

	namesUsed := make(map[string]struct{})

	for _, item := range b.oli.Items {
		if len(item.Keys) == 0 {
			// Should never happen, since we don't use b.oli.Filter
			diags = append(diags, &zcl.Diagnostic{
				Severity: zcl.DiagError,
				Summary:  "Invalid item",
				Detail:   "Somehow we have an HCL item with no keys. This should never happen.",
				Context:  rangeFromHCLPos(item.Pos()).Ptr(),
			})
			continue
		}

		name := item.Keys[0].Token.Value().(string)
		if _, hidden := b.hiddenNames[name]; hidden {
			continue
		}

		if _, isAttr := attrSchemas[name]; isAttr {
			if len(item.Keys) > 1 {
				diags = append(diags, &zcl.Diagnostic{
					Severity: zcl.DiagError,
					Summary:  "Unsupported block type",
					Detail:   fmt.Sprintf("Blocks of type %q are not expected here. Did you mean to define an attribute named %q?", name, name),
					Context:  rangeFromHCLPos(item.Pos()).Ptr(),
				})
				continue
			}

			diags = append(diags, insertAttr(attrs, item)...)
			namesUsed[name] = struct{}{}
		} else if blockS, isBlock := blockSchemas[name]; isBlock {
			obj, isBlock := item.Val.(*hclast.ObjectType)
			if !isBlock {
				diags = append(diags, &zcl.Diagnostic{
					Severity: zcl.DiagError,
					Summary:  "Unsupported attribute",
					Detail:   fmt.Sprintf("An attribute named %q is not expected here. Did you mean to define a block of type %q?", name, name),
					Context:  rangeFromHCLPos(item.Pos()).Ptr(),
				})
				continue
			}

			if item.Assign.Line != 0 {
				diags = append(diags, &zcl.Diagnostic{
					Severity: zcl.DiagWarning,
					Summary:  "Attribute syntax used for block",
					Detail:   fmt.Sprintf("Block %q is defined using attribute syntax, which is deprecated. The equals sign is not used to define a block.", name),
					Context:  rangeFromHCLPos(item.Pos()).Ptr(),
				})
			}

			labelKeys := item.Keys[1:]

			if len(labelKeys) > len(blockS.LabelNames) {
				if len(blockS.LabelNames) == 0 {
					diags = append(diags, &zcl.Diagnostic{
						Severity: zcl.DiagError,
						Summary:  fmt.Sprintf("Extraneous label for %s", name),
						Detail: fmt.Sprintf(
							"No labels are expected for %s blocks", name,
						),
						Context: rangeFromHCLPos(labelKeys[len(blockS.LabelNames)].Pos()).Ptr(),
					})
				} else {
					diags = append(diags, &zcl.Diagnostic{
						Severity: zcl.DiagError,
						Summary:  fmt.Sprintf("Extraneous label for %s", name),
						Detail: fmt.Sprintf(
							"Only %d labels (%s) are expected for %s blocks",
							len(blockS.LabelNames), strings.Join(blockS.LabelNames, ", "), name,
						),
						Context: rangeFromHCLPos(labelKeys[len(blockS.LabelNames)].Pos()).Ptr(),
					})
				}
				continue
			}
			if len(labelKeys) < len(blockS.LabelNames) {
				diags = append(diags, &zcl.Diagnostic{
					Severity: zcl.DiagError,
					Summary:  fmt.Sprintf("Missing %s for %s", blockS.LabelNames[len(labelKeys)], name),
					Detail: fmt.Sprintf(
						"All %s blocks must have %d labels (%s).",
						name, len(blockS.LabelNames), strings.Join(blockS.LabelNames, ", "),
					),
					Context: rangeFromHCLPos(obj.Pos()).Ptr(),
				})
				continue
			}

			var labels []string
			var labelRanges []zcl.Range
			if len(labelKeys) > 0 {
				labels = make([]string, len(labelKeys))
				labelRanges = make([]zcl.Range, len(labelKeys))
				for i, objKey := range labelKeys {
					labels[i] = objKey.Token.Value().(string)
					labelRanges[i] = rangeFromHCLPos(objKey.Pos())
				}
			}

			blocks = append(blocks, &zcl.Block{
				Type:   name,
				Labels: labels,
				Body: &body{
					oli: obj.List,
				},

				DefRange:    rangeFromHCLPos(obj.Pos()),
				TypeRange:   rangeFromHCLPos(item.Keys[0].Pos()),
				LabelRanges: labelRanges,
			})
			namesUsed[name] = struct{}{}

		} else {
			if !partial {
				if item.Assign.Line == 0 {
					diags = append(diags, &zcl.Diagnostic{
						Severity: zcl.DiagError,
						Summary:  "Unsupported block type",
						Detail:   fmt.Sprintf("Blocks of type %q are not expected here.", name),
						Context:  rangeFromHCLPos(item.Pos()).Ptr(),
					})
				} else {
					diags = append(diags, &zcl.Diagnostic{
						Severity: zcl.DiagError,
						Summary:  "Unsupported attribute",
						Detail:   fmt.Sprintf("An attribute named %q is not expected here.", name),
						Context:  rangeFromHCLPos(item.Pos()).Ptr(),
					})
				}
			}
		}
	}

	for _, attrS := range schema.Attributes {
		if !attrS.Required {
			continue
		}

		if attrs[attrS.Name] == nil {
			// HCL has a bug where it panics if you ask for the position of an
			// empty object list. This means we can't specify a subject for
			// this diagnostic in that case.
			var subject *zcl.Range
			if len(b.oli.Items) > 0 {
				subject = rangeFromHCLPos(b.oli.Pos()).Ptr()
			}
			diags = diags.Append(&zcl.Diagnostic{
				Severity: zcl.DiagError,
				Summary:  "Missing required attribute",
				Detail:   fmt.Sprintf("The attribute %q is required, but no definition was found.", attrS.Name),
				Subject:  subject,
			})
		}
	}

	var leftovers zcl.Body
	if partial {
		for name := range b.hiddenNames {
			namesUsed[name] = struct{}{}
		}
		leftovers = &body{
			oli:         b.oli,
			hiddenNames: namesUsed,
		}
	}

	return &zcl.BodyContent{
		Attributes: attrs,
		Blocks:     blocks,
	}, leftovers, diags
}

func (b *body) JustAttributes() (zcl.Attributes, zcl.Diagnostics) {
	items := b.oli.Items
	attrs := make(zcl.Attributes)
	var diags zcl.Diagnostics

	for _, item := range items {
		if len(item.Keys) == 0 {
			// Should never happen, since we don't use b.oli.Filter
			diags = append(diags, &zcl.Diagnostic{
				Severity: zcl.DiagError,
				Summary:  "Invalid item",
				Detail:   "Somehow we have an HCL item with no keys. This should never happen.",
				Context:  rangeFromHCLPos(item.Pos()).Ptr(),
			})
			continue
		}

		name := item.Keys[0].Token.Value().(string)
		if _, hidden := b.hiddenNames[name]; hidden {
			continue
		}

		if len(item.Keys) > 1 {
			name := item.Keys[0].Token.Value().(string)
			diags = append(diags, &zcl.Diagnostic{
				Severity: zcl.DiagError,
				Summary:  fmt.Sprintf("Unexpected %s block", name),
				Detail:   "Blocks are not allowed here.",
				Context:  rangeFromHCLPos(item.Pos()).Ptr(),
			})
			continue
		}

		diags = append(diags, insertAttr(attrs, item)...)
	}

	return attrs, diags
}

func insertAttr(attrs zcl.Attributes, item *hclast.ObjectItem) zcl.Diagnostics {
	name := item.Keys[0].Token.Value().(string)
	var diags zcl.Diagnostics

	if item.Assign.Line == 0 {
		diags = append(diags, &zcl.Diagnostic{
			Severity: zcl.DiagWarning,
			Summary:  "Block syntax used for attribute",
			Detail:   fmt.Sprintf("Attribute %q is defined using block syntax, which is deprecated. Use an equals sign after the attribute name instead.", name),
			Context:  rangeFromHCLPos(item.Pos()).Ptr(),
		})
	}

	if attrs[name] != nil {
		diags = append(diags, &zcl.Diagnostic{
			Severity: zcl.DiagError,
			Summary:  "Duplicate attribute definition",
			Detail: fmt.Sprintf(
				"Attribute %q was previously defined at %s",
				name, attrs[name].NameRange.String(),
			),
			Context: rangeFromHCLPos(item.Pos()).Ptr(),
		})
		return diags
	}

	attrs[name] = &zcl.Attribute{
		Name:      name,
		Expr:      &expression{src: item.Val},
		Range:     rangeFromHCLPos(item.Pos()),
		NameRange: rangeFromHCLPos(item.Keys[0].Pos()),
	}

	return diags
}

func (b *body) MissingItemRange() zcl.Range {
	if len(b.oli.Items) == 0 {
		// Can't return a sensible range in this case, because HCL panics if
		// you ask for the position of an empty list.
		return zcl.Range{
			Filename: "<unknown>",
		}
	}
	return rangeFromHCLPos(b.oli.Pos())
}

// body is our implementation of zcl.Body in terms of an HCL node, which may
// internally have strings to be interpreted as HIL templates.
type expression struct {
	src hclast.Node
}

func (e *expression) Value(ctx *zcl.EvalContext) (cty.Value, zcl.Diagnostics) {
	// TODO: Implement
	return cty.NilVal, nil
}

func (e *expression) Range() zcl.Range {
	return rangeFromHCLPos(e.src.Pos())
}
func (e *expression) StartRange() zcl.Range {
	return rangeFromHCLPos(e.src.Pos())
}
