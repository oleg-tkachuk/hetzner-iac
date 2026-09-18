package clusterspec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// schemaPath is the JSON Schema editors validate the topology against.
const schemaPath = "../../../infra/cluster/cluster.schema.json"

// node is the part of a JSON Schema this test walks.
type node struct {
	Type                 string          `json:"type"`
	Properties           map[string]node `json:"properties"`
	Items                *node           `json:"items"`
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
	MaxLength            *int            `json:"maxLength"`
	Pattern              string          `json:"pattern"`
	Enum                 []string        `json:"enum"`
}

// TestSchema_DescribesExactlyTheTopologyStruct is the guard that makes the
// schema worth having.
//
// A schema is a second description of the same shape, so it is a new way for
// two halves to drift: a field added to the struct and not to the schema is
// flagged by the editor as unknown, and a field removed from the struct but
// left in the schema is autocompleted into a file that then fails to load.
// Neither shows up in a test suite unless something compares them.
func TestSchema_DescribesExactlyTheTopologyStruct(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	var schema node
	require.NoError(t, json.Unmarshal(raw, &schema))

	compare(t, "", reflect.TypeFor[clusterspec.Topology](), schema)
}

// compare walks a struct and a schema object together, reporting a field
// present in one and missing from the other.
func compare(t *testing.T, path string, structType reflect.Type, schema node) {
	t.Helper()

	for structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return
	}

	inStruct := map[string]reflect.Type{}

	for i := range structType.NumField() {
		field := structType.Field(i)

		name := jsonName(field.Tag.Get("json"))
		if name == "" || name == "-" {
			continue
		}

		inStruct[name] = field.Type
	}

	assert.ElementsMatch(t, keys(inStruct), keys(schema.Properties),
		"%s: the struct and the schema describe different fields", at(path))

	for name, fieldType := range inStruct {
		property, ok := schema.Properties[name]
		if !ok {
			continue
		}

		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		// A list of structs: compare the element against the schema's items.
		if fieldType.Kind() == reflect.Slice && fieldType.Elem().Kind() == reflect.Struct {
			require.NotNil(t, property.Items, "%s.%s: schema has no items", at(path), name)
			compare(t, path+"."+name, fieldType.Elem(), *property.Items)

			continue
		}

		compare(t, path+"."+name, fieldType, property)
	}
}

// TestSchema_ForbidsUnknownFieldsEverywhere keeps the schema strict.
//
// Without additionalProperties: false a typo is simply accepted: the editor
// says nothing, the loader ignores the key, and the cluster comes up missing
// whatever that key was meant to set.
func TestSchema_ForbidsUnknownFieldsEverywhere(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	var schema node
	require.NoError(t, json.Unmarshal(raw, &schema))

	forbidsUnknown(t, "", schema)
}

func forbidsUnknown(t *testing.T, path string, schema node) {
	t.Helper()

	if len(schema.Properties) > 0 {
		assert.JSONEq(t, "false", string(schema.AdditionalProperties),
			"%s: objects with named properties must set additionalProperties: false", at(path))
	}

	for name, property := range schema.Properties {
		if property.Items != nil {
			forbidsUnknown(t, path+"."+name, *property.Items)
		}

		forbidsUnknown(t, path+"."+name, property)
	}
}

// TestSchema_MatchesTheCommittedTopology proves the schema accepts the file it
// is written for — a schema nothing is checked against is decoration.
func TestSchema_MatchesTheCommittedTopology(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "infra", "cluster", "cluster.*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no committed topology to check the schema against")

	for _, path := range paths {
		topology, loadErr := clusterspec.LoadTopology(path)
		require.NoError(t, loadErr, path)
		require.NoError(t, topology.Validate(), path)
	}
}

func jsonName(tag string) string {
	for i := range len(tag) {
		if tag[i] == ',' {
			return tag[:i]
		}
	}

	return tag
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func at(path string) string {
	if path == "" {
		return "(root)"
	}

	return path
}

// TestSchema_BoundsMatchTheConstantsTheyMirror catches the drift the
// field-name comparison above cannot see.
//
// A schema restates limits, not just shapes, and a restated limit is a second
// copy. The first version of this schema said maxLength 40 where the package
// says 47 — a number invented while writing it — so an editor would have
// flagged seven characters of perfectly valid name. Nothing else would have
// reported that: the loader accepts what the schema rejects, so the two only
// disagree in the editor.
func TestSchema_BoundsMatchTheConstantsTheyMirror(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	var schema node
	require.NoError(t, json.Unmarshal(raw, &schema))

	name := schema.Properties["metadata"].Properties["name"]
	require.NotNil(t, name.MaxLength, "metadata.name has no maxLength")

	assert.Equal(t, clusterspec.MaxClusterNameLength, *name.MaxLength,
		"the schema and internal/pkg/clusterspec disagree on how long a cluster name may be")
}

func TestSchema_EnumsMatchTheValidator(t *testing.T) {
	t.Parallel()

	// The schema is what an editor validates against, and it accepts or
	// rejects a value before Validate ever runs. An enum the validator
	// disagrees with is worse than no enum: the editor either marks a legal
	// value as wrong, or autocompletes one the apply then refuses.
	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	var schema node
	require.NoError(t, json.Unmarshal(raw, &schema))

	architecture := schema.Properties["talos"].Properties["architecture"]
	require.NotEmpty(t, architecture.Enum, "talos.architecture has no enum")

	assert.ElementsMatch(t, clusterspec.Architectures, architecture.Enum,
		"the schema and internal/pkg/clusterspec disagree on which architectures exist")

	// The same check the location enum never had. It listed six values and the
	// validator's map listed six, independently — two spellings of one set,
	// and an editor that accepts what the apply then refuses.
	location := schema.Properties["placement"].Properties["location"]
	require.NotEmpty(t, location.Enum, "placement.location has no enum")

	assert.ElementsMatch(t, clusterspec.Locations, location.Enum,
		"the schema and internal/pkg/clusterspec disagree on which locations exist")
}
