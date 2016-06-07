package graphql

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/graphql-go/graphql/gqlerrors"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/kinds"
	"github.com/graphql-go/graphql/language/printer"
	"github.com/graphql-go/graphql/language/visitor"
)

// SpecifiedRules set includes all validation rules defined by the GraphQL spec.
var SpecifiedRules = []ValidationRuleFn{
	ArgumentsOfCorrectTypeRule,
	DefaultValuesOfCorrectTypeRule,
	FieldsOnCorrectTypeRule,
	FragmentsOnCompositeTypesRule,
	KnownArgumentNamesRule,
	KnownDirectivesRule,
	KnownFragmentNamesRule,
	KnownTypeNamesRule,
	LoneAnonymousOperationRule,
	NoFragmentCyclesRule,
	NoUndefinedVariablesRule,
	NoUnusedFragmentsRule,
	NoUnusedVariablesRule,
	OverlappingFieldsCanBeMergedRule,
	PossibleFragmentSpreadsRule,
	ProvidedNonNullArgumentsRule,
	ScalarLeafsRule,
	UniqueArgumentNamesRule,
	UniqueFragmentNamesRule,
	UniqueInputFieldNamesRule,
	UniqueOperationNamesRule,
	UniqueVariableNamesRule,
	VariablesAreInputTypesRule,
	VariablesInAllowedPositionRule,
}

type ValidationRuleInstance struct {
	VisitorOpts *visitor.VisitorOptions
}
type ValidationRuleFn func(context *ValidationContext) *ValidationRuleInstance

func newValidationError(message string, nodes []ast.Node) *gqlerrors.Error {
	return gqlerrors.NewError(
		message,
		nodes,
		"",
		nil,
		[]int{},
		nil, // TODO: this is interim, until we port "better-error-messages-for-inputs"
	)
}

func reportError(context *ValidationContext, message string, nodes []ast.Node) (string, interface{}) {
	context.ReportError(newValidationError(message, nodes))
	return visitor.ActionNoChange, nil
}

// ArgumentsOfCorrectTypeRule Argument values of correct type
//
// A GraphQL document is only valid if all field argument literal values are
// of the type expected by their position.
func ArgumentsOfCorrectTypeRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Argument: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if argAST, ok := p.Node.(*ast.Argument); ok {
						value := argAST.Value
						argDef := context.Argument()
						if argDef != nil {
							isValid, messages := isValidLiteralValue(argDef.Type, value)
							if !isValid {
								argNameValue := ""
								if argAST.Name != nil {
									argNameValue = argAST.Name.Value
								}

								messagesStr := ""
								if len(messages) > 0 {
									messagesStr = "\n" + strings.Join(messages, "\n")
								}
								reportError(
									context,
									fmt.Sprintf(`Argument "%v" has invalid value %v.%v`,
										argNameValue, printer.Print(value), messagesStr),
									[]ast.Node{value},
								)
							}

						}
					}
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// DefaultValuesOfCorrectTypeRule Variable default values of correct type
//
// A GraphQL document is only valid if all variable default values are of the
// type expected by their definition.
func DefaultValuesOfCorrectTypeRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.VariableDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if varDefAST, ok := p.Node.(*ast.VariableDefinition); ok {
						name := ""
						if varDefAST.Variable != nil && varDefAST.Variable.Name != nil {
							name = varDefAST.Variable.Name.Value
						}
						defaultValue := varDefAST.DefaultValue
						ttype := context.InputType()

						if ttype, ok := ttype.(*NonNull); ok && defaultValue != nil {
							reportError(
								context,
								fmt.Sprintf(`Variable "$%v" of type "%v" is required and will not use the default value. Perhaps you meant to use type "%v".`,
									name, ttype, ttype.OfType),
								[]ast.Node{defaultValue},
							)
						}
						isValid, messages := isValidLiteralValue(ttype, defaultValue)
						if ttype != nil && defaultValue != nil && !isValid {
							messagesStr := ""
							if len(messages) > 0 {
								messagesStr = "\n" + strings.Join(messages, "\n")
							}
							reportError(
								context,
								fmt.Sprintf(`Variable "$%v" has invalid default value: %v.%v`,
									name, printer.Print(defaultValue), messagesStr),
								[]ast.Node{defaultValue},
							)
						}
					}
					return visitor.ActionSkip, nil
				},
			},
			kinds.SelectionSet: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.FragmentDefinition: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}
func quoteStrings(slice []string) []string {
	quoted := []string{}
	for _, s := range slice {
		quoted = append(quoted, fmt.Sprintf(`"%v"`, s))
	}
	return quoted
}

// quotedOrList Given [ A, B, C ] return '"A", "B", or "C"'.
// Notice oxford comma
func quotedOrList(slice []string) string {
	maxLength := 5
	if len(slice) == 0 {
		return ""
	}
	quoted := quoteStrings(slice)
	if maxLength > len(quoted) {
		maxLength = len(quoted)
	}
	if maxLength > 2 {
		return fmt.Sprintf("%v, or %v", strings.Join(quoted[0:maxLength-1], ", "), quoted[maxLength-1])
	}
	if maxLength > 1 {
		return fmt.Sprintf("%v or %v", strings.Join(quoted[0:maxLength-1], ", "), quoted[maxLength-1])
	}
	return quoted[0]
}
func UndefinedFieldMessage(fieldName string, ttypeName string, suggestedTypeNames []string, suggestedFieldNames []string) string {
	message := fmt.Sprintf(`Cannot query field "%v" on type "%v".`, fieldName, ttypeName)
	if len(suggestedTypeNames) > 0 {
		message = fmt.Sprintf(`%v Did you mean to use an inline fragment on %v?`, message, quotedOrList(suggestedTypeNames))
	} else if len(suggestedFieldNames) > 0 {
		message = fmt.Sprintf(`%v Did you mean %v?`, message, quotedOrList(suggestedFieldNames))
	}
	return message
}

// FieldsOnCorrectTypeRule Fields on correct type
//
// A GraphQL document is only valid if all fields selected are defined by the
// parent type, or are an allowed meta field such as __typenamme
func FieldsOnCorrectTypeRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Field: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					var action = visitor.ActionNoChange
					var result interface{}
					if node, ok := p.Node.(*ast.Field); ok {
						ttype := context.ParentType()

						if ttype != nil {
							fieldDef := context.FieldDef()
							if fieldDef == nil {
								// This field doesn't exist, lets look for suggestions.
								nodeName := ""
								if node.Name != nil {
									nodeName = node.Name.Value
								}
								// First determine if there are any suggested types to condition on.
								suggestedTypeNames := getSuggestedTypeNames(context.Schema(), ttype, nodeName)

								// If there are no suggested types, then perhaps this was a typo?
								suggestedFieldNames := []string{}
								if len(suggestedTypeNames) == 0 {
									suggestedFieldNames = getSuggestedFieldNames(context.Schema(), ttype, nodeName)
								}

								reportError(
									context,
									UndefinedFieldMessage(nodeName, ttype.Name(), suggestedTypeNames, suggestedFieldNames),
									[]ast.Node{node},
								)
							}
						}
					}
					return action, result
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// getSuggestedTypeNames Go through all of the implementations of type, as well as the interfaces
// that they implement. If any of those types include the provided field,
// suggest them, sorted by how often the type is referenced,  starting
// with Interfaces.
func getSuggestedTypeNames(schema *Schema, ttype Output, fieldName string) []string {

	possibleTypes := schema.PossibleTypes(ttype)

	suggestedObjectTypes := []string{}
	suggestedInterfaces := []*suggestedInterface{}
	// stores a map of interface name => index in suggestedInterfaces
	suggestedInterfaceMap := map[string]int{}
	// stores a maps of object name => true to remove duplicates from results
	suggestedObjectMap := map[string]bool{}

	for _, possibleType := range possibleTypes {
		if field, ok := possibleType.Fields()[fieldName]; !ok || field == nil {
			continue
		}
		// This object type defines this field.
		suggestedObjectTypes = append(suggestedObjectTypes, possibleType.Name())
		suggestedObjectMap[possibleType.Name()] = true

		for _, possibleInterface := range possibleType.Interfaces() {
			if field, ok := possibleInterface.Fields()[fieldName]; !ok || field == nil {
				continue
			}

			// This interface type defines this field.

			// - find the index of the suggestedInterface and retrieving the interface
			// - increase count
			index, ok := suggestedInterfaceMap[possibleInterface.Name()]
			if !ok {
				suggestedInterfaces = append(suggestedInterfaces, &suggestedInterface{
					name:  possibleInterface.Name(),
					count: 0,
				})
				index = len(suggestedInterfaces) - 1
				suggestedInterfaceMap[possibleInterface.Name()] = index
			}
			if index < len(suggestedInterfaces) {
				s := suggestedInterfaces[index]
				if s.name == possibleInterface.Name() {
					s.count = s.count + 1
				}
			}
		}
	}

	// sort results (by count usage for interfaces, alphabetical order for objects)
	sort.Sort(suggestedInterfaceSortedSlice(suggestedInterfaces))
	sort.Sort(sort.StringSlice(suggestedObjectTypes))

	// return concatenated slices of both interface and object type names
	// and removing duplicates
	// ordered by: interface (sorted) and object (sorted)
	results := []string{}
	for _, s := range suggestedInterfaces {
		if _, ok := suggestedObjectMap[s.name]; !ok {
			results = append(results, s.name)

		}
	}
	results = append(results, suggestedObjectTypes...)
	return results
}

