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
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"regexp"
	"strings"
	"syscall"

	"github.com/ringerc/gripmock/stub"
)

const (
	// The generated server uses this module name, so it won't clash with
	// anything that go tools might download from the Internet
	GENERATED_MODULE_NAME="gripmock/generated"
)

var protoCounter = 0

func main() {
	outputPointer := flag.String("o", "generated", "directory to output generated files and binaries. Default is \"generated\"")
	templateDir := flag.String("template-dir", "", "path to directory containing server.tmpl and its go.mod, uses compiled-in template by default")
	grpcPort := flag.String("grpc-port", "4770", "Port of gRPC tcp server")
	grpcBindAddr := flag.String("grpc-listen", "", "Adress the gRPC server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	adminport := flag.String("admin-port", "4771", "Port of stub admin server")
	adminBindAddr := flag.String("admin-listen", "", "Adress the admin server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	stubPath := flag.String("stub", "", "Path where the stub files are (Optional)")
	imports := flag.String("imports", "", "comma separated imports path to search for dependency .proto files")
	// for backwards compatibility
	if os.Args[1] == "gripmock" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	flag.Parse()
	fmt.Println("Starting GripMock")
	output := *outputPointer

	// for safety
	output += "/"
	if _, err := os.Stat(output); os.IsNotExist(err) {
		os.Mkdir(output, os.ModePerm)
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
		log.Fatal("Need at least one proto file")
	}

	importDirs := strings.Split(*imports, ",")

	// generate pb.go and grpc server based on proto
	generateProtoc(protocParam{
		protoPath:   protoPaths,
		adminPort:   *adminport,
		grpcAddress: *grpcBindAddr,
		grpcPort:    *grpcPort,
		output:      output,
		imports:     importDirs,
		templateDir:    *templateDir,
	})

	// Build the server binary
	buildServer(output)

	// and run
	run, runerr := runGrpcServer(output)

	var term = make(chan os.Signal)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
	select {
	case err := <-runerr:
		log.Fatal(err)
	case <-term:
		fmt.Println("Stopping gRPC Server")
		run.Process.Kill()
	}
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

func generateProtoc(param protocParam) {
	log.Printf("Generating server protocol %s to %s...", param.protoPath, param.output)
	var err error
	param.protoPath, err = fixGoPackages(param.protoPath, param.output)
	if err != nil {
		log.Fatalf("Munging proto files: %v", err)
	}

	args := []string{
		"-I", param.output,
	}
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
	log.Printf("invoking \"protoc\" with args %v", args)
	protoc := exec.Command("protoc", args...)
	protoc.Stdout = os.Stdout
	protoc.Stderr = os.Stderr
	err = protoc.Run()
	if err != nil {
		log.Fatal("Fail on protoc ", err)
	}

	log.Print("Generated protocol")
}

// Generate a go package name for input proto file 'protoPath';
// return the package name relative to the GENERATED_MODULE_NAME
// prefix.
//
// These package names aren't used to identify the gRPC service,
// and they can be pretty much whatever we find convenient. Rather
// than fiddling with trying to deduce path prefixes etc, we'll
// just generate them as "proto_<n>" using a counter.
//
func genProtoPackageName(protoPath string) (string, error) {
	protoCounter = protoCounter + 1
	return fmt.Sprintf("proto%d", protoCounter), nil
}

// Rewrite the .proto file to replace any go_package directive with one based
// on our local package path for generated servers, GENERATED_MODULE_NAME .
// Write the new file to the provided path.
//
func fixGoPackage(protoPath string, newpackage string, outPath string) error {
	in, err := os.Open(protoPath)
	if err != nil {
		return err
	}
	defer in.Close()
	s := bufio.NewScanner(in)
	s.Split(bufio.ScanLines)

	of, err := os.OpenFile(outPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
			return err
	}
	defer of.Close()
	ow := bufio.NewWriter(of)

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
				return fmt.Errorf("Found more than one \"syntax\" statement in \"%s\"", protoPath)
			}
			foundSyntaxLine = true;
			// Immediately after the "syntax" line, add our own option
			// go_package line to override the protocol's real package
			// with one we will generate
			l = l + fmt.Sprintf("\noption go_package = \"%s\";\n", newpackage)
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
		return fmt.Errorf("Failed to munge protocol file %s: no \"syntax\" line found when scanning file", protoPath)
	}

	return nil
}

func fixGoPackages(protoPaths []string, output string) ([]string, error) {
	outProtos := make([]string, len(protoPaths))
	for i, proto := range protoPaths {
		newPackageSuffix, err := genProtoPackageName(proto)
		if err != nil {
			return []string{}, err
		}
		outProtoDir := path.Join(output, newPackageSuffix)
		if err := os.MkdirAll(outProtoDir, os.ModePerm); err != nil {
			return []string{}, err
		}
		outProto := path.Join(outProtoDir, path.Base(proto))
		// Write a copy of the .proto file in outProto with the go_package
		// directive rewritten to point to the full package path, and the file
		// placed in in newPackageSuffix/{filename}.proto
		newPackage := path.Join(GENERATED_MODULE_NAME, newPackageSuffix)
		if err := fixGoPackage(proto, newPackage, outProto); err != nil {
			return []string{}, err
		}
		outProtos[i] = outProto
	}
	return outProtos, nil
}

func runGrpcServer(output string) (*exec.Cmd, <-chan error) {
	run := exec.Command(path.Join(output,"server"))
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	err := run.Start()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("grpc server pid: %d\n", run.Process.Pid)
	runerr := make(chan error)
	go func() {
		runerr <- run.Wait()
	}()
	return run, runerr
}

func buildServer(output string) {
	log.Print("Building server...")
	oldCwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chdir(output); err != nil {
		log.Fatal(err)
	}

	log.Printf("setting module name")
	run := exec.Command("go", "mod", "edit", "-module", GENERATED_MODULE_NAME)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		log.Fatal("go mod edit: ", err)
	}

	log.Printf("go mod tidy")
	run = exec.Command("go", "mod", "tidy")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		log.Fatal("go mod tidy: ", err)
	}

	log.Printf("go build")
	run = exec.Command("go", "build", "-o", "server", ".")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		log.Fatal("go build -o server .: ", err)
	}

	if err := os.Chdir(oldCwd); err != nil {
		log.Fatal(err)
	}
	log.Print("Built ", path.Join(output,"server"))
}

// vim: sw=4 ts=4 noet ai
