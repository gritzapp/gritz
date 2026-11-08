// Command protoc-gen-gritz-docs is a protoc plugin that writes the API
// reference pages of docs.gritz.dev from the proto files. buf generate runs it;
// see buf.gen.yaml.
package main

import (
	"flag"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/gritzapp/gritz/internal/protodocs"
)

// main runs the plugin: it parses the package= option and calls
// protodocs.Generate.
func main() {
	var flags flag.FlagSet
	pkg := flags.String("package", "", "the proto package to document, such as gritz.v1")
	protogen.Options{ParamFunc: flags.Set}.Run(func(gen *protogen.Plugin) error {
		gen.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		return protodocs.Generate(gen, protodocs.Options{Package: protoreflect.FullName(*pkg)})
	})
}
