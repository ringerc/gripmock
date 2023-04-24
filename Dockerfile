FROM golang:1.20.3-alpine3.17

RUN mkdir -p /tmp && \
    chmod 1777 /tmp

RUN --mount=type=cache,target=/var/cache/apk \
  apk -U add git protobuf

# Download protobuf well-known protocol sources. There must be a better way to
# fetch the protobuf well-known types than this, but there's no obvious
# standlone artifact on the protobuf releases page etc, and the proto files are
# jumbled into the main source tree. We need these so that user supplied
# protocols that reference these will work.
RUN --mount=type=cache,target=/protobuf-repo \
  [ -e /protobuf-repo/.git ] || git clone --depth=1 https://github.com/google/protobuf.git /protobuf-repo && \
  cp -r /protobuf-repo/src/ /protobuf
RUN rmdir /protobuf-repo

# Most work should be done as non-root
RUN adduser -D -h /gripmock gripmock

# Use a clean go build where we don't - and can't - write to /go at all;
# also keep all "go build" droppings outside the image
RUN --mount=type=cache,target=/gomodcache-save \
    --mount=type=cache,target=/tmp/gocache \
    chmod o-w $GOPATH && \
    chown -R gripmock /gomodcache-save && \
    chown -R gripmock /tmp/gocache
ENV GOBIN=/gripmock/bin \
    GOCACHE=/tmp/gocache \
    GOMODCACHE=/gomodcache-save \
    PATH=/gripmock/bin:$PATH \
    GOPATH=

# gripmock currently expects workdirs called /protogen and /generated to be
# writeable. This should be tidied up in future.
RUN mkdir /protogen /generated && \
    chown gripmock /protogen /generated

USER gripmock
RUN mkdir /gripmock/bin

# Install protogen to $GOBIN
# A different go mod cache is used
RUN --mount=type=cache,target=/gomodcache-save \
    --mount=type=cache,target=/tmp/gocache \
  go install -v google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
  go install -v google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Install gripmock to $GOBIN
RUN mkdir -p /gripmock
ADD gripmock /gripmock/gripmock
WORKDIR /gripmock/gripmock
RUN --mount=type=cache,target=/gomodcache-save \
    --mount=type=cache,target=/tmp/gocache \
    GO111MODULE=on go install -v \
    && rm -rf vendor >&/dev/null

ADD protoc-gen-gripmock /gripmock/protoc-gen-gripmock
WORKDIR /gripmock/protoc-gen-gripmock
RUN --mount=type=cache,target=/gomodcache-save \
    --mount=type=cache,target=/tmp/gocache \
    GO111MODULE=on go install -v \
    && rm -rf vendor &>/dev/null

# Pre-download modules used for server builds. This relies on the default
# go_mod.tmpl not really using any templating directives; at some point this
# should probably instead invoke gripmock with an option to generate the server
# template and exit before building it, so we can then "go mod download" it.
WORKDIR /tmp/server_download
RUN --mount=type=cache,target=/gomodcache-save \
    cp /gripmock/protoc-gen-gripmock/server_template/go_mod.tmpl go.mod && \
    go mod download && \
    go mod download cloud.google.com/go@v0.105.0

# Save the go module cache we've populated in the container image so it can be
# used for mock server builds without having to download all the modules again.
USER root
RUN --mount=type=cache,target=/gomodcache-save \
    rm -rf /gomodcache && \
    mkdir -p /gomodcache && \
    cp -r /gomodcache-save/* /gomodcache/ && \
    chown -R gripmock /gomodcache
RUN rmdir /gomodcache-save
ENV GOMODCACHE=/gomodcache
USER gripmock

# While it'd be preferable to use a non-root workdir, existing scripts tend to
# assume they can mount proto files off / and we'll generate paths under
# /protogen for them, so use / as workdir until a workaround for that is
# available.
WORKDIR /

ENV GOCACHE=

EXPOSE 4770 4771

ENTRYPOINT ["gripmock","-imports","/protobuf","-o","/generated"]
