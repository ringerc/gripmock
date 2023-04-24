package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"text/template"
	"path"
	_ "embed"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
	"google.golang.org/protobuf/types/descriptorpb"
	"golang.org/x/tools/imports"
)


//go:embed server_template/server.tmpl
var defaultServerTemplate []byte

//go:embed server_template/go_mod.tmpl
var defaultServerGoMod []byte

func main() {
	// Tip of the hat to Tim Coulson
	// https://medium.com/@tim.r.coulson/writing-a-protoc-plugin-with-google-golang-org-protobuf-cd5aa75f5777

	// Protoc passes pluginpb.CodeGeneratorRequest in via stdin
	// marshalled with Protobuf; read and decode it
	input, _ := ioutil.ReadAll(os.Stdin)
	var request pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(input, &request); err != nil {
		log.Fatalf("error unmarshalling CodeGeneratorRequest protobuf request from stdin [%s]: %v", string(input), err)
	}

	// Initialise our plugin with default options
	opts := protogen.Options{}
	plugin, err := opts.New(&request)
	if err != nil {
		log.Fatalf("error initializing plugin: %v", err)
	}

	// We don't do anything special for the "optional" marker, but we have to
	// declare that we support it so that protogen will invoke our plugin.
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)

	protos := make([]*descriptorpb.FileDescriptorProto, len(plugin.Files))
	for index, file := range plugin.Files {
		protos[index] = file.Proto
	}

	params := make(map[string]string)
	for _, param := range strings.Split(request.GetParameter(), ",") {
		split := strings.Split(param, "=")
		params[split[0]] = split[1]
	}

	generateOptions := Options{
		adminPort: params["admin-port"],
		grpcAddr:  fmt.Sprintf("%s:%s", params["grpc-address"], params["grpc-port"]),
		templateDir:  params["template-dir"],
	}
	fw := fileWriter{plugin:plugin}
	err = generateServer(fw, protos, &generateOptions)

	if err != nil {
		log.Fatalf("Failed to generate server: %v", err)
	}

	// Generate a response from our plugin and marshall as protobuf
	out, err := proto.Marshal(plugin.Response())
	if err != nil {
		log.Fatalf("error marshalling plugin response: %v", err)
	}

	// Write the response to stdout, to be picked up by protoc
	os.Stdout.Write(out)
}

type generatorParam struct {
	Services     []Service
	Imports      map[string]string
	GrpcAddr     string
	AdminPort    string
	PbPath       string
}

type Service struct {
	Name    string
	// golang package
	Package string
	// proto file package (api)
	GrpcService string
	Methods []methodTemplate
}

type methodTemplate struct {
	SvcPackage  string
	Name        string
	ServiceName string
	MethodType  string
	Input       string
	Output      string
}

// mock-able adapter for the protobuf plugin file output, so we can easily
// intercept the files and test the generator separately.
type FileWriter interface {
	AddGeneratedFile(filename string, goImportPath protogen.GoImportPath, content []byte) error
}
// Default implementation sends files to protobuf server
type fileWriter struct{
	plugin *protogen.Plugin
}
func (fw fileWriter) AddGeneratedFile(filename string, goImportPath protogen.GoImportPath, content []byte) error {
	of := fw.plugin.NewGeneratedFile(filename, goImportPath)
	if _, err := of.Write(content); err != nil {
		return fmt.Errorf("while writing output %s: %v", filename, err)
	}
	return nil
}

const (
	methodTypeStandard = "standard"
	// server to client stream
	methodTypeServerStream = "server-stream"
	// client to server stream
	methodTypeClientStream  = "client-stream"
	methodTypeBidirectional = "bidirectional"
)

type Options struct {
	grpcAddr  string
	adminPort string
	format    bool
	templateDir  string
}

/*
 * Read a file from the template directory, for when we're using
 * a server template that's not embedded in the binary.
 */
func readTemplateFile(templateDir string, filename string) ([]byte, error) {
	// read the template file from the filesystem
	filePath := path.Join(templateDir, filename)
	log.Printf("Loading template %s...", filePath)
	f, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %v", filePath, err)
	}
	return f, nil
}

/*
 * Read a template file for generating the server sources.
 *
 * The default server source template is embedded into this plugin at build
 * time using go:embed, but it may be overridden by a --template-dir option on
 * the command line options.
 */
func readTemplate(templateDir string, filename string) ([]byte, error) {
	if templateDir == "" {
		switch filename {
		case "server.tmpl":
			return defaultServerTemplate, nil
		case "go_mod.tmpl":
			return defaultServerGoMod, nil
		default:
			return nil, fmt.Errorf("No template file named \"%s\" in compiled-in template", filename)
		}
	} else {
		tmpl, err := readTemplateFile(templateDir, filename)
		if err != nil {
			return nil, err
		}
		return tmpl, err
	}
}

/*
 * Load server.tmpl and other template files, apply template params, and append
 * each file to the output to be sent in the protobuf reply.
 */
func generateServer(fw FileWriter, protos []*descriptorpb.FileDescriptorProto, opt *Options) error {
	services := extractServices(protos)
	imports := resolveImports(protos)

	if opt == nil {
		opt = &Options{}
	}

	templateParams := generatorParam{
		Services:     services,
		Imports:      imports,
		GrpcAddr:     opt.grpcAddr,
		AdminPort:    opt.adminPort,
	}

	if err := generateFile(fw, opt, templateParams, "server.tmpl", "server.go", true); err != nil {
		return err
	}

	if err := generateFile(fw, opt, templateParams, "go_mod.tmpl", "go.mod", false); err != nil {
		return err
	}

	return nil
}

