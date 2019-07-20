#!/usr/bin/env bash

tmp=$(mktemp -d)
version=ee3c2418e3682cc4a4e6c5dd1b32d0b98f7e2c55

cd $tmp &&\
git clone https://github.com/go-bindata/go-bindata.git &&\
cd $tmp/go-bindata &&\
git checkout -q $version &&\
go build -o build-go-bindata ./go-bindata &&\
cp build-go-bindata $GOPATH/bin/go-bindata
