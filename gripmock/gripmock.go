package main

/*
 * "gripmock" is a wrapper to generate the golang protocol files and server
 * implementation from the input .proto files.
 *
 * It invokes protoc to generate the regular golang protobuf client/server
 * packages and a custom "protoc-gen-gripmock" plugin to generate the server
 * implementation.
 * 
 * The server implementation is based on a "server.tmpl" file that's populated
 * with setup code based on the protocol(s) it should support and linked with
 * the stub loading support code.
 *
 * Once the files are all generated, gripmock compiles them to generate a
 * server binary and by default invokes the server binary. The main gripmock
 * binary runs to serve stubs for the API server(s).
 */

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"io"
	"os/exec"
	"os/signal"
	stdlog "log"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"

	"github.com/ringerc/gripmock/stub"
)

const (
	// The generated server uses this module name, so it won't clash with
	// anything that go tools might download from the Internet
	GENERATED_MODULE_NAME="gripmock/generated"

	EXITCODE_OTHER_ERROR = 1
	EXITCODE_BUILD_ERROR = 2
	EXITCODE_RUNTIME_ERROR = 3
	EXITCODE_ARGUMENTS_ERROR = 4

	LOG_ERROR = 0
	LOG_INFO = 1
	LOG_VERBOSE = 2
	LOG_DEBUG = 3
	LOG_TRACE = 4
)

var log logr.Logger

