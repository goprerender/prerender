@echo off
cd /d %~dp0..
protoc -I=. --go_out=./pkg/api/storage --go-grpc_out=./pkg/api/storage api/proto/storage.proto