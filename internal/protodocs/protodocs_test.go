package protodocs

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/pluginpb"
	"gotest.tools/v3/assert"
)

const testPackage = "test.v1"

// testFile is a proto file of package test.v1:
//
//	enum Status { UNSPECIFIED = 0; DONE = 1 [deprecated = true]; }
//	message Item {}
//	message Thing {
//	  string name = 1;
//	  repeated string tags = 2;
//	  optional int64 count = 3;
//	  map<string, string> labels = 4;
//	  map<string, Item> items = 5;
//	  Item item = 6;
//	  repeated Item list = 7;
//	  Status status = 8;
//	  google.protobuf.Timestamp created_at = 9;
//	  oneof payload { string text = 10; Item other = 11; }
//	  string old = 12 [deprecated = true];
//	}
func testFile() *descriptorpb.FileDescriptorProto {
	field := func(name string, number int32, typ descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			JsonName: proto.String(name),
			Number:   proto.Int32(number),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
		}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	repeated := func(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
		f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		return f
	}
	inOneof := func(f *descriptorpb.FieldDescriptorProto, index int32) *descriptorpb.FieldDescriptorProto {
		f.OneofIndex = proto.Int32(index)
		return f
	}
	deprecated := func(f *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
		f.Options = &descriptorpb.FieldOptions{Deprecated: proto.Bool(true)}
		return f
	}
	mapEntry := func(name string, value *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{
			Name: proto.String(name),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
				value,
			},
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
		}
	}
	const (
		str = descriptorpb.FieldDescriptorProto_TYPE_STRING
		msg = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	)
	count := inOneof(field("count", 3, descriptorpb.FieldDescriptorProto_TYPE_INT64, ""), 1)
	count.Proto3Optional = proto.Bool(true)
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test/v1/test.proto"),
		Package:    proto.String(testPackage),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String("example.com/test/v1;testv1")},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("DONE"), Number: proto.Int32(1), Options: &descriptorpb.EnumValueOptions{Deprecated: proto.Bool(true)}},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Item")},
			{
				Name: proto.String("Thing"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("name", 1, str, ""),
					repeated(field("tags", 2, str, "")),
					count,
					repeated(field("labels", 4, msg, ".test.v1.Thing.LabelsEntry")),
					repeated(field("items", 5, msg, ".test.v1.Thing.ItemsEntry")),
					field("item", 6, msg, ".test.v1.Item"),
					repeated(field("list", 7, msg, ".test.v1.Item")),
					field("status", 8, descriptorpb.FieldDescriptorProto_TYPE_ENUM, ".test.v1.Status"),
					field("created_at", 9, msg, ".google.protobuf.Timestamp"),
					inOneof(field("text", 10, str, ""), 0),
					inOneof(field("other", 11, msg, ".test.v1.Item"), 0),
					deprecated(field("old", 12, str, "")),
				},
				NestedType: []*descriptorpb.DescriptorProto{
					mapEntry("LabelsEntry", field("value", 2, str, "")),
					mapEntry("ItemsEntry", field("value", 2, msg, ".test.v1.Item")),
				},
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: proto.String("payload")},
					{Name: proto.String("_count")},
				},
			},
		},
	}
}

func testMessage(t *testing.T, name protoreflect.Name) protoreflect.MessageDescriptor {
	t.Helper()
	f, err := protodesc.NewFile(testFile(), protoregistry.GlobalFiles)
	assert.NilError(t, err)
	return f.Messages().ByName(name)
}

// generate runs Generate on files, the last of which is to be generated, and
// returns the pages it writes by path.
func generate(t *testing.T, files ...*descriptorpb.FileDescriptorProto) (map[string]string, error) {
	t.Helper()
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{files[len(files)-1].GetName()},
		ProtoFile:      append([]*descriptorpb.FileDescriptorProto{protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto)}, files...),
	}
	gen, err := protogen.Options{}.New(req)
	assert.NilError(t, err)
	if err := Generate(gen, Options{Package: testPackage}); err != nil {
		return nil, err
	}
	resp := gen.Response()
	assert.Equal(t, resp.GetError(), "")
	pages := map[string]string{}
	for _, f := range resp.File {
		pages[f.GetName()] = f.GetContent()
	}
	return pages, nil
}