// getSuggestedFieldNames For the field name provided, determine if there are any similar field names
// that may be the result of a typo.
func getSuggestedFieldNames(schema *Schema, ttype Output, fieldName string) []string {

	fields := FieldDefinitionMap{}
	switch ttype := ttype.(type) {
	case *Object:
		fields = ttype.Fields()
	case *Interface:
		fields = ttype.Fields()
	default:
		return []string{}
	}

	possibleFieldNames := []string{}
	for possibleFieldName := range fields {
		possibleFieldNames = append(possibleFieldNames, possibleFieldName)
	}
	return suggestionList(fieldName, possibleFieldNames)
}

// suggestedInterface an internal struct to sort interface by usage count
type suggestedInterface struct {
	name  string
	count int
}
type suggestedInterfaceSortedSlice []*suggestedInterface

func (s suggestedInterfaceSortedSlice) Len() int {
	return len(s)
}
func (s suggestedInterfaceSortedSlice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s suggestedInterfaceSortedSlice) Less(i, j int) bool {
	if s[i].count == s[j].count {
		return s[i].name < s[j].name
	}
	return s[i].count > s[j].count
}

// FragmentsOnCompositeTypesRule Fragments on composite type
//
// Fragments use a type condition to determine if they apply, since fragments
// can only be spread into a composite type (object, interface, or union), the
// type condition must also be a composite type.
func FragmentsOnCompositeTypesRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.InlineFragment: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.InlineFragment); ok {
						ttype := context.Type()
						if node.TypeCondition != nil && ttype != nil && !IsCompositeType(ttype) {
							reportError(
								context,
								fmt.Sprintf(`Fragment cannot condition on non composite type "%v".`, ttype),
								[]ast.Node{node.TypeCondition},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.FragmentDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.FragmentDefinition); ok {
						ttype := context.Type()
						if ttype != nil && !IsCompositeType(ttype) {
							nodeName := ""
							if node.Name != nil {
								nodeName = node.Name.Value
							}
							reportError(
								context,
								fmt.Sprintf(`Fragment "%v" cannot condition on non composite type "%v".`, nodeName, printer.Print(node.TypeCondition)),
								[]ast.Node{node.TypeCondition},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func unknownArgMessage(argName string, fieldName string, parentTypeName string, suggestedArgs []string) string {
	message := fmt.Sprintf(`Unknown argument "%v" on field "%v" of type "%v".`, argName, fieldName, parentTypeName)

	if len(suggestedArgs) > 0 {
		message = fmt.Sprintf(`%v Did you mean %v?`, message, quotedOrList(suggestedArgs))
	}

	return message
}

func unknownDirectiveArgMessage(argName string, directiveName string, suggestedArgs []string) string {
	message := fmt.Sprintf(`Unknown argument "%v" on directive "@%v".`, argName, directiveName)

	if len(suggestedArgs) > 0 {
		message = fmt.Sprintf(`%v Did you mean %v?`, message, quotedOrList(suggestedArgs))
	}

	return message
}

// KnownArgumentNamesRule Known argument names
//
// A GraphQL field is only valid if all supplied arguments are defined by
// that field.
func KnownArgumentNamesRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Argument: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					var action = visitor.ActionNoChange
					var result interface{}
					if node, ok := p.Node.(*ast.Argument); ok {
						var argumentOf ast.Node
						if len(p.Ancestors) > 0 {
							argumentOf = p.Ancestors[len(p.Ancestors)-1]
						}
						if argumentOf == nil {
							return action, result
						}
						var fieldArgDef *Argument
						if argumentOf.GetKind() == kinds.Field {
							fieldDef := context.FieldDef()
							if fieldDef == nil {
								return action, result
							}
							nodeName := ""
							if node.Name != nil {
								nodeName = node.Name.Value
							}
							argNames := []string{}
							for _, arg := range fieldDef.Args {
								argNames = append(argNames, arg.Name())
								if arg.Name() == nodeName {
									fieldArgDef = arg
								}
							}
							if fieldArgDef == nil {
								parentType := context.ParentType()
								parentTypeName := ""
								if parentType != nil {
									parentTypeName = parentType.Name()
								}
								reportError(
									context,
									unknownArgMessage(nodeName, fieldDef.Name, parentTypeName, suggestionList(nodeName, argNames)),
									[]ast.Node{node},
								)
							}
						} else if argumentOf.GetKind() == kinds.Directive {
							directive := context.Directive()
							if directive == nil {
								return action, result
							}
							nodeName := ""
							if node.Name != nil {
								nodeName = node.Name.Value
							}
							argNames := []string{}
							var directiveArgDef *Argument
							for _, arg := range directive.Args {
								argNames = append(argNames, arg.Name())
								if arg.Name() == nodeName {
									directiveArgDef = arg
								}
							}
							if directiveArgDef == nil {
								reportError(
									context,
									unknownDirectiveArgMessage(nodeName, directive.Name, suggestionList(nodeName, argNames)),
									[]ast.Node{node},
								)
							}
						}

					}
					return action, result
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func MisplaceDirectiveMessage(directiveName string, location string) string {
	return fmt.Sprintf(`Directive "%v" may not be used on %v.`, directiveName, location)
}

// KnownDirectivesRule Known directives
//
// A GraphQL document is only valid if all `@directives` are known by the
// schema and legally positioned.
func KnownDirectivesRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Directive: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					var action = visitor.ActionNoChange
					var result interface{}
					if node, ok := p.Node.(*ast.Directive); ok {

						nodeName := ""
						if node.Name != nil {
							nodeName = node.Name.Value
						}

						var directiveDef *Directive
						for _, def := range context.Schema().Directives() {
							if def.Name == nodeName {
								directiveDef = def
							}
						}
						if directiveDef == nil {
							return reportError(
								context,
								fmt.Sprintf(`Unknown directive "%v".`, nodeName),
								[]ast.Node{node},
							)
						}

						candidateLocation := getDirectiveLocationForASTPath(p.Ancestors)

						directiveHasLocation := false
						for _, loc := range directiveDef.Locations {
							if loc == candidateLocation {
								directiveHasLocation = true
								break
							}
						}

						if candidateLocation == "" {
							reportError(
								context,
								MisplaceDirectiveMessage(nodeName, node.GetKind()),
								[]ast.Node{node},
							)
						} else if !directiveHasLocation {
							reportError(
								context,
								MisplaceDirectiveMessage(nodeName, candidateLocation),
								[]ast.Node{node},
							)
						}

					}
					return action, result
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func getDirectiveLocationForASTPath(ancestors []ast.Node) string {
	var appliedTo ast.Node
	if len(ancestors) > 0 {
		appliedTo = ancestors[len(ancestors)-1]
	}
	if appliedTo == nil {
		return ""
	}
	kind := appliedTo.GetKind()
	if kind == kinds.OperationDefinition {
		appliedTo, _ := appliedTo.(*ast.OperationDefinition)
		if appliedTo.Operation == ast.OperationTypeQuery {
			return DirectiveLocationQuery
		}
		if appliedTo.Operation == ast.OperationTypeMutation {
			return DirectiveLocationMutation
		}
		if appliedTo.Operation == ast.OperationTypeSubscription {
			return DirectiveLocationSubscription
		}
	}
	if kind == kinds.Field {
		return DirectiveLocationField
	}
	if kind == kinds.FragmentSpread {
		return DirectiveLocationFragmentSpread
	}
	if kind == kinds.InlineFragment {
		return DirectiveLocationInlineFragment
	}
	if kind == kinds.FragmentDefinition {
		return DirectiveLocationFragmentDefinition
	}
	if kind == kinds.SchemaDefinition {
		return DirectiveLocationSchema
	}
	if kind == kinds.ScalarDefinition {
		return DirectiveLocationScalar
	}
	if kind == kinds.ObjectDefinition {
		return DirectiveLocationObject
	}
	if kind == kinds.FieldDefinition {
		return DirectiveLocationFieldDefinition
	}
	if kind == kinds.InterfaceDefinition {
		return DirectiveLocationInterface
	}
	if kind == kinds.UnionDefinition {
		return DirectiveLocationUnion
	}
	if kind == kinds.EnumDefinition {
		return DirectiveLocationEnum
	}
	if kind == kinds.EnumValueDefinition {
		return DirectiveLocationEnumValue
	}
	if kind == kinds.InputObjectDefinition {
		return DirectiveLocationInputObject
	}
	if kind == kinds.InputValueDefinition {
		var parentNode ast.Node
		if len(ancestors) >= 3 {
			parentNode = ancestors[len(ancestors)-3]
		}
		if parentNode.GetKind() == kinds.InputObjectDefinition {
			return DirectiveLocationInputFieldDefinition
		} else {
			return DirectiveLocationArgumentDefinition
		}
	}
	return ""
}

// KnownFragmentNamesRule Known fragment names
//
// A GraphQL document is only valid if all `...Fragment` fragment spreads refer
// to fragments defined in the same document.
func KnownFragmentNamesRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.FragmentSpread: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					var action = visitor.ActionNoChange
					var result interface{}
					if node, ok := p.Node.(*ast.FragmentSpread); ok {

						fragmentName := ""
						if node.Name != nil {
							fragmentName = node.Name.Value
						}

						fragment := context.Fragment(fragmentName)
						if fragment == nil {
							reportError(
								context,
								fmt.Sprintf(`Unknown fragment "%v".`, fragmentName),
								[]ast.Node{node.Name},
							)
						}
					}
					return action, result
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func unknownTypeMessage(typeName string, suggestedTypes []string) string {
	message := fmt.Sprintf(`Unknown type "%v".`, typeName)
	if len(suggestedTypes) > 0 {
		message = fmt.Sprintf(`%v Did you mean %v?`, message, quotedOrList(suggestedTypes))
	}

	return message
}

// KnownTypeNamesRule Known type names
//
// A GraphQL document is only valid if referenced types (specifically
// variable definitions and fragment conditions) are defined by the type schema.
func KnownTypeNamesRule(context *ValidationContext) *ValidationRuleInstance {
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.ObjectDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.InterfaceDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.UnionDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.InputObjectDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.Named: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.Named); ok {
						typeNameValue := ""
						typeName := node.Name
						if typeName != nil {
							typeNameValue = typeName.Value
						}
						ttype := context.Schema().Type(typeNameValue)
						if ttype == nil {
							suggestedTypes := []string{}
							for key := range context.Schema().TypeMap() {
								suggestedTypes = append(suggestedTypes, key)
							}
							reportError(
								context,
								unknownTypeMessage(typeNameValue, suggestionList(typeNameValue, suggestedTypes)),
								[]ast.Node{node},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// LoneAnonymousOperationRule Lone anonymous operation
//
// A GraphQL document is only valid if when it contains an anonymous operation
// (the query short-hand) that it contains only that one operation definition.
func LoneAnonymousOperationRule(context *ValidationContext) *ValidationRuleInstance {
	var operationCount = 0
	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Document: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.Document); ok {
						operationCount = 0
						for _, definition := range node.Definitions {
							if definition.GetKind() == kinds.OperationDefinition {
								operationCount = operationCount + 1
							}
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.OperationDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.OperationDefinition); ok {
						if node.Name == nil && operationCount > 1 {
							reportError(
								context,
								`This anonymous operation must be the only defined operation.`,
								[]ast.Node{node},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

type nodeSet struct {
	set map[ast.Node]bool
}

func newNodeSet() *nodeSet {
	return &nodeSet{
		set: map[ast.Node]bool{},
	}
}
func (set *nodeSet) Has(node ast.Node) bool {
	_, ok := set.set[node]
	return ok
}
func (set *nodeSet) Add(node ast.Node) bool {
	if set.Has(node) {
		return false
	}
	set.set[node] = true
	return true
}

func CycleErrorMessage(fragName string, spreadNames []string) string {
	via := ""
	if len(spreadNames) > 0 {
		via = " via " + strings.Join(spreadNames, ", ")
	}
	return fmt.Sprintf(`Cannot spread fragment "%v" within itself%v.`, fragName, via)
}

// NoFragmentCyclesRule No fragment cycles
func NoFragmentCyclesRule(context *ValidationContext) *ValidationRuleInstance {

	// Tracks already visited fragments to maintain O(N) and to ensure that cycles
	// are not redundantly reported.
	visitedFrags := map[string]bool{}

	// Array of AST nodes used to produce meaningful errors
	spreadPath := []*ast.FragmentSpread{}

	// Position in the spread path
	spreadPathIndexByName := map[string]int{}

	// This does a straight-forward DFS to find cycles.
	// It does not terminate when a cycle was found but continues to explore
	// the graph to find all possible cycles.
	var detectCycleRecursive func(fragment *ast.FragmentDefinition)
	detectCycleRecursive = func(fragment *ast.FragmentDefinition) {

		fragmentName := ""
		if fragment.Name != nil {
			fragmentName = fragment.Name.Value
		}
		visitedFrags[fragmentName] = true

		spreadNodes := context.FragmentSpreads(fragment)
		if len(spreadNodes) == 0 {
			return
		}

		spreadPathIndexByName[fragmentName] = len(spreadPath)

		for _, spreadNode := range spreadNodes {

			spreadName := ""
			if spreadNode.Name != nil {
				spreadName = spreadNode.Name.Value
			}
			cycleIndex, ok := spreadPathIndexByName[spreadName]
			if !ok {
				spreadPath = append(spreadPath, spreadNode)
				if visited, ok := visitedFrags[spreadName]; !ok || !visited {
					spreadFragment := context.Fragment(spreadName)
					if spreadFragment != nil {
						detectCycleRecursive(spreadFragment)
					}
				}
				spreadPath = spreadPath[:len(spreadPath)-1]
			} else {
				cyclePath := spreadPath[cycleIndex:]

				spreadNames := []string{}
				for _, s := range cyclePath {
					name := ""
					if s.Name != nil {
						name = s.Name.Value
					}
					spreadNames = append(spreadNames, name)
				}

				nodes := []ast.Node{}
				for _, c := range cyclePath {
					nodes = append(nodes, c)
				}
				nodes = append(nodes, spreadNode)

				reportError(
					context,
					CycleErrorMessage(spreadName, spreadNames),
					nodes,
				)
			}

		}
		delete(spreadPathIndexByName, fragmentName)

	}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.FragmentDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.FragmentDefinition); ok && node != nil {
						nodeName := ""
						if node.Name != nil {
							nodeName = node.Name.Value
						}
						if _, ok := visitedFrags[nodeName]; !ok {
							detectCycleRecursive(node)
						}
					}
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func UndefinedVarMessage(varName string, opName string) string {
	if opName != "" {
		return fmt.Sprintf(`Variable "$%v" is not defined by operation "%v".`, varName, opName)
	}
	return fmt.Sprintf(`Variable "$%v" is not defined.`, varName)
}

// NoUndefinedVariablesRule No undefined variables
//
// A GraphQL operation is only valid if all variables encountered, both directly
// and via fragment spreads, are defined by that operation.
func NoUndefinedVariablesRule(context *ValidationContext) *ValidationRuleInstance {
	var variableNameDefined = map[string]bool{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Enter: func(p visitor.VisitFuncParams) (string, interface{}) {
					variableNameDefined = map[string]bool{}
					return visitor.ActionNoChange, nil
				},
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					if operation, ok := p.Node.(*ast.OperationDefinition); ok && operation != nil {
						usages := context.RecursiveVariableUsages(operation)

						for _, usage := range usages {
							if usage == nil {
								continue
							}
							if usage.Node == nil {
								continue
							}
							varName := ""
							if usage.Node.Name != nil {
								varName = usage.Node.Name.Value
							}
							opName := ""
							if operation.Name != nil {
								opName = operation.Name.Value
							}
							if res, ok := variableNameDefined[varName]; !ok || !res {
								reportError(
									context,
									UndefinedVarMessage(varName, opName),
									[]ast.Node{usage.Node, operation},
								)
							}
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.VariableDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.VariableDefinition); ok && node != nil {
						variableName := ""
						if node.Variable != nil && node.Variable.Name != nil {
							variableName = node.Variable.Name.Value
						}
						variableNameDefined[variableName] = true
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// NoUnusedFragmentsRule No unused fragments
//
// A GraphQL document is only valid if all fragment definitions are spread
// within operations, or spread within other fragments spread within operations.
func NoUnusedFragmentsRule(context *ValidationContext) *ValidationRuleInstance {

	var fragmentDefs = []*ast.FragmentDefinition{}
	var operationDefs = []*ast.OperationDefinition{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.OperationDefinition); ok && node != nil {
						operationDefs = append(operationDefs, node)
					}
					return visitor.ActionSkip, nil
				},
			},
			kinds.FragmentDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.FragmentDefinition); ok && node != nil {
						fragmentDefs = append(fragmentDefs, node)
					}
					return visitor.ActionSkip, nil
				},
			},
			kinds.Document: {
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					fragmentNameUsed := map[string]bool{}
					for _, operation := range operationDefs {
						fragments := context.RecursivelyReferencedFragments(operation)
						for _, fragment := range fragments {
							fragName := ""
							if fragment.Name != nil {
								fragName = fragment.Name.Value
							}
							fragmentNameUsed[fragName] = true
						}
					}

					for _, def := range fragmentDefs {
						defName := ""
						if def.Name != nil {
							defName = def.Name.Value
						}

						isFragNameUsed, ok := fragmentNameUsed[defName]
						if !ok || isFragNameUsed != true {
							reportError(
								context,
								fmt.Sprintf(`Fragment "%v" is never used.`, defName),
								[]ast.Node{def},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func UnusedVariableMessage(varName string, opName string) string {
	if opName != "" {
		return fmt.Sprintf(`Variable "$%v" is never used in operation "%v".`, varName, opName)
	}
	return fmt.Sprintf(`Variable "$%v" is never used.`, varName)
}

// NoUnusedVariablesRule No unused variables
//
// A GraphQL operation is only valid if all variables defined by an operation
// are used, either directly or within a spread fragment.
func NoUnusedVariablesRule(context *ValidationContext) *ValidationRuleInstance {

	var variableDefs = []*ast.VariableDefinition{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Enter: func(p visitor.VisitFuncParams) (string, interface{}) {
					variableDefs = []*ast.VariableDefinition{}
					return visitor.ActionNoChange, nil
				},
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					if operation, ok := p.Node.(*ast.OperationDefinition); ok && operation != nil {
						variableNameUsed := map[string]bool{}
						usages := context.RecursiveVariableUsages(operation)

						for _, usage := range usages {
							varName := ""
							if usage != nil && usage.Node != nil && usage.Node.Name != nil {
								varName = usage.Node.Name.Value
							}
							if varName != "" {
								variableNameUsed[varName] = true
							}
						}
						for _, variableDef := range variableDefs {
							variableName := ""
							if variableDef != nil && variableDef.Variable != nil && variableDef.Variable.Name != nil {
								variableName = variableDef.Variable.Name.Value
							}
							opName := ""
							if operation.Name != nil {
								opName = operation.Name.Value
							}
							if res, ok := variableNameUsed[variableName]; !ok || !res {
								reportError(
									context,
									UnusedVariableMessage(variableName, opName),
									[]ast.Node{variableDef},
								)
							}
						}

					}

					return visitor.ActionNoChange, nil
				},
			},
			kinds.VariableDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if def, ok := p.Node.(*ast.VariableDefinition); ok && def != nil {
						variableDefs = append(variableDefs, def)
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

type fieldDefPair struct {
	ParentType Composite
	Field      *ast.Field
	FieldDef   *FieldDefinition
}

func collectFieldASTsAndDefs(context *ValidationContext, parentType Named, selectionSet *ast.SelectionSet, visitedFragmentNames map[string]bool, astAndDefs map[string][]*fieldDefPair) map[string][]*fieldDefPair {

	if astAndDefs == nil {
		astAndDefs = map[string][]*fieldDefPair{}
	}
	if visitedFragmentNames == nil {
		visitedFragmentNames = map[string]bool{}
	}
	if selectionSet == nil {
		return astAndDefs
	}
	for _, selection := range selectionSet.Selections {
		switch selection := selection.(type) {
		case *ast.Field:
			fieldName := ""
			if selection.Name != nil {
				fieldName = selection.Name.Value
			}
			var fieldDef *FieldDefinition
			if parentType, ok := parentType.(*Object); ok {
				fieldDef, _ = parentType.Fields()[fieldName]
			}
			if parentType, ok := parentType.(*Interface); ok {
				fieldDef, _ = parentType.Fields()[fieldName]
			}

			responseName := fieldName
			if selection.Alias != nil {
				responseName = selection.Alias.Value
			}
			_, ok := astAndDefs[responseName]
			if !ok {
				astAndDefs[responseName] = []*fieldDefPair{}
			}
			if parentType, ok := parentType.(Composite); ok {
				astAndDefs[responseName] = append(astAndDefs[responseName], &fieldDefPair{
					ParentType: parentType,
					Field:      selection,
					FieldDef:   fieldDef,
				})
			} else {
				astAndDefs[responseName] = append(astAndDefs[responseName], &fieldDefPair{
					Field:    selection,
					FieldDef: fieldDef,
				})
			}
		case *ast.InlineFragment:
			inlineFragmentType := parentType
			if selection.TypeCondition != nil {
				parentType, _ := typeFromAST(*context.Schema(), selection.TypeCondition)
				inlineFragmentType = parentType
			}
			astAndDefs = collectFieldASTsAndDefs(
				context,
				inlineFragmentType,
				selection.SelectionSet,
				visitedFragmentNames,
				astAndDefs,
			)
		case *ast.FragmentSpread:
			fragName := ""
			if selection.Name != nil {
				fragName = selection.Name.Value
			}
			if _, ok := visitedFragmentNames[fragName]; ok {
				continue
			}
			visitedFragmentNames[fragName] = true
			fragment := context.Fragment(fragName)
			if fragment == nil {
				continue
			}
			parentType, _ := typeFromAST(*context.Schema(), fragment.TypeCondition)
			astAndDefs = collectFieldASTsAndDefs(
				context,
				parentType,
				fragment.SelectionSet,
				visitedFragmentNames,
				astAndDefs,
			)
		}
	}
	return astAndDefs
}

// pairSet A way to keep track of pairs of things when the ordering of the pair does
// not matter. We do this by maintaining a sort of double adjacency sets.
type pairSet struct {
	data map[ast.Node]*nodeSet
}

func newPairSet() *pairSet {
	return &pairSet{
		data: map[ast.Node]*nodeSet{},
	}
}
func (pair *pairSet) Has(a ast.Node, b ast.Node) bool {
	first, ok := pair.data[a]
	if !ok || first == nil {
		return false
	}
	res := first.Has(b)
	return res
}
func (pair *pairSet) Add(a ast.Node, b ast.Node) bool {
	pair.data = pairSetAdd(pair.data, a, b)
	pair.data = pairSetAdd(pair.data, b, a)
	return true
}

func pairSetAdd(data map[ast.Node]*nodeSet, a, b ast.Node) map[ast.Node]*nodeSet {
	set, ok := data[a]
	if !ok || set == nil {
		set = newNodeSet()
		data[a] = set
	}
	set.Add(b)
	return data
}

type conflictReason struct {
	Name    string
	Message interface{} // conflictReason || []conflictReason
}
type conflict struct {
	Reason      conflictReason
	FieldsLeft  []ast.Node
	FieldsRight []ast.Node
}

func sameArguments(args1 []*ast.Argument, args2 []*ast.Argument) bool {
	if len(args1) != len(args2) {
		return false
	}

	for _, arg1 := range args1 {
		arg1Name := ""
		if arg1.Name != nil {
			arg1Name = arg1.Name.Value
		}

		var foundArgs2 *ast.Argument
		for _, arg2 := range args2 {
			arg2Name := ""
			if arg2.Name != nil {
				arg2Name = arg2.Name.Value
			}
			if arg1Name == arg2Name {
				foundArgs2 = arg2
			}
			break
		}
		if foundArgs2 == nil {
			return false
		}
		if sameValue(arg1.Value, foundArgs2.Value) == false {
			return false
		}
	}

	return true
}
func sameValue(value1 ast.Value, value2 ast.Value) bool {
	if value1 == nil && value2 == nil {
		return true
	}
	val1 := printer.Print(value1)
	val2 := printer.Print(value2)

	return val1 == val2
}

func sameType(typeA, typeB Type) bool {
	if typeA == typeB {
		return true
	}

	if typeA, ok := typeA.(*List); ok {
		if typeB, ok := typeB.(*List); ok {
			return sameType(typeA.OfType, typeB.OfType)
		}
	}
	if typeA, ok := typeA.(*NonNull); ok {
		if typeB, ok := typeB.(*NonNull); ok {
			return sameType(typeA.OfType, typeB.OfType)
		}
	}

	return false
}

// Two types conflict if both types could not apply to a value simultaneously.
// Composite types are ignored as their individual field types will be compared
// later recursively. However List and Non-Null types must match.
func doTypesConflict(type1 Output, type2 Output) bool {
	if type1, ok := type1.(*List); ok {
		if type2, ok := type2.(*List); ok {
			return doTypesConflict(type1.OfType, type2.OfType)
		}
		return true
	}
	if type2, ok := type2.(*List); ok {
		if type1, ok := type1.(*List); ok {
			return doTypesConflict(type1.OfType, type2.OfType)
		}
		return true
	}
	if type1, ok := type1.(*NonNull); ok {
		if type2, ok := type2.(*NonNull); ok {
			return doTypesConflict(type1.OfType, type2.OfType)
		}
		return true
	}
	if type2, ok := type2.(*NonNull); ok {
		if type1, ok := type1.(*NonNull); ok {
			return doTypesConflict(type1.OfType, type2.OfType)
		}
		return true
	}
	if IsLeafType(type1) || IsLeafType(type2) {
		return type1 != type2
	}
	return false
}

// getSubfieldMap Given two overlapping fields, produce the combined collection of subfields.
func getSubfieldMap(context *ValidationContext, ast1 *ast.Field, type1 Output, ast2 *ast.Field, type2 Output) map[string][]*fieldDefPair {
	selectionSet1 := ast1.SelectionSet
	selectionSet2 := ast2.SelectionSet
	if selectionSet1 != nil && selectionSet2 != nil {
		visitedFragmentNames := map[string]bool{}
		subfieldMap := collectFieldASTsAndDefs(
			context,
			GetNamed(type1),
			selectionSet1,
			visitedFragmentNames,
			nil,
		)
		subfieldMap = collectFieldASTsAndDefs(
			context,
			GetNamed(type2),
			selectionSet2,
			visitedFragmentNames,
			subfieldMap,
		)
		return subfieldMap
	}
	return nil
}

// subfieldConflicts Given a series of Conflicts which occurred between two sub-fields, generate a single Conflict.
func subfieldConflicts(conflicts []*conflict, responseName string, ast1 *ast.Field, ast2 *ast.Field) *conflict {
	if len(conflicts) > 0 {
		conflictReasons := []conflictReason{}
		conflictFieldsLeft := []ast.Node{ast1}
		conflictFieldsRight := []ast.Node{ast2}
		for _, c := range conflicts {
			conflictReasons = append(conflictReasons, c.Reason)
			conflictFieldsLeft = append(conflictFieldsLeft, c.FieldsLeft...)
			conflictFieldsRight = append(conflictFieldsRight, c.FieldsRight...)
		}

		return &conflict{
			Reason: conflictReason{
				Name:    responseName,
				Message: conflictReasons,
			},
			FieldsLeft:  conflictFieldsLeft,
			FieldsRight: conflictFieldsRight,
		}
	}
	return nil
}

// findConflicts Find all Conflicts within a collection of fields.
func findConflicts(context *ValidationContext, parentFieldsAreMutuallyExclusive bool, fieldMap map[string][]*fieldDefPair, comparedSet *pairSet) (conflicts []*conflict) {

	// ensure field traversal
	orderedName := sort.StringSlice{}
	for responseName := range fieldMap {
		orderedName = append(orderedName, responseName)
	}
	orderedName.Sort()

	for _, responseName := range orderedName {
		fields, _ := fieldMap[responseName]
		for _, fieldA := range fields {
			for _, fieldB := range fields {
				c := findConflict(context, parentFieldsAreMutuallyExclusive, responseName, fieldA, fieldB, comparedSet)
				if c != nil {
					conflicts = append(conflicts, c)
				}
			}
		}
	}
	return conflicts
}

// findConflict Determines if there is a conflict between two particular fields.
func findConflict(context *ValidationContext, parentFieldsAreMutuallyExclusive bool, responseName string, field *fieldDefPair, field2 *fieldDefPair, comparedSet *pairSet) *conflict {

	parentType1 := field.ParentType
	ast1 := field.Field
	def1 := field.FieldDef

	parentType2 := field2.ParentType
	ast2 := field2.Field
	def2 := field2.FieldDef

	// Not a pair.
	if ast1 == ast2 {
		return nil
	}

	// Memoize, do not report the same issue twice.
	// Note: Two overlapping ASTs could be encountered both when
	// `parentFieldsAreMutuallyExclusive` is true and is false, which could
	// produce different results (when `true` being a subset of `false`).
	// However we do not need to include this piece of information when
	// memoizing since this rule visits leaf fields before their parent fields,
	// ensuring that `parentFieldsAreMutuallyExclusive` is `false` the first
	// time two overlapping fields are encountered, ensuring that the full
	// set of validation rules are always checked when necessary.
	if comparedSet.Has(ast1, ast2) {
		return nil
	}
	comparedSet.Add(ast1, ast2)

	// The return type for each field.
	var type1 Type
	var type2 Type
	if def1 != nil {
		type1 = def1.Type
	}
	if def2 != nil {
		type2 = def2.Type
	}

	// If it is known that two fields could not possibly apply at the same
	// time, due to the parent types, then it is safe to permit them to diverge
	// in aliased field or arguments used as they will not present any ambiguity
	// by differing.
	// It is known that two parent types could never overlap if they are
	// different Object types. Interface or Union types might overlap - if not
	// in the current state of the schema, then perhaps in some future version,
	// thus may not safely diverge.
	_, isParentType1Object := parentType1.(*Object)
	_, isParentType2Object := parentType2.(*Object)
	fieldsAreMutuallyExclusive := parentFieldsAreMutuallyExclusive || parentType1 != parentType2 && isParentType1Object && isParentType2Object

	if !fieldsAreMutuallyExclusive {
		// Two aliases must refer to the same field.
		name1 := ""
		name2 := ""

		if ast1.Name != nil {
			name1 = ast1.Name.Value
		}
		if ast2.Name != nil {
			name2 = ast2.Name.Value
		}
		if name1 != name2 {
			return &conflict{
				Reason: conflictReason{
					Name:    responseName,
					Message: fmt.Sprintf(`%v and %v are different fields`, name1, name2),
				},
				FieldsLeft:  []ast.Node{ast1},
				FieldsRight: []ast.Node{ast2},
			}
		}

		// Two field calls must have the same arguments.
		if !sameArguments(ast1.Arguments, ast2.Arguments) {
			return &conflict{
				Reason: conflictReason{
					Name:    responseName,
					Message: `they have differing arguments`,
				},
				FieldsLeft:  []ast.Node{ast1},
				FieldsRight: []ast.Node{ast2},
			}
		}
	}

	if type1 != nil && type2 != nil && doTypesConflict(type1, type2) {
		return &conflict{
			Reason: conflictReason{
				Name:    responseName,
				Message: fmt.Sprintf(`they return conflicting types %v and %v`, type1, type2),
			},
			FieldsLeft:  []ast.Node{ast1},
			FieldsRight: []ast.Node{ast2},
		}
	}

	subFieldMap := getSubfieldMap(context, ast1, type1, ast2, type2)
	if subFieldMap != nil {
		conflicts := findConflicts(context, fieldsAreMutuallyExclusive, subFieldMap, comparedSet)
		return subfieldConflicts(conflicts, responseName, ast1, ast2)
	}

	return nil
}

// OverlappingFieldsCanBeMergedRule Overlapping fields can be merged
//
// A selection set is only valid if all fields (including spreading any
// fragments) either correspond to distinct response names or can be merged
// without ambiguity.
func OverlappingFieldsCanBeMergedRule(context *ValidationContext) *ValidationRuleInstance {
	comparedSet := newPairSet()

	var reasonMessage func(message interface{}) string
	reasonMessage = func(message interface{}) string {
		switch reason := message.(type) {
		case string:
			return reason
		case conflictReason:
			return reasonMessage(reason.Message)
		case []conflictReason:
			messages := []string{}
			for _, r := range reason {
				messages = append(messages, fmt.Sprintf(
					`subfields "%v" conflict because %v`,
					r.Name,
					reasonMessage(r.Message),
				))
			}
			return strings.Join(messages, " and ")
		}
		return ""
	}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.SelectionSet: {
				// Note: we validate on the reverse traversal so deeper conflicts will be
				// caught first, for correct calculation of mutual exclusivity and for
				// clearer error messages.
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					if selectionSet, ok := p.Node.(*ast.SelectionSet); ok && selectionSet != nil {
						parentType, _ := context.ParentType().(Named)
						fieldMap := collectFieldASTsAndDefs(
							context,
							parentType,
							selectionSet,
							nil,
							nil,
						)
						conflicts := findConflicts(context, false, fieldMap, comparedSet)
						if len(conflicts) > 0 {
							for _, c := range conflicts {
								responseName := c.Reason.Name
								reason := c.Reason
								reportError(
									context,
									fmt.Sprintf(`Fields "%v" conflict because %v. `+
										`Use different aliases on the fields to fetch both if this was intentional.`,
										responseName,
										reasonMessage(reason),
									),
									append(c.FieldsLeft, c.FieldsRight...),
								)
							}
							return visitor.ActionNoChange, nil
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

func getFragmentType(context *ValidationContext, name string) Type {
	frag := context.Fragment(name)
	if frag == nil {
		return nil
	}
	ttype, _ := typeFromAST(*context.Schema(), frag.TypeCondition)
	return ttype
}

func doTypesOverlap(schema *Schema, t1 Type, t2 Type) bool {
	if t1 == t2 {
		return true
	}
	if _, ok := t1.(*Object); ok {
		if _, ok := t2.(*Object); ok {
			return false
		}
		if t2, ok := t2.(Abstract); ok {
			for _, ttype := range schema.PossibleTypes(t2) {
				if ttype == t1 {
					return true
				}
			}
			return false
		}
	}
	if t1, ok := t1.(Abstract); ok {
		if _, ok := t2.(*Object); ok {
			for _, ttype := range schema.PossibleTypes(t1) {
				if ttype == t2 {
					return true
				}
			}
			return false
		}
		t1TypeNames := map[string]bool{}
		for _, ttype := range schema.PossibleTypes(t1) {
			t1TypeNames[ttype.Name()] = true
		}
		if t2, ok := t2.(Abstract); ok {
			for _, ttype := range schema.PossibleTypes(t2) {
				if hasT1TypeName, _ := t1TypeNames[ttype.Name()]; hasT1TypeName {
					return true
				}
			}
			return false
		}
	}
	return false
}

// PossibleFragmentSpreadsRule Possible fragment spread
//
// A fragment spread is only valid if the type condition could ever possibly
// be true: if there is a non-empty intersection of the possible parent types,
// and possible types which pass the type condition.
func PossibleFragmentSpreadsRule(context *ValidationContext) *ValidationRuleInstance {

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.InlineFragment: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.InlineFragment); ok && node != nil {
						fragType := context.Type()
						parentType, _ := context.ParentType().(Type)

						if fragType != nil && parentType != nil && !doTypesOverlap(context.Schema(), fragType, parentType) {
							reportError(
								context,
								fmt.Sprintf(`Fragment cannot be spread here as objects of `+
									`type "%v" can never be of type "%v".`, parentType, fragType),
								[]ast.Node{node},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.FragmentSpread: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.FragmentSpread); ok && node != nil {
						fragName := ""
						if node.Name != nil {
							fragName = node.Name.Value
						}
						fragType := getFragmentType(context, fragName)
						parentType, _ := context.ParentType().(Type)
						if fragType != nil && parentType != nil && !doTypesOverlap(context.Schema(), fragType, parentType) {
							reportError(
								context,
								fmt.Sprintf(`Fragment "%v" cannot be spread here as objects of `+
									`type "%v" can never be of type "%v".`, fragName, parentType, fragType),
								[]ast.Node{node},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// ProvidedNonNullArgumentsRule Provided required arguments
//
// A field or directive is only valid if all required (non-null) field arguments
// have been provided.
func ProvidedNonNullArgumentsRule(context *ValidationContext) *ValidationRuleInstance {

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Field: {
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					// Validate on leave to allow for deeper errors to appear first.
					if fieldAST, ok := p.Node.(*ast.Field); ok && fieldAST != nil {
						fieldDef := context.FieldDef()
						if fieldDef == nil {
							return visitor.ActionSkip, nil
						}

						argASTs := fieldAST.Arguments

						argASTMap := map[string]*ast.Argument{}
						for _, arg := range argASTs {
							name := ""
							if arg.Name != nil {
								name = arg.Name.Value
							}
							argASTMap[name] = arg
						}
						for _, argDef := range fieldDef.Args {
							argAST, _ := argASTMap[argDef.Name()]
							if argAST == nil {
								if argDefType, ok := argDef.Type.(*NonNull); ok {
									fieldName := ""
									if fieldAST.Name != nil {
										fieldName = fieldAST.Name.Value
									}
									reportError(
										context,
										fmt.Sprintf(`Field "%v" argument "%v" of type "%v" `+
											`is required but not provided.`, fieldName, argDef.Name(), argDefType),
										[]ast.Node{fieldAST},
									)
								}
							}
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.Directive: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					// Validate on leave to allow for deeper errors to appear first.

					if directiveAST, ok := p.Node.(*ast.Directive); ok && directiveAST != nil {
						directiveDef := context.Directive()
						if directiveDef == nil {
							return visitor.ActionSkip, nil
						}
						argASTs := directiveAST.Arguments

						argASTMap := map[string]*ast.Argument{}
						for _, arg := range argASTs {
							name := ""
							if arg.Name != nil {
								name = arg.Name.Value
							}
							argASTMap[name] = arg
						}

						for _, argDef := range directiveDef.Args {
							argAST, _ := argASTMap[argDef.Name()]
							if argAST == nil {
								if argDefType, ok := argDef.Type.(*NonNull); ok {
									directiveName := ""
									if directiveAST.Name != nil {
										directiveName = directiveAST.Name.Value
									}
									reportError(
										context,
										fmt.Sprintf(`Directive "@%v" argument "%v" of type `+
											`"%v" is required but not provided.`, directiveName, argDef.Name(), argDefType),
										[]ast.Node{directiveAST},
									)
								}
							}
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// ScalarLeafsRule Scalar leafs
//
// A GraphQL document is valid only if all leaf fields (fields without
// sub selections) are of scalar or enum types.
func ScalarLeafsRule(context *ValidationContext) *ValidationRuleInstance {

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Field: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.Field); ok && node != nil {
						nodeName := ""
						if node.Name != nil {
							nodeName = node.Name.Value
						}
						ttype := context.Type()
						if ttype != nil {
							if IsLeafType(ttype) {
								if node.SelectionSet != nil {
									reportError(
										context,
										fmt.Sprintf(`Field "%v" of type "%v" must not have a sub selection.`, nodeName, ttype),
										[]ast.Node{node.SelectionSet},
									)
								}
							} else if node.SelectionSet == nil {
								reportError(
									context,
									fmt.Sprintf(`Field "%v" of type "%v" must have a sub selection.`, nodeName, ttype),
									[]ast.Node{node},
								)
							}
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// UniqueArgumentNamesRule Unique argument names
//
// A GraphQL field or directive is only valid if all supplied arguments are
// uniquely named.
func UniqueArgumentNamesRule(context *ValidationContext) *ValidationRuleInstance {
	knownArgNames := map[string]*ast.Name{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.Field: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					knownArgNames = map[string]*ast.Name{}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.Directive: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					knownArgNames = map[string]*ast.Name{}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.Argument: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.Argument); ok {
						argName := ""
						if node.Name != nil {
							argName = node.Name.Value
						}
						if nameAST, ok := knownArgNames[argName]; ok {
							reportError(
								context,
								fmt.Sprintf(`There can be only one argument named "%v".`, argName),
								[]ast.Node{nameAST, node.Name},
							)
						} else {
							knownArgNames[argName] = node.Name
						}
					}
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// UniqueFragmentNamesRule Unique fragment names
//
// A GraphQL document is only valid if all defined fragments have unique names.
func UniqueFragmentNamesRule(context *ValidationContext) *ValidationRuleInstance {
	knownFragmentNames := map[string]*ast.Name{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
			kinds.FragmentDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.FragmentDefinition); ok && node != nil {
						fragmentName := ""
						if node.Name != nil {
							fragmentName = node.Name.Value
						}
						if nameAST, ok := knownFragmentNames[fragmentName]; ok {
							reportError(
								context,
								fmt.Sprintf(`There can only be one fragment named "%v".`, fragmentName),
								[]ast.Node{nameAST, node.Name},
							)
						} else {
							knownFragmentNames[fragmentName] = node.Name
						}
					}
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// UniqueInputFieldNamesRule Unique input field names
//
// A GraphQL input object value is only valid if all supplied fields are
// uniquely named.
func UniqueInputFieldNamesRule(context *ValidationContext) *ValidationRuleInstance {
	knownNameStack := []map[string]*ast.Name{}
	knownNames := map[string]*ast.Name{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.ObjectValue: visitor.NamedVisitFuncs{
				Enter: func(p visitor.VisitFuncParams) (string, interface{}) {
					knownNameStack = append(knownNameStack, knownNames)
					knownNames = map[string]*ast.Name{}
					return visitor.ActionNoChange, nil
				},
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					// pop
					knownNames, knownNameStack = knownNameStack[len(knownNameStack)-1], knownNameStack[:len(knownNameStack)-1]
					return visitor.ActionNoChange, nil
				},
			},
			kinds.ObjectField: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.ObjectField); ok {
						fieldName := ""
						if node.Name != nil {
							fieldName = node.Name.Value
						}
						if knownNameAST, ok := knownNames[fieldName]; ok {
							reportError(
								context,
								fmt.Sprintf(`There can be only one input field named "%v".`, fieldName),
								[]ast.Node{knownNameAST, node.Name},
							)
						} else {
							knownNames[fieldName] = node.Name
						}

					}
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// UniqueOperationNamesRule Unique operation names
//
// A GraphQL document is only valid if all defined operations have unique names.
func UniqueOperationNamesRule(context *ValidationContext) *ValidationRuleInstance {
	knownOperationNames := map[string]*ast.Name{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.OperationDefinition); ok && node != nil {
						operationName := ""
						if node.Name != nil {
							operationName = node.Name.Value
						}
						if nameAST, ok := knownOperationNames[operationName]; ok {
							reportError(
								context,
								fmt.Sprintf(`There can only be one operation named "%v".`, operationName),
								[]ast.Node{nameAST, node.Name},
							)
						} else {
							knownOperationNames[operationName] = node.Name
						}
					}
					return visitor.ActionSkip, nil
				},
			},
			kinds.FragmentDefinition: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					return visitor.ActionSkip, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// UniqueVariableNamesRule Unique variable names
//
// A GraphQL operation is only valid if all its variables are uniquely named.
func UniqueVariableNamesRule(context *ValidationContext) *ValidationRuleInstance {
	knownVariableNames := map[string]*ast.Name{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.OperationDefinition); ok && node != nil {
						knownVariableNames = map[string]*ast.Name{}
					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.VariableDefinition: visitor.NamedVisitFuncs{
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.VariableDefinition); ok && node != nil {
						variableName := ""
						var variableNameAST *ast.Name
						if node.Variable != nil && node.Variable.Name != nil {
							variableNameAST = node.Variable.Name
							variableName = node.Variable.Name.Value
						}
						if nameAST, ok := knownVariableNames[variableName]; ok {
							reportError(
								context,
								fmt.Sprintf(`There can only be one variable named "%v".`, variableName),
								[]ast.Node{nameAST, variableNameAST},
							)
						} else {
							knownVariableNames[variableName] = variableNameAST
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// VariablesAreInputTypesRule Variables are input types
//
// A GraphQL operation is only valid if all the variables it defines are of
// input types (scalar, enum, or input object).
func VariablesAreInputTypesRule(context *ValidationContext) *ValidationRuleInstance {

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.VariableDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if node, ok := p.Node.(*ast.VariableDefinition); ok && node != nil {
						ttype, _ := typeFromAST(*context.Schema(), node.Type)

						// If the variable type is not an input type, return an error.
						if ttype != nil && !IsInputType(ttype) {
							variableName := ""
							if node.Variable != nil && node.Variable.Name != nil {
								variableName = node.Variable.Name.Value
							}
							reportError(
								context,
								fmt.Sprintf(`Variable "$%v" cannot be non-input type "%v".`,
									variableName, printer.Print(node.Type)),
								[]ast.Node{node.Type},
							)
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// If a variable definition has a default value, it's effectively non-null.
func effectiveType(varType Type, varDef *ast.VariableDefinition) Type {
	if varDef.DefaultValue == nil {
		return varType
	}
	if _, ok := varType.(*NonNull); ok {
		return varType
	}
	return NewNonNull(varType)
}

// VariablesInAllowedPositionRule Variables passed to field arguments conform to type
func VariablesInAllowedPositionRule(context *ValidationContext) *ValidationRuleInstance {

	varDefMap := map[string]*ast.VariableDefinition{}

	visitorOpts := &visitor.VisitorOptions{
		KindFuncMap: map[string]visitor.NamedVisitFuncs{
			kinds.OperationDefinition: {
				Enter: func(p visitor.VisitFuncParams) (string, interface{}) {
					varDefMap = map[string]*ast.VariableDefinition{}
					return visitor.ActionNoChange, nil
				},
				Leave: func(p visitor.VisitFuncParams) (string, interface{}) {
					if operation, ok := p.Node.(*ast.OperationDefinition); ok {

						usages := context.RecursiveVariableUsages(operation)
						for _, usage := range usages {
							varName := ""
							if usage != nil && usage.Node != nil && usage.Node.Name != nil {
								varName = usage.Node.Name.Value
							}
							varDef, _ := varDefMap[varName]
							if varDef != nil && usage.Type != nil {
								varType, err := typeFromAST(*context.Schema(), varDef.Type)
								if err != nil {
									varType = nil
								}
								if varType != nil && !isTypeSubTypeOf(context.Schema(), effectiveType(varType, varDef), usage.Type) {
									reportError(
										context,
										fmt.Sprintf(`Variable "$%v" of type "%v" used in position `+
											`expecting type "%v".`, varName, varType, usage.Type),
										[]ast.Node{varDef, usage.Node},
									)
								}
							}
						}

					}
					return visitor.ActionNoChange, nil
				},
			},
			kinds.VariableDefinition: {
				Kind: func(p visitor.VisitFuncParams) (string, interface{}) {
					if varDefAST, ok := p.Node.(*ast.VariableDefinition); ok {
						defName := ""
						if varDefAST.Variable != nil && varDefAST.Variable.Name != nil {
							defName = varDefAST.Variable.Name.Value
						}
						if defName != "" {
							varDefMap[defName] = varDefAST
						}
					}
					return visitor.ActionNoChange, nil
				},
			},
		},
	}
	return &ValidationRuleInstance{
		VisitorOpts: visitorOpts,
	}
}

// Utility for validators which determines if a value literal AST is valid given
// an input type.
//
// Note that this only validates literal values, variables are assumed to
// provide values of the correct type.
func isValidLiteralValue(ttype Input, valueAST ast.Value) (bool, []string) {
	// A value must be provided if the type is non-null.
	if ttype, ok := ttype.(*NonNull); ok {
		if valueAST == nil {
			if ttype.OfType.Name() != "" {
				return false, []string{fmt.Sprintf(`Expected "%v!", found null.`, ttype.OfType.Name())}
			}
			return false, []string{"Expected non-null value, found null."}
		}
		ofType, _ := ttype.OfType.(Input)
		return isValidLiteralValue(ofType, valueAST)
	}

	if valueAST == nil {
		return true, nil
	}

	// This function only tests literals, and assumes variables will provide
	// values of the correct type.
	if valueAST.GetKind() == kinds.Variable {
		return true, nil
	}

	// Lists accept a non-list value as a list of one.
	if ttype, ok := ttype.(*List); ok {
		itemType, _ := ttype.OfType.(Input)
		if valueAST, ok := valueAST.(*ast.ListValue); ok {
			messagesReduce := []string{}
			for _, value := range valueAST.Values {
				_, messages := isValidLiteralValue(itemType, value)
				for idx, message := range messages {
					messagesReduce = append(messagesReduce, fmt.Sprintf(`In element #%v: %v`, idx+1, message))
				}
			}
			return (len(messagesReduce) == 0), messagesReduce
		}
		return isValidLiteralValue(itemType, valueAST)

	}

	// Input objects check each defined field and look for undefined fields.
	if ttype, ok := ttype.(*InputObject); ok {
		valueAST, ok := valueAST.(*ast.ObjectValue)
		if !ok {
			return false, []string{fmt.Sprintf(`Expected "%v", found not an object.`, ttype.Name())}
		}
		fields := ttype.Fields()
		messagesReduce := []string{}

		// Ensure every provided field is defined.
		fieldASTs := valueAST.Fields
		fieldASTMap := map[string]*ast.ObjectField{}
		for _, fieldAST := range fieldASTs {
			fieldASTName := ""
			if fieldAST.Name != nil {
				fieldASTName = fieldAST.Name.Value
			}

			fieldASTMap[fieldASTName] = fieldAST

			field, ok := fields[fieldASTName]
			if !ok || field == nil {
				messagesReduce = append(messagesReduce, fmt.Sprintf(`In field "%v": Unknown field.`, fieldASTName))
			}
		}
		// Ensure every defined field is valid.
		for fieldName, field := range fields {
			fieldAST, _ := fieldASTMap[fieldName]
			var fieldASTValue ast.Value
			if fieldAST != nil {
				fieldASTValue = fieldAST.Value
			}
			if isValid, messages := isValidLiteralValue(field.Type, fieldASTValue); !isValid {
				for _, message := range messages {
					messagesReduce = append(messagesReduce, fmt.Sprintf("In field \"%v\": %v", fieldName, message))
				}
			}
		}
		return (len(messagesReduce) == 0), messagesReduce
	}

	if ttype, ok := ttype.(*Scalar); ok {
		if isNullish(ttype.ParseLiteral(valueAST)) {
			return false, []string{fmt.Sprintf(`Expected type "%v", found %v.`, ttype.Name(), printer.Print(valueAST))}
		}
	}
	if ttype, ok := ttype.(*Enum); ok {
		if isNullish(ttype.ParseLiteral(valueAST)) {
			return false, []string{fmt.Sprintf(`Expected type "%v", found %v.`, ttype.Name(), printer.Print(valueAST))}
		}
	}

	return true, nil
}

// Internal struct to sort results from suggestionList()
type suggestionListResult struct {
	Options   []string
	Distances []float64
}

func (s suggestionListResult) Len() int {
	return len(s.Options)
}
func (s suggestionListResult) Swap(i, j int) {
	s.Options[i], s.Options[j] = s.Options[j], s.Options[i]
}
func (s suggestionListResult) Less(i, j int) bool {
	return s.Distances[i] < s.Distances[j]
}

// suggestionList Given an invalid input string and a list of valid options, returns a filtered
// list of valid options sorted based on their similarity with the input.
func suggestionList(input string, options []string) []string {
	dists := []float64{}
	filteredOpts := []string{}
	inputThreshold := float64(len(input) / 2)

	for _, opt := range options {
		dist := lexicalDistance(input, opt)
		threshold := math.Max(inputThreshold, float64(len(opt)/2))
		threshold = math.Max(threshold, 1)
		if dist <= threshold {
			filteredOpts = append(filteredOpts, opt)
			dists = append(dists, dist)
		}
	}
	//sort results
	suggested := suggestionListResult{filteredOpts, dists}
	sort.Sort(suggested)
	return suggested.Options
}

// lexicalDistance Computes the lexical distance between strings A and B.
// The "distance" between two strings is given by counting the minimum number
// of edits needed to transform string A into string B. An edit can be an
// insertion, deletion, or substitution of a single character, or a swap of two
// adjacent characters.
// This distance can be useful for detecting typos in input or sorting
func lexicalDistance(a, b string) float64 {
	d := [][]float64{}
	aLen := len(a)
	bLen := len(b)
	for i := 0; i <= aLen; i++ {
		d = append(d, []float64{float64(i)})
	}
	for k := 1; k <= bLen; k++ {
		d[0] = append(d[0], float64(k))
	}

	for i := 1; i <= aLen; i++ {
		for k := 1; k <= bLen; k++ {
			cost := 1.0
			if a[i-1] == b[k-1] {
				cost = 0.0
			}
			minCostFloat := math.Min(
				d[i-1][k]+1.0,
				d[i][k-1]+1.0,
			)
			minCostFloat = math.Min(
				minCostFloat,
				d[i-1][k-1]+cost,
			)
			d[i] = append(d[i], minCostFloat)

			if i > 1 && k < 1 &&
				a[i-1] == b[k-2] &&
				a[i-2] == b[k-1] {
				d[i][k] = math.Min(d[i][k], d[i-2][k-2]+cost)
			}
		}
	}

	return d[aLen][bLen]
}
