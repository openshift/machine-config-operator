#!/usr/bin/env bash

version=6025e8de665b31fa74ab1a66f2cddd8c0abf887e

OLD_GOPATH=$GOPATH
GOPATH="$(mktemp -d)"

mkdir $GOPATH/bin &&\
git clone https://github.com/jteeuwen/go-bindata.git "$GOPATH/src/github.com/jteeuwen/go-bindata" &&\
cd "$GOPATH/src/github.com/jteeuwen/go-bindata" &&\
git checkout -q "$version" &&\
go build -o build-go-bindata ./go-bindata

GOPATH=$OLD_GOPATH
cp build-go-bindata $GOPATH/bin/go-bindata