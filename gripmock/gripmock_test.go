package main

import (
	"os"
	"testing"
	"path"
	"strings"
	"bytes"
	"path/filepath"
	"github.com/lithammer/dedent"
	"github.com/stretchr/testify/assert"
)

// These tests use the real file system to resolve paths because golang lacks a
// decent native way to mock out filesystem access without cumbersome extra
// tools and abstractions.
const exampleDir = "../example"

func init() {
	initLogging(4);
}

func Test_findProtoInImports(t *testing.T) {
	// helper to get relative path to an example file
	rel := func(relproto string) string {
		return path.Join(exampleDir, relproto)
	}

	_, err := os.Stat(exampleDir)
	assert.NoError(t, err, "example dir must exist at \"" + exampleDir + "\"")

	// Workdir required for abspath based tests
	absExampleDir, err := filepath.Abs(exampleDir)
	assert.NoError(t, err)
	// helper to get absolute path to an example file
	abs := func(relproto string) string {
		return path.Join(absExampleDir, relproto)
	}
	
	type args struct {
		protoPath string
		imports   []string
	}
	type result struct {
		imp string
		rel string
	}
	tests := []struct {
		name string
		args args
		res result
		errMatch []string
	}{
		{
			name: "empty input",
			args: args{
				protoPath: "",
				imports:   []string{},
			},
			errMatch: []string{"empty input"},
		},
		{
			// This func doesn't care about the actual path
			// existence for relative paths and will happily return
			// a dir or some other file
			name: "input is an existing relpath directory",
			args: args{
				protoPath: rel("multi-package"),
				imports:   []string{},
			},
			errMatch: []string{"is a directory"},
		},
		{
			// Gripmock will use the protocol's full dirname as the
			// implied import dir if the proto isn't found on the
			// import path. This works for most simple cases, but
			// will fall over if the proto imports other other
			// protos within the same proto tree.
			name: "deduced",
			args: args{
				protoPath: rel("multi-package/hello.proto"),
				imports:   []string{},
			},
			res: result{
				imp: rel("multi-package"),
				rel: ".",
			},
		},
		{
			name: "specified in rel import root, relpath",
			args: args{
				protoPath: "hello.proto",
				imports:   []string{rel("multi-package")},
			},
			res: result{
				imp: rel("multi-package"),
				rel: ".",
			},
		},
		{
			name: "specified in rel import subdir, relpath",
			args: args{
				protoPath: "bar/bar.proto",
				imports:   []string{rel("multi-package")},
			},
			res: result{
				imp: rel("multi-package"),
				rel: "bar",
			},
		},
		{
			name: "specified in rel import root, abspath",
			args: args{
				protoPath: abs("multi-package/hello.proto"),
				imports:   []string{rel("multi-package")},
			},
			res: result{
				imp: rel("multi-package"),
				rel: ".",
			},
		},
		{
			name: "specified in rel import subdir, abspath",
			args: args{
				protoPath: abs("multi-package/bar/bar.proto"),
				imports:   []string{rel("multi-package")},
			},
			res: result{
				imp: rel("multi-package"),
				rel: "bar",
			},
		},
		{
			name: "specified in abs import root, relpath",
			args: args{
				protoPath: "hello.proto",
				imports:   []string{abs("multi-package")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: ".",
			},
		},
		{
			name: "specified in abs import subdir, relpath",
			args: args{
				protoPath: "bar/bar.proto",
				imports:   []string{abs("multi-package")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: "bar",
			},
		},
		{
			name: "specified in abs import root, abspath",
			args: args{
				protoPath: abs("multi-package/hello.proto"),
				imports:   []string{abs("multi-package")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: ".",
			},
		},
		{
			name: "specified in abs import subdir, abspath",
			args: args{
				protoPath: abs("multi-package/bar/bar.proto"),
				imports:   []string{abs("multi-package")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: "bar",
			},
		},
		{
			name: "nonexistent relpath",
			args: args{
				protoPath: "nosuch.proto",
				imports:   []string{rel("multi-package")},
			},
			// should really use proper typed errors
			errMatch: []string{"could not find proto", "on import path"},
		},
		{
			name: "nonexistent abspath",
			args: args{
				protoPath: abs("nosuch.proto"),
				imports:   []string{rel("multi-package")},
			},
			errMatch: []string{"could not find proto", "on import path"},
		},
		{
			// the proto file exists at given absolute path but is
			// not within the import path. Gripmock generates an implicit
			// import for the full dir of the proto file.
			name: "abs import path does not contain abs proto",
			args: args{
				protoPath: abs("multi-package/hello.proto"),
				imports:   []string{abs("simple")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: ".",
			},

		},
		{
			// Nonexistent paths do not break the protocol search,
			// and it will fall back to the implicit proto dir
			name: "nonexistent import path entries, abs proto",
			args: args{
				protoPath: abs("multi-package/hello.proto"),
				imports:   []string{"/nosuch", abs("missing"), rel("norel")},
			},
			res: result{
				imp: abs("multi-package"),
				rel: ".",
			},
		},
		{
			// Same for relative proto path; nonexistent search path
			// entries do not break finding the protocol and falling
			// back to the implicit protocol
			name: "nonexistent import path entries, rel proto",
			args: args{
				protoPath: rel("multi-package/hello.proto"),
				imports:   []string{"/nosuch", abs("missing"), rel("norel")},
			},
			res: result{
				imp: rel("multi-package"),
				rel: ".",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res result
			var err error
			log.V(0).Info("------------ starting test " + tt.name + "-----------")
			res.imp, res.rel, err = findProtoInImports(tt.args.imports, tt.args.protoPath)
			assert.Equal(t, tt.res, res)
			if len(tt.errMatch) == 0 {
				assert.NoErrorf(t, err, "expect no error")
			} else {
				for _, m := range tt.errMatch {
					assert.ErrorContains(t, err, m, "match expected error")
				}
			}
			log.V(0).Info("------------ end of test " + tt.name + "----------")
		})
	}
}

// Validate "option go_package" transforms in proto files
func Test_fixGoPackageProtoStream(t *testing.T) {
	dummypkg := path.Join(GENERATED_MODULE_NAME, "subpkg")
	tests := []struct{
		name string
		in string
		newPackage string
		out string
		errMatch []string
	}{
		{
			name: "empty package",
			in: ``,
			newPackage: "",
			errMatch: []string{`empty package name`},
		},
		{
			name: "empty input",
			in: ``,
			newPackage: dummypkg,
			errMatch: []string{`no "syntax" line found when scanning proto file`},
		},
		{
			name: "only syntax line no go_package",
			in: `syntax = "proto3";
`,
			newPackage: dummypkg,
			out: `syntax = "proto3";
option go_package = "gripmock/generated/subpkg";

`,
		},
		{
			name: "with go_package",
			in: `syntax = "proto3";
option go_package = "some/prev/package";
`,
			newPackage: dummypkg,
			out: `syntax = "proto3";
option go_package = "gripmock/generated/subpkg";

`,
		},
		{
			name: "with go_package noeof",
			in: `syntax = "proto3";
option go_package = "some/prev/package";`,
			newPackage: dummypkg,
			out: `syntax = "proto3";
option go_package = "gripmock/generated/subpkg";

`,
		},
		{
			name: "basic valid proto file",
			in: `
# copy of example/simple/simple.proto
syntax = "proto3";

package simple;

option go_package = "gripmock/generated/subpkg";

// The Gripmock service definition.
service Gripmock {
  // simple unary method
  rpc SayHello (Request) returns (Reply);
}

// The request message containing the user's name.
message Request {
  string name = 1;
}

// The response message containing the greetings
message Reply {
  string message = 1;
  int32 return_code = 2;
}
`,
			newPackage: dummypkg,
			out: `
# copy of example/simple/simple.proto
syntax = "proto3";
option go_package = "gripmock/generated/subpkg";


package simple;


// The Gripmock service definition.
service Gripmock {
  // simple unary method
  rpc SayHello (Request) returns (Reply);
}

// The request message containing the user's name.
message Request {
  string name = 1;
}

// The response message containing the greetings
message Reply {
  string message = 1;
  int32 return_code = 2;
}
`,
		},
		{
			// this is invalid protobuf, but the func doesn't care
			name: "go_package first",
			in: `
option go_package = "some/prev/package";
syntax = "proto3";
`,
			newPackage: dummypkg,
			out: `
syntax = "proto3";
option go_package = "gripmock/generated/subpkg";

`,
		},
		{
			// The func will accept any "syntax" line and doesn't try to parse
			// for correctness.
			name: "syntax present but invalid",
			in: `syntax this is garbage`,
			newPackage: dummypkg,
			out: `syntax this is garbage
option go_package = "gripmock/generated/subpkg";

`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.NewReader(dedent.Dedent(tt.in))
			var out bytes.Buffer
			err := fixGoPackageProtoStream(in, tt.newPackage, &out)
			if len(tt.errMatch) == 0 {
				assert.NoErrorf(t, err, "expect no error")
				assert.Equal(t, dedent.Dedent(string(out.Bytes())), tt.out)
			} else {
				for _, m := range tt.errMatch {
					assert.ErrorContains(t, err, m, "match expected error")
				}
			}
		})
	}
}