/*
 * Load, template, and write one file from the server template.
 */
func generateFile(fw FileWriter, opt *Options, templateParams generatorParam, templateFileName string, outFileName string, formatGo bool) error {

	templateFile, err := readTemplate(opt.templateDir, templateFileName)
	if err != nil {
		return err
	}

	tmpl := template.New(templateFileName)
	tmpl, err = tmpl.Parse(string(templateFile))
	if err != nil {
		return fmt.Errorf("template parse %v", err)
	}

	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, templateParams)
	if err != nil {
		return fmt.Errorf("template execute %v", err)
	}
	byt := buf.Bytes()

	if formatGo {
		bytProcessed, err := imports.Process("", byt, nil)
		if err != nil {
			return fmt.Errorf("formatting imports: %v \n%s", err, string(byt))
		}
		byt = bytProcessed
	}

	if err := fw.AddGeneratedFile(outFileName, ".", byt); err != nil {
		return err
	}

	return nil
}

/*
 * Find the go packages for the generated golang files relating to each
 * protocol and import them into the generated server. Because gripmock munges
 * the go_package in the proto files, this will find the ones that gripmock's
 * own protogen invocation creates in the same directory as the generated
 * server.
 */
func resolveImports(protos []*descriptorpb.FileDescriptorProto) map[string]string {

	deps := map[string]string{}
	for _, proto := range protos {
		alias, pkg := getGoPackage(proto)

		// fatal if go_package is not present
		if pkg == "" {
			log.Fatalf("option go_package is required. but %s doesn't have any", proto.GetName())
		}

		if _, ok := deps[pkg]; ok {
			continue
		}

		deps[pkg] = alias
	}

	return deps
}

var aliases = map[string]bool{}
var aliasNum = 1
var packages = map[string]string{}

func getGoPackage(proto *descriptorpb.FileDescriptorProto) (alias string, goPackage string) {
	goPackage = proto.GetOptions().GetGoPackage()
	if goPackage == "" {
		return
	}

	// support go_package alias declaration
	// https://github.com/golang/protobuf/issues/139
	if splits := strings.Split(goPackage, ";"); len(splits) > 1 {
		goPackage = splits[0]
		alias = splits[1]
	} else {
		// get the alias based on the latest folder
		splitSlash := strings.Split(goPackage, "/")
		// replace - with _
		alias = strings.ReplaceAll(splitSlash[len(splitSlash)-1], "-", "_")
	}

	// if package already discovered just return
	if al, ok := packages[goPackage]; ok {
		alias = al
		return
	}

	// Aliases can't be keywords
	if isKeyword(alias) {
		alias = fmt.Sprintf("%s_pb", alias)
	}

	// in case of found same alias
	// add numbers on it
	if ok := aliases[alias]; ok {
		alias = fmt.Sprintf("%s%d", alias, aliasNum)
		aliasNum++
	}

	packages[goPackage] = alias
	aliases[alias] = true

	return
}

// change the structure also translate method type
func extractServices(protos []*descriptorpb.FileDescriptorProto) []Service {
	svcTmp := []Service{}
	for _, proto := range protos {
		for _, svc := range proto.GetService() {
			var s Service
			s.Name = svc.GetName()
			s.GrpcService = proto.GetPackage()
			alias, _ := getGoPackage(proto)
			if alias != "" {
				s.Package = alias + "."
			}
			methods := make([]methodTemplate, len(svc.Method))
			for j, method := range svc.Method {
				tipe := methodTypeStandard
				if method.GetServerStreaming() && !method.GetClientStreaming() {
					tipe = methodTypeServerStream
				} else if !method.GetServerStreaming() && method.GetClientStreaming() {
					tipe = methodTypeClientStream
				} else if method.GetServerStreaming() && method.GetClientStreaming() {
					tipe = methodTypeBidirectional
				}

				methods[j] = methodTemplate{
					Name:        strings.Title(*method.Name),
					SvcPackage:  s.Package,
					ServiceName: svc.GetName(),
					Input:       getMessageType(protos, method.GetInputType()),
					Output:      getMessageType(protos, method.GetOutputType()),
					MethodType:  tipe,
				}
			}
			s.Methods = methods
			svcTmp = append(svcTmp, s)
		}
	}
	return svcTmp
}

func getMessageType(protos []*descriptorpb.FileDescriptorProto, tipe string) string {
	split := strings.Split(tipe, ".")[1:]
	targetPackage := strings.Join(split[:len(split)-1], ".")
	targetType := split[len(split)-1]
	for _, proto := range protos {
		if proto.GetPackage() != targetPackage {
			continue
		}

		for _, msg := range proto.GetMessageType() {
			if msg.GetName() == targetType {
				alias, _ := getGoPackage(proto)
				if alias != "" {
					alias += "."
				}
				return fmt.Sprintf("%s%s", alias, msg.GetName())
			}
		}
	}
	return targetType
}

func isKeyword(word string) bool {
	keywords := [...]string{
		"break",
		"case",
		"chan",
		"const",
		"continue",
		"default",
		"defer",
		"else",
		"fallthrough",
		"for",
		"func",
		"go",
		"goto",
		"if",
		"import",
		"interface",
		"map",
		"package",
		"range",
		"return",
		"select",
		"struct",
		"switch",
		"type",
		"var",
	}

	for _, keyword := range keywords {
		if strings.ToLower(word) == keyword {
			return true
		}
	}

	return false
}

// vim: ts=4 sw=4 ai noet
