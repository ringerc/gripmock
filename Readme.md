# GripMock
GripMock is a **mock server** for **GRPC** services. It's using a `.proto` file to generate implementation of gRPC service for you.
You can use gripmock for setting up end-to-end testing or as a dummy server in a software development phase.
The server implementation is in GoLang but the client can be any programming language that support gRPC.

---

**This is a fork of the
[original Gripmock upstream](https://github.com/tokopedia/gripmock/)**
with the following changes:

Features:

* Support proto3 "optional"

* Support overriding the default server template using a `-template-dir` option
  to provide an alternate directory for the `server.tmpl` and `go_mod.tmpl`
  files.

* Support for building using locally pre-generated implementations of dependency
  protocols. The new `-go-replace` flag takes list of `from=to` module
  replacements in the same form as `go mod edit -replace` command.

* Read OpenTelemetry trace metadata in W3C trace context or Zipkin b3 (single
  or multi) formats and emit trace spans to a trace collector if appropriate env-vars
  are set;
  See [Tracing requests and responses with OpenTelemetry](tracing-requests-and-responses-with-opentelemetry)

* Stubs print the services and methods they expose when they start up. This
  should probably be made optional via a log-level control at some point.

* Option for verbose logging

Deprecated code replacement:

* Replace use of [`markbates/pkger`](https://github.com/markbates/pkger)
  with native `go:embed` use.

* Replace deprecated `https://github.com/google/protobuf` with
  `google.golang.org/protobuf` and adapt plugin interface accordingly.

* Update to go 1.19

* Pass plugin options as separate generator options, not embedded into input
  file name as comma separated files

Cleanups:

* Remove use of a shell script for munging the protocol files. The `gripmock`
  binary does the required proto source transformations directly, and
  `fix_gopackage.sh` has been deleted.

* Document the protocol path discovery logic and add checks to ensure
  the files really exist.

* Generate a `go.mod` along with the server stub `server.go`, and build the
  server with `go build` instead of using a direct `go run`. This makes it
  easier to ensure that expected modules and versions are present, override
  modules, etc.

* Use a module name for generated code that is not under the gripmock github
  repo. This ensures that the build will use generated protocols. If they're
  not found locally then attempts to download them from the Internet will fail
  visibly.

* Cache build dependencies for the generated servers in the `Dockerfile`
  so that most runs of the container image don't need to make any network
  requests.

* Move gripmock into a subdir so the components don't share a module

* Do not write inside `$GOPATH`; build clean and self contained server stubs
  where the `/go` path is read-only

* Only copy needed files into the docker image; omit tempfiles, go build caches
  etc from the final image

* Use Docker Buildkit cache for image builds when available so that repeat
  builds on one machine are faster and don't repeat downloads even if the
  dockerfile layer cache is invalidated

---

## Quick Usage

First, prepare your `.proto` file. Or you can use `hello.proto` in
`example/simple/` folder.

It is recommended that you use the gripmock docker container image, as it's
significantly easier to run. This fork doesn't currently publish a pre-built
image, but it's trivial to build one:

- Install [Docker](https://docs.docker.com/install/)

- Clone this repo: 

      git clone github.com/ringerc/gripmock

- Build the image:

      cd gripmock && docker buildx build -t gripmock .

- Run the `gripmock` image with your protocol files bind-mounted into the
  container by fully qualified path, and with ports exposed for access. We'll
  use the `example/simple.proto` protocol from the gripmock repo, specified by
  full path:

      docker run \
        -p 4770:4770 -p 4771:4771 \
        -v ${PWD}/example/simple:/proto \
        gripmock \
        /proto/simple.proto

- On a separate terminal add a stub into the stub service. Run:

      curl -X POST -d '{
              "service":"Gripmock",
        "method":"SayHello",
        "input":{
          "equals":{"name":"gripmock"}
        },
        "output":{
          "data":{"message":"Hello GripMock"}
        }
      }' \
      localhost:4771/add

  You'll usually want to use stub files instead, but this is handy for a demo.

- Now we are ready to test it. The simplest way is to install
  [grpcurl](https://github.com/fullstorydev/grpcurl) then call the endpoint
  with:

      grpcurl -plaintext -format json -d '{"name":"gripmock"}' \
        localhost:4770 simple.Gripmock/SayHello

Check [`example`](https://github.com/ringerc/gripmock/tree/master/example)
folder for various usecase of gripmock (all from the original project) and
some example clients.

---

## How It Works
![Running Gripmock](/assets/images/gripmock_readme-running%20system.png)

From client perspective, GripMock has 2 main components:
1. GRPC server that serves on `tcp://localhost:4770`. Its main job is to serve incoming rpc call from client and then parse the input so that it can be posted to Stub service to find the perfect stub match.
2. Stub server that serves on `http://localhost:4771`. Its main job is to store all the stub mapping. We can add a new stub or list existing stub using http request.

Matched stub will be returned to GRPC service then further parse it to response the rpc call.


From technical perspective, GripMock consists of 2 binaries. 
The first binary is the gripmock itself, when it will generate the gRPC server using the plugin installed in the system (see [Dockerfile](Dockerfile)). 
When the server sucessfully generated, it will be invoked in parallel with stub server which ends up opening 2 ports for client to use.

The second binary is the protoc plugin which located in folder [protoc-gen-gripmock](/protoc-gen-gripmock). This plugin is the one who translates protobuf declaration into a gRPC server in Go programming language. 

![Inside GripMock](/assets/images/gripmock_readme-inside.png)

## Local install

To install and run `gripmock` locally, without using the container image, you will require `protoc-gen-go` and `protoc-gen-go-grpc`:

```bash
go install -v google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
go install -v google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

Then in the `gripmock` dir, install both `gripmock` and its protogen plugin:

```bash
(cd gripmock && go install .)
(cd protoc-gen-gripmock && go install .)
```

## Protocol paths

`gripmock` itself does not check and resolve imports between protocol files. It
just preserves the protocol paths of each input protocol relative to the
matching import path directory when it generates its own modified protocol
files. So if you get your import paths wrong, `protoc` will complain about
resolving imports relative to the gripmock output directory.

Using [`example/multi-package`](./example/multi-package)
from the gripmock source as an example:

        example/multi-package
                    ├── bar
                    │   └── bar.proto
                    ├── client
                    ├── foo.proto
                    ├── hello.proto
                    └── stub

You will note that [`hello.proto`](example/multi-package/hello.proto)'s `import` directives are
relative to the `example/multi-package` directory:

    import "bar/bar.proto";
    import "foo.proto";

therefore we must specify the `example/multi-package` directory on the gripmock
import path.

If we tried to instead supply the `example` directory on the import path,
`protoc` would be unable to resolve `foo.proto` as `example/foo.proto` and
would fail.

The following would all be correct container invocations, where
`example/multi-package` is bound to `/src` within the container:

    # Use absolute paths within container, import path is prefix of proto
    # paths
    docker run -v ${PWD}/example/multi-package:/src gripmock \
        -import /protobuf,/src \
        /src/foo.proto /src/hello.proto /src/bar/bar.proto

    # Proto paths are relative to absolute import path
    docker run -v ${PWD}/example/multi-package:/src gripmock \
        -import /protobuf,/src \
        foo.proto hello.proto bar/bar.proto

If you want one `gripmock` to serve many protocols, just list each protocol
directory as an import. Then specify the protocol files relative to the
protocol directories.

### Gripmock protocol path resolution

If you see an error like

    Protocol file not found on any import path, see README for details

then gripmock was unable to find a containing import directory for one or more
input `.proto` files on the command line positional arguments list.

Each input `.proto` file MUST be contained within one of the directories on the
`-imports` argument list. Gripmock searches for protocols within import
directories using the following algorithm, which resembles that used by
`protoc` itself:

* If the `.proto` path is relative; then
  * For each import directory:
    * Construct a fully qualified proto path relative to the import dir; and
    * Test whether the file at `{importdir}/{proto_arg}` exists
* If the `.proto` path is absolute; then
  * For each import directory:
    * Convert relative import dir paths to an absolute path resolved against
      the current working directory; then
    * Normalize proto path and import path; then
    * Check whether the import path is a lexical prefix of the import path,
      and if so, return the proto path with the import path prefix removed
* If the protocol file is not found in any import path, test whether the
  protocol path exists (either relative to the gripmock working
  directory or as an absolute path). If so, use the directory that contains
  the input `.proto` file as an implicit path entry. The message
  `WARNING: adding proto file's containing dir as implicit import path` will
  be emitted in this case. You should use explicit import paths instead,
  as this will not work correctly if the protocol file imports other protos
  via relative paths.

Absolute path matching is case sensitive even if the file system is
case-insensitive. Gripmock will not resolve symbolic links when resolving
absolute protocol paths.

Mixing absolute and relative paths is supported, but not recommended.

Run `gripmock` with `-verbose 4` to see details on the protocol path search
process.

### Protocol path shadowed

An error like

    Input is shadowed in the --proto_path

from `protoc` itself indicates a bug in gripmock's protocol path resolution.
Please raise an issue *with a fully self contained reproducible test case*
illustrating the problem.

### Resolving implementations for dependency protocols

Gripmock will *not* generate golang protobuf files for protocols not explicitly
listed on the gripmock command line.

There are three ways to provide the required implementations:

* Pass the dependency `.proto` files as arguments to `gripmock`, so it will
  automatically generate the go files for them;
* Let the `go mod tidy` command run by `gripmock` find and download them via
  the same go module resolution as usual. This may require setting a
  `GOPRIVATE` env-var in container runs, and possibly injecting repo access
  credentials or API tokens into the container.
* Pass a `-go-replace` option to `gripmock` that maps the module paths for
  the dependencies to locally pre-generated files, usually bind-mounted into
  a container.

For example, say some protocol file `protos/src/a/a.proto` has:

```
option go_module = github.com/mycompany/protos/generated/v1/a/a.proto
import "b/b.proto"
```

Lets assume that `protos` has subdir `src` containing the proto files, and
`generated` containing golang modules for each proto.

Running `gripmock -imports ./protos/src a/a.proto` will allow `gripmock` to
find the required `b/b.proto` protobuf file at `protos/src/b/b.proto` but it
won't know how to get the generated golang files for the implementation so the
server will fail with an error like

    gripmock/generated/a imports
        github.com/mycompany/protos/generated: cannot find module providing package github.com/mycompany/protos/generated/v1/b [...]

You could solve this by:

* Giving the `go` runtime wherever `gripmock` is running access to
  `github.com/mycompany/protos/generated/v1/b`, so it can download
  pre-generated files from the Internet;
* Explicitly listing `b/b.proto` on the gripmock argument list; or
* Adding
  `-replace=github.com/mycompany/protos/generated=${PWD}/protos/generated` to
  the gripmock CLI, so module resolution for paths is re-mapped to
  pre-generated local proto implementations.

## Stubbing

Stubbing is the essential mocking of GripMock. It will match and return the expected result into GRPC service. This is where you put all your request expectation and response

### Dynamic stubbing
You could add stubbing on the fly with a simple REST API. HTTP stub server is running on port `:4771`

- `GET /` Will list all stubs mapping.
- `POST /add` Will add stub with provided stub data
- `POST /find` Find matching stub with provided input. see [Input Matching](#input_matching) below.
- `GET /clear` Clear stub mappings.

Stub Format is JSON text format. It has a skeleton as follows:
```
{
  "service":"<servicename>", // name of service defined in proto
  "method":"<methodname>", // name of method that we want to mock
  "input":{ // input matching rule. see Input Matching Rule section below
    // put rule here
  },
  "output":{ // output json if input were matched
    "data":{
      // put result fields here
    },
    "error":"<error message>" // Optional. if you want to return error instead.
  }
}
```

For our `hello` service example we put a stub with the text below:
```
  {
    "service":"Greeter",
    "method":"SayHello",
    "input":{
      "equals":{
        "name":"gripmock"
      }
    },
    "output":{
      "data":{
        "message":"Hello GripMock"
      }
    }
  }
```

### Static stubbing
You could initialize gripmock with stub json files and provide the path using `--stub` argument. For example you may
mount your stub file in `/mystubs` folder then mount it to docker like

`docker run -p 4770:4770 -p 4771:4771 -v /mypath:/proto -v /mystubs:/stub tkpd/gripmock --stub=/stub /proto/hello.proto`

Please note that Gripmock still serves http stubbing to modify stored stubs on the fly.

## <a name="input_matching"></a>Input Matching
Stub will respond with the expected response only if the request matches any rule. Stub service will serve `/find` endpoint with format:
```
{
  "service":"<service name>",
  "method":"<method name>",
  "data":{
    // input that suppose to match with stored stubs
  }
}
```
So if you do a `curl -X POST -d '{"service":"Greeter","method":"SayHello","data":{"name":"gripmock"}}' localhost:4771/find` stub service will find a match from listed stubs stored there.

### Input Matching Rule
Input matching has 3 rules to match an input: **equals**,**contains** and **regex**
<br>
Nested fields are allowed for input matching too for all JSON data types. (`string`, `bool`, `array`, etc.)
<br>
**Gripmock** recursively goes over the fields and tries to match with given input.
<br>
**equals** will match the exact field name and value of input into expected stub. example stub JSON:
```
{
  .
  .
  "input":{
    "equals":{
      "name":"gripmock",
      "greetings": {
            "english": "Hello World!",
            "indonesian": "Halo Dunia!",
            "turkish": "Merhaba Dünya!"
      },
      "ok": true,
      "numbers": [4, 8, 15, 16, 23, 42]
      "null": null
    }
  }
  .
  .
}
```

**contains** will match input that has the value declared expected fields. example stub JSON:
```
{
  .
  .
  "input":{
    "contains":{
      "field2":"hello",
      "field4":{
        "field5": "value5"
      } 
    }
  }
  .
  .
}
```

**matches** using regex for matching fields expectation. example:

```
{
  .
  .
  "input":{
    "matches":{
      "name":"^grip.*$",
      "cities": ["Jakarta", "Istanbul", ".*grad$"]
    }
  }
  .
  .
}
```

## Discovering methods

The server stubs print the methods they expose on startup, but the gripmock
server exposes a reflection interface so you can also use
[grpcurl](https://github.com/fullstorydev/grpcurl) to list them with e.g.:

    grpcurl -plaintext localhost:5880 list

## Tracing requests and responses with OpenTelemetry

Gripmock generates an OpenTelemetry-enabled gRPC server that will send trace
events to the OpenTelemetry collector, a zipkin server, or write them as json
to stdout.

Simple example:

    OTEL_TRACES_EXPORTER=stdout gripmock

### Available trace exporters

To enable tracing, set the env-var `OTEL_TRACES_EXPORTER` to a comma-separated list
of one or more of `otlp`, `zipkin` and `stdout`

#### OpenTelemetry exporter

`OTEL_TRACES_EXPORTER=otlp` will send traces to an [OpenTelemetry collector endpoint](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/protocol/exporter.md#configuration-options) at `OTEL_EXPORTER_OTLP_ENDPOINT` in format `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`.

For a HTTP exporter endpoint:

    OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf" OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 gripmock ...

For a gRPC exporter endpoint:

    OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL="grpc" OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 gripmock ...

Optionally `OTEL_EXPORTER_OTLP_INSECURE=true` may be set to disable the use of
TLS for trace event delivery. See [the spec](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/protocol/exporter.md) for certificate configuration and other options.

#### Zipkin exporter

`OTEL_TRACES_EXPORTER=zipkin` will send traces to Zipkin-compatible server at [`OTEL_EXPORTER_ZIPKIN_ENDPOINT`](https://opentelemetry.io/docs/reference/specification/sdk-environment-variables/#zipkin-exporter), e.g.:

    OTEL_TRACES_EXPORTER=zipkin OTEL_EXPORTER_ZIPKIN_ENDPOINT=http://localhost:9411/api/v2/spans gripmock ...

#### stdout exporter

`OTEL_TRACES_EXPORTER=stdout` will write json trace events to stdout.

Unlike the other exporters, the stdout writer is configured to be *synchonous*,
so it has a greater performance impact. It's intended for debug use only. Use a
server based collector if performance is a concern.

### Trace context propagation

Gripmock recognises inbound trace contexts in w3c format and baggage format by
default. To use support zipkin B3 headers or other propagators, run gripmock
with the environment variable `OTEL_PROPAGATORS` set to
[any supported set of propagators](https://pkg.go.dev/go.opentelemetry.io/contrib/propagators/autoprop)
e.g.

    OTEL_PROPAGATORS=tracecontext,baggage,b3,b3multi gripmock ....

### Tracing demo

To test the tracing, you can set appropriate trace headers in 
[`grpcurl`](https://github.com/fullstorydev/grpcurl) requests. For example, in
one session run `gripmock` to serve `example/simple/simple.proto`;
set the env-vars `OTEL_PROPAGATORS=tracecontext,baggage,b3,b3multi`
and `OTEL_TRACES_EXPORTER=stdout`. Then in another session run:

    curl -X POST -d '{"service":"Gripmock","method":"SayHello","input":{"equals":{}},"output":{"data":{"message":"Hello GripMock"}}}' localhost:4771/add

   grpcurl -H 'b3: 80f198ee56343ba864fe8b2a57d3eff7-e457b5a2e4d86bd1-1' -plaintext localhost:4770 simple.Gripmock/SayHello