func main() {
	outputPointer := flag.String("o", "generated", "directory to output generated files and binaries. Default is \"generated\"")
	templateDir := flag.String("template-dir", "", "path to directory containing server.tmpl and its go.mod, uses compiled-in template by default")
	grpcPort := flag.String("grpc-port", "4770", "Port of gRPC tcp server")
	grpcBindAddr := flag.String("grpc-listen", "", "Adress the gRPC server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	adminport := flag.String("admin-port", "4771", "Port of stub admin server")
	adminBindAddr := flag.String("admin-listen", "", "Adress the admin server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	stubPath := flag.String("stub", "", "Path where the stub files are (Optional)")
	imports := flag.String("imports", "", "comma separated imports path to search for dependency .proto files")
	logVerbosity := flag.Int("verbosity", LOG_INFO, "log verbosity [0..4], default 1")

	// for backwards compatibility
	if os.Args[1] == "gripmock" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	flag.Parse()

	initLogging(*logVerbosity)

	log.V(LOG_VERBOSE).Info("Starting GripMock")

	output := *outputPointer
	if output == "" {
		log.V(LOG_ERROR).Info("output dir may not be empty")
		os.Exit(EXITCODE_ARGUMENTS_ERROR)
	}
	if _, err := os.Stat(output); os.IsNotExist(err) {
		if err := os.Mkdir(output, os.ModePerm); err != nil {
			log.Error(err, "creating output directory", "dir", output)
			os.Exit(EXITCODE_OTHER_ERROR)
		}
	}

	// run admin stub server
	stub.RunStubServer(stub.Options{
		StubPath: *stubPath,
		Port:     *adminport,
		BindAddr: *adminBindAddr,
	})

	// parse proto files
	protoPaths := flag.Args()

	if len(protoPaths) == 0 {
		log.V(LOG_ERROR).Info("Need at least one proto file")
		os.Exit(EXITCODE_ARGUMENTS_ERROR)
	}

	importDirs := strings.Split(*imports, ",")

	// generate pb.go and grpc server based on proto
	if err := generateProtoc(protocParam{
		protoPath:   protoPaths,
		adminPort:   *adminport,
		grpcAddress: *grpcBindAddr,
		grpcPort:    *grpcPort,
		output:      output,
		imports:     importDirs,
		templateDir:    *templateDir,
	}); err != nil {
		log.Error(err, "when generating protocol and server")
		os.Exit(EXITCODE_BUILD_ERROR)
	}

	// Build the server binary
	if err := buildServer(output); err != nil {
		log.Error(err, "building gRPC server")
		os.Exit(EXITCODE_BUILD_ERROR)
	}

	// and run
	run, runerrchan := runGrpcServer(output)

	var sigchan = make(chan os.Signal)
	signal.Notify(sigchan, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case err := <-runerrchan:
			switch e := err.(type) {
			case *exec.ExitError:
				log.V(LOG_INFO).Info("gRPC server exited", "exitcode", e.ExitCode())
				if e.Success() {
					os.Exit(0)
				} else {
					os.Exit(EXITCODE_RUNTIME_ERROR)
				}
			default:
				log.V(LOG_INFO).Error(e, "gRPC server exited", "error")
			}
		case <-sigchan:
			log.V(LOG_DEBUG).Info("Caught signal, stopping gRPC Server")
			run.Process.Kill()
			// Now wait for child exit
		}
	}

	panic("unreachable")
}

func initLogging(level int) {
	log = stdr.New(stdlog.New(os.Stdout, "", 0))
	stdr.SetVerbosity(level)
}

type protocParam struct {
	protoPath   []string
	adminPort   string
	grpcAddress string
	grpcPort    string
	output      string
	imports     []string
	templateDir string
}

func generateProtoc(param protocParam) error {
	log.V(LOG_VERBOSE).Info("Generating server protocol", "input", param.protoPath, "output", param.output)

	// Generate new .proto files under param.output and update param.protoPath
	// and param.imports to point to them instead of the original user inputs
	if err := fixGoPackages(&param); err != nil {
		return fmt.Errorf("Munging proto files: %w", err)
	}

	// Always search the generated protos dir first, since that will ensure
	// any proto files we rewrote with new package names will appear before
	// any of the well-known types and other protos our proto files may
	// have imported but do not serve.
	args := []string{"-I", param.output}
	for _, imp := range param.imports {
		args = append(args, "-I", imp)
	}
	args = append(args, param.protoPath...)
	args = append(args,
		"--go_out="+param.output,
		"--go_opt=module="+GENERATED_MODULE_NAME,
		"--go-grpc_out="+param.output,
		"--go-grpc_opt=module="+GENERATED_MODULE_NAME,
	)
	args = append(args,
		"--gripmock_out="+param.output,
		"--gripmock_opt=paths=source_relative",
		"--gripmock_opt=admin-port="+param.adminPort,
		"--gripmock_opt=grpc-address="+param.grpcAddress,
		"--gripmock_opt=grpc-port="+param.grpcPort,
		"--gripmock_opt=template-dir="+param.templateDir,
	)
	protoc := exec.Command("protoc", args...)
	protoc.Stdout = os.Stdout
	protoc.Stderr = os.Stderr
	log.V(LOG_VERBOSE).Info("invoking \"protoc\"", "cmd", protoc.String())
	if err := protoc.Run(); err != nil {
		return fmt.Errorf("running protoc: %w", err)
	}

	log.V(LOG_VERBOSE).Info("Generated protocol and server")

	return nil
}

// Generate a go package name for input proto file 'protoPath';
// return the package name relative to the GENERATED_MODULE_NAME
// prefix.
//
// Returns: import path protocol matched on, protocol path relative to import dir
//
// The go package could be anything unique. But protoc imports
// expect to find the proto files relative to the import path,
// so we're going to need to preserve that in the output.
//
func findProtoInImports(importPaths []string, protoPath string) (string, string, error) {
	// Search the original source import path(s) for the proto file(s)
	// requested. Returns the import path the proto is within and the relative
	// path to the proto file's containing dir within that import path.
	// 
	// If the proto is a relative path, search for that relative path under
	// each import and return it if found.
	//
	log.V(LOG_TRACE).Info("Searching import paths for protocol file", "proto", protoPath, "importPaths", importPaths)
	if protoPath == "" {
		return "", "", fmt.Errorf("empty input")
	}
	protoPath = filepath.Clean(protoPath)
	var matchedImp string
	var matchedRel string
	for _, imp := range importPaths {
		imp = filepath.Clean(imp)
		log.V(LOG_TRACE).Info("checking import", "dir", imp, "proto", protoPath)

		if filepath.IsAbs(protoPath) {
			// If the proto is an absolute path, check if any import has that
			// prefix, and return the remainder of the path. This isn't a very
			// smart operation; it will fall over on case-insensitive file
			// systems, and it doesn't try to resolve symlinks. Any relative
			// import paths are made relative to the gripmock CWD.
			//
			// Relative import paths are converted to absolute paths using the
			// gripmock working directory as a base.
			//
			absImp, err := filepath.Abs(imp)
			if err != nil {
				return "", "", fmt.Errorf("making path %s absolute: %w", imp, err)
			}
			log.V(LOG_TRACE).Info("testing path containment", "containerPath", absImp, "containedPath", protoPath)
			relPath, err := filepath.Rel(absImp, protoPath)
			// We have to exclude relative paths that descend because filepath.Rel
			// will generate a descending relative path if given two absolute paths
			if err == nil && relPath != "" && !strings.HasPrefix(relPath, "../") {
				log.V(LOG_TRACE).Info("matched absolute path prefix", "proto", protoPath, "dir", imp, "absdir", absImp, "rel", relPath)

				rel := path.Dir(relPath)
				// Sanity check that the proto file is actually on the matched
				// import path, since filepath.Rel is just a lexical check.
				derivedPath := path.Join(imp, rel, path.Base(protoPath))
				if _, err := os.Stat(derivedPath); err != nil {
					log.V(LOG_TRACE).Info("protocol file appears to be within import path, but could not stat() file",
										 "derived proto path to stat", derivedPath, "error", err)
					// We'll keep searching later import dirs, this
					// one didn't contain the file
				} else {
					// Proto file exists in this import dir. Return the import path and
					// the proto path relative to the import path.
					matchedImp = imp
					matchedRel = rel
					log.V(LOG_TRACE).Info("found abs path protocol file",
										   "derived path", derivedPath,
									       "import", matchedImp,
									       "relpath", matchedRel)
					break;
				}
			}
		} else {
			// The proto is a relative path. Search each import directory for
			// it, and if the proto file exists, return the path to the proto
			// file's directory relative to the matching import dir. It's
			// irrelevant whether the import path is relative or absolute for
			// this. We don't recurse inside the directories, we only care about
			// whether the proto path can be found within the top level importdir.
			importProtoPath := path.Join(imp, protoPath)
			log.V(LOG_TRACE).Info("testing path existence", "path", importProtoPath)
			if _, err := os.Stat(importProtoPath); !os.IsNotExist(err) {
				matchedImp = imp
				matchedRel = path.Dir(protoPath)
				log.V(LOG_TRACE).Info("matched relative path", "proto", protoPath, "dir", imp, "fullPath", importProtoPath)
				break
			}
		}
	}

	if matchedRel == "" && matchedImp == "" {
		// If the proto path exist (rel or abs) but isn't in the import path,
		// we'll add its full path as an implicit import path entry. This isn't
		// recommended, but was the previous gripmock behaviour, so is retained
		// for BC. It won't work correctly if the proto imports any other protos
		// and isn't itself in the "base" directory of its proto tree; protoc
		// will fail to find its imports.
		if _, err := os.Stat(protoPath); !os.IsNotExist(err) {
			if ! filepath.IsAbs(protoPath) {
				wd, _ := os.Getwd()
				log.V(LOG_VERBOSE).Info(fmt.Sprintf("Protocol file \"%s\" not found on any import path, but WAS found relative to the gripmock working directory \"%s\".", protoPath, wd))
			}
			matchedImp = path.Dir(protoPath)
			matchedRel = "."
			log.V(LOG_INFO).Info("WARNING: adding proto file's containing dir as implicit import path. You should specify an appropriate path on the -imports list instead.", "importpath", matchedImp)
		} else {
			log.V(LOG_INFO).Info("Protocol file not found on any import path, see README for details") 
			return "", "", fmt.Errorf("could not find proto \"%s\" on import path", protoPath)
		}

	}

	// Sanity check that the proto file is actually on the matched import path
	derivedPath := path.Join(matchedImp, matchedRel, path.Base(protoPath))
	fileinfo, err := os.Stat(derivedPath)
	if err != nil {
		return "", "", fmt.Errorf("cannot stat proto file at path \"%s\": %w", derivedPath, err)
	}
	if fileinfo.IsDir() {
		return "", "", fmt.Errorf("path \"%s\" is a directory", derivedPath)
	}

	return matchedImp, matchedRel, nil
}

// Stream transformation that rewrites a .proto file's go_package directive
// to point to new_package
func fixGoPackageProtoStream(in io.Reader, newPackage string, out io.Writer) error {
	if newPackage == "" {
		return fmt.Errorf("empty package name")
	}

	s := bufio.NewScanner(in)
	s.Split(bufio.ScanLines)

	ow := bufio.NewWriter(out)

	var err error
	foundSyntaxLine := false
	var matched bool
	for s.Scan() {
		l := s.Text()

		// Any go_package line must be omitted, since we'll be writing a
		// replacement for it.
		if matched, err = regexp.MatchString("^option[ \\t]+go_package[ \\t]+=", l); err != nil {
			return err
		}
		if matched {
			continue
		}

		if matched, err = regexp.MatchString("^syntax[ \\t]", l); err != nil {
			return err
		}
		if matched {
			if foundSyntaxLine {
				return fmt.Errorf("Found more than one \"syntax\" statement")
			}
			foundSyntaxLine = true;
			// Immediately after the "syntax" line, add our own option
			// go_package line to override the protocol's real package
			// with one we will generate
			l = l + fmt.Sprintf("\noption go_package = \"%s\";\n", newPackage)
		}

		// Write (possibly modified) line(s) to the new proto file
		if _, err := ow.WriteString(l + "\n"); err != nil {
			return err
		}
	}

	if err := ow.Flush(); err != nil {
		return err
	}
	if err := s.Err(); err != nil {
		return err
	}

	if ! foundSyntaxLine {
		return fmt.Errorf("no \"syntax\" line found when scanning proto file")
	}

	return nil
}

// Rewrite the .proto file to replace any go_package directive with one based
// on our local package path for generated servers, GENERATED_MODULE_NAME .
// Write the new file to the provided path.
//
func fixGoPackage(protoPath string, newPackage string, outPath string) error {
	in, err := os.Open(protoPath)
	if err != nil {
		return fmt.Errorf("opening input proto file for reading: %w", err)
	}
	defer in.Close()

	of, err := os.OpenFile(outPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("opening output proto file for writing: %w", err)
	}
	defer of.Close()

	if err := fixGoPackageProtoStream(in, newPackage, of); err != nil {
		return fmt.Errorf("failed to munge proto file \"%s\": %w", protoPath, err)
	}
	log.V(LOG_DEBUG).Info("wrote modified proto file",
						   "original", protoPath,
						   "new package name", newPackage,
						   "output", outPath)
	return nil
}

// Make copies of the input protocol file(s) into the gripmock output
// directory, with each protocol file modified so that its go package is
// rewritten to a custom package.
//
// Returns list of resolved .proto file paths.
//
// The resulting proto files must have the same relative path to the output dir
// as the corresponding input did to the protoc import path that contained it.
// This is needed because protoc imports are resolved as paths relative to the
// protoc import path list and we don't want to have to rewrite import paths in
// the protocols we process.
//
func fixGoPackages(param *protocParam) error {
	outProtos := make([]string, len(param.protoPath))
	for i, proto := range param.protoPath {
		importDir, newPackageSuffix, err := findProtoInImports(param.imports, proto)
		if err != nil {
			return err
		}
		protoPath := path.Join(importDir, newPackageSuffix, path.Base(proto))
		outProtoDir := path.Join(param.output, newPackageSuffix)
		outProto := path.Join(outProtoDir, path.Base(proto))
		// Write a copy of the .proto file in outProto with the go_package
		// directive rewritten to point to the full package path, and the file
		// placed in in newPackageSuffix/{filename}.proto
		newPackage := path.Join(GENERATED_MODULE_NAME, newPackageSuffix)
		log.V(LOG_TRACE).Info("path resolution",
							  "input proto arg", proto,
							  "resolved input proto path", protoPath,
							  "import dir", importDir,
							  "package suffix", newPackageSuffix,
							  "output proto dir", outProtoDir,
							  "output proto file", outProto,
							  "full proto package", newPackage)
		if err := os.MkdirAll(outProtoDir, os.ModePerm); err != nil {
			return err
		}
		// Write a copy of the original proto in the output dir, preserving the
		// same path-prefix. Change the go_package directive to the new one for
		// locally generated proto files.
		if err := fixGoPackage(protoPath, newPackage, outProto); err != nil {
			return err
		}
		outProtos[i] = outProto
	}
	// Modify the protoc inputs to use our munged protocol files,
	// all of which are within the outputdir
	param.protoPath = outProtos
	return nil
}

func runGrpcServer(output string) (*exec.Cmd, <-chan error) {
	run := exec.Command(path.Join(output,"server"))
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	err := run.Start()
	if err != nil {
		log.Error(err, "starting grpc server")
		os.Exit(EXITCODE_RUNTIME_ERROR)
	}
	log.V(LOG_VERBOSE).Info("grpc server started", "pid", run.Process.Pid)
	runerr := make(chan error)
	go func() {
		runerr <- run.Wait()
	}()
	return run, runerr
}

func buildServer(output string) error {
	log.V(LOG_VERBOSE).Info("Building server")
	oldCwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getcwd(): %w", err)
	}
	if err := os.Chdir(output); err != nil {
		return fmt.Errorf("changing directory to %s: %w", output, err)
	}

	run := exec.Command("go", "mod", "edit", "-module", GENERATED_MODULE_NAME)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	log.V(LOG_DEBUG).Info("setting go.mod module name", "cmd", run.String())
	if err := run.Run(); err != nil {
		return fmt.Errorf("setting go.mod name: %w", err)
	}

	run = exec.Command("go", "mod", "tidy")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	log.V(LOG_DEBUG).Info("tidying go.mod", "cmd", run.String())
	if err := run.Run(); err != nil {
		return fmt.Errorf("tidying go.mod: %w", err)
	}

	run = exec.Command("go", "build", "-o", "server", "./cmd/...")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	log.V(LOG_DEBUG).Info("building gRPC server from module", "cmd", run.String())
	if err := run.Run(); err != nil {
		return fmt.Errorf("building server: %w", err)
	}

	if err := os.Chdir(oldCwd); err != nil {
		return fmt.Errorf("returning to old working directory: %w", err)
	}
	log.Info("Built server", "path", path.Join(output,"server"))

	return nil
}

// vim: sw=4 ts=4 noet ai
