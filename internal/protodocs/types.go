package protodocs

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// wellKnownPackage is the package of the well-known types, which link to their
// entries on protobuf.dev.
const wellKnownPackage = "google.protobuf"

// relName is a type's full name below the package, such as "Task", or
// "Outer.Inner" for a nested type.
func relName(d protoreflect.Descriptor, pkg protoreflect.FullName) string {
	return strings.TrimPrefix(string(d.FullName()), string(pkg)+".")
}

// slug is the file name, and so the URL segment, of a type's page: its name
// below the package, lower-cased, with "." as "-". Astro lower-cases page ids,
// so the slug is already its own id.
func slug(d protoreflect.Descriptor, pkg protoreflect.FullName) string {
	return strings.ReplaceAll(strings.ToLower(relName(d, pkg)), ".", "-")
}

// pageURL is the root-relative URL of the page of a message or enum of pkg.
func pageURL(d protoreflect.Descriptor, pkg protoreflect.FullName) string {
	kind := "messages"
	if _, ok := d.(protoreflect.EnumDescriptor); ok {
		kind = "enums"
	}
	return fmt.Sprintf("/api/%s/%s/", kind, slug(d, pkg))
}

// fieldType renders a field's type for the Type column: a scalar in code, a
// message or enum of pkg as a link to its page, a well-known type as a link to
// protobuf.dev, wrapped in `repeated`, `optional` or `map<K, V>`. A type made
// only of scalars is one code span; otherwise `<` is written as `&lt;` so the
// Markdown renderer never sees a tag.
func fieldType(fd protoreflect.FieldDescriptor, pkg protoreflect.FullName) (string, error) {
	var prefix string
	switch {
	case fd.IsList():
		prefix = "repeated "
	case fd.HasOptionalKeyword():
		prefix = "optional "
	}
	if fd.IsMap() {
		k, kScalar, err := elemType(fd.MapKey(), pkg)
		if err != nil {
			return "", err
		}
		v, vScalar, err := elemType(fd.MapValue(), pkg)
		if err != nil {
			return "", err
		}
		if kScalar && vScalar {
			return fmt.Sprintf("`map<%s, %s>`", k, v), nil
		}
		return fmt.Sprintf("map&lt;%s, %s&gt;", code(k, kScalar), code(v, vScalar)), nil
	}
	t, scalar, err := elemType(fd, pkg)
	if err != nil {
		return "", err
	}
	if scalar {
		return "`" + prefix + t + "`", nil
	}
	return prefix + t, nil
}

// elemType renders a field's element type, ignoring its cardinality. scalar
// reports whether it is a scalar's bare name rather than a Markdown link.
func elemType(fd protoreflect.FieldDescriptor, pkg protoreflect.FullName) (text string, scalar bool, err error) {
	var d protoreflect.Descriptor
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		d = fd.Message()
	case protoreflect.EnumKind:
		d = fd.Enum()
	default:
		return fd.Kind().String(), true, nil
	}
	switch d.ParentFile().Package() {
	case pkg:
		return fmt.Sprintf("[%s](%s)", relName(d, pkg), pageURL(d, pkg)), false, nil
	case wellKnownPackage:
		return fmt.Sprintf("[%s](https://protobuf.dev/reference/protobuf/google.protobuf/#%s)",
			d.FullName(), strings.ToLower(string(d.Name()))), false, nil
	}
	return "", false, fmt.Errorf("type %s is neither in package %s nor a well-known type", d.FullName(), pkg)
}

func code(text string, scalar bool) string {
	if scalar {
		return "`" + text + "`"
	}
	return text
}

// yamlString quotes s for a frontmatter value. JSON strings are YAML.
func yamlString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(fmt.Sprintf("protodocs: encoding a string can't fail: %v", err))
	}
	return strings.TrimSuffix(b.String(), "\n")
}