func TestFieldType(t *testing.T) {
	thing := testMessage(t, "Thing")
	tests := []struct {
		field protoreflect.Name
		want  string
	}{
		{"name", "`string`"},
		{"tags", "`repeated string`"},
		{"count", "`optional int64`"},
		{"labels", "`map<string, string>`"},
		{"items", "map&lt;`string`, [Item](/api/messages/item/)&gt;"},
		{"item", "[Item](/api/messages/item/)"},
		{"list", "repeated [Item](/api/messages/item/)"},
		{"status", "[Status](/api/enums/status/)"},
		{"created_at", "[google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp)"},
	}
	for _, tt := range tests {
		t.Run(string(tt.field), func(t *testing.T) {
			got, err := fieldType(thing.Fields().ByName(tt.field), testPackage)

			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestDocLines(t *testing.T) {
	cs := protogen.CommentSet{
		LeadingDetached: []protogen.Comments{" A contributor note.\n"},
		Leading:         " The first line.\n   indented\n",
		Trailing:        " The trailing comment.\n",
	}

	got := docLines(cs)

	assert.DeepEqual(t, got, []string{"The first line.", "  indented", "", "The trailing comment."})
}

func TestFirstSentence(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"none", nil, ""},
		{"sentence", []string{"Task is a unit of work. It runs", "in a sandbox."}, "Task is a unit of work."},
		{"wrapped", []string{"Task is a unit", "of work. It runs."}, "Task is a unit of work."},
		{"paragraph", []string{"Task is a unit of work", "", "Second paragraph."}, "Task is a unit of work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, firstSentence(tt.lines), tt.want)
		})
	}
}

func TestCellMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"none", nil, ""},
		{"lines", []string{"Max tasks to return", "  (default: 50)."}, "Max tasks to return (default: 50)."},
		{"paragraphs", []string{"First.", "", "Second."}, "First.<br><br>Second."},
		{"pipe", []string{"One of `a|b`."}, "One of `a\\|b`."},
		{"inline markdown", []string{"Set `path` and *line*, see [docs](https://docs.gritz.dev)."}, "Set `path` and *line*, see [docs](https://docs.gritz.dev)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cellMarkdown(tt.lines)

			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestCellMarkdown_BlockSyntax(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		err   string
	}{
		{"list item", []string{"Values:", "- one"}, "a list item"},
		{"ordered list item", []string{"1. one"}, "a list item"},
		{"heading", []string{"# Title"}, "a heading"},
		{"fenced code", []string{"```", "x", "```"}, "a fenced code block"},
		{"indented code", []string{"Example:", "", "    x := 1"}, "an indented code block"},
		{"block quote", []string{"> quoted"}, "a block quote"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellMarkdown(tt.lines)

			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestGenerate(t *testing.T) {
	pages, err := generate(t, testFile())

	assert.NilError(t, err)
	assert.DeepEqual(t, sortedKeys(pages), []string{"enums/status.md", "messages/item.md", "messages/thing.md"})
	assert.Equal(t, pages["messages/item.md"], `---
# Code generated by protoc-gen-gritz-docs. DO NOT EDIT.
title: "Item"
description: "The `+"`Item`"+` message of the gritz API."
editUrl: https://github.com/gritzapp/gritz/edit/master/proto/test/v1/test.proto
---

This message has no fields.
`)
	assert.Assert(t, strings.Contains(pages["messages/thing.md"], "| `text` | `string` | One of `payload`. |\n"))
	assert.Assert(t, strings.Contains(pages["messages/thing.md"], "| `old` | `string` | Deprecated. |\n"))
	assert.Assert(t, strings.Contains(pages["enums/status.md"], "| `DONE` | 1 | Deprecated. |\n"))
}

func TestGenerate_SlugCollision(t *testing.T) {
	f := testFile()
	f.MessageType = append(f.MessageType, &descriptorpb.DescriptorProto{Name: proto.String("ITEM")})

	_, err := generate(t, f)

	assert.ErrorContains(t, err, "test.v1.Item and test.v1.ITEM both have the page messages/item.md")
}

func sortedKeys(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
