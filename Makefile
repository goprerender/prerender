MAIN_VERSION := $(shell git describe --abbrev=0 --tags 2>/dev/null || echo "0.1.0")
VERSION := ${MAIN_VERSION}\#$(shell git log -n 1 --pretty=format:"%h" 2>/dev/null)
LDFLAGS := -ldflags "-X prerender/cmd/server/Version=${VERSION}"

# Detect OS and set variables
ifeq ($(OS),Windows_NT)
    EXE_EXT := .exe
    RM := del /f /q
    MKDIR := mkdir
    RMDIR := rmdir /s /q
    SHELL := cmd
else
    EXE_EXT :=
    RM := rm -f
    MKDIR := mkdir -p
    RMDIR := rm -rf
endif

# Targets
PRERENDER := prerender$(EXE_EXT)
STORAGE := storage$(EXE_EXT)
WORKER := worker$(EXE_EXT)

.PHONY: default build debug clean test test-verbose test-coverage run

default: run

build:
	go build -o $(PRERENDER) ./cmd/server/main.go
	go build -o $(STORAGE) ./cmd/storage/main.go
	go build -o $(WORKER) ./cmd/worker/main.go

debug:
	go build -gcflags="all=-N -l" -o $(PRERENDER) ./cmd/server/main.go
	go build -gcflags="all=-N -l" -o $(STORAGE) ./cmd/storage/main.go
	go build -gcflags="all=-N -l" -o $(WORKER) ./cmd/worker/main.go

clean:
	$(RM) $(PRERENDER) $(STORAGE) $(WORKER)

test:
	go test -short ./...

test-verbose:
	go test -v ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

run: build
	./$(PRERENDER)

# Cross-compilation targets
build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(PRERENDER) ./cmd/server/main.go
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(STORAGE) ./cmd/storage/main.go
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(WORKER) ./cmd/worker/main.go

build-windows:
	GOOS=windows GOARCH=amd64 go build -o bin/windows/$(PRERENDER) ./cmd/server/main.go
	GOOS=windows GOARCH=amd64 go build -o bin/windows/$(STORAGE) ./cmd/storage/main.go
	GOOS=windows GOARCH=amd64 go build -o bin/windows/$(WORKER) ./cmd/worker/main.go

build-darwin:
	GOOS=darwin GOARCH=amd64 go build -o bin/darwin/$(PRERENDER) ./cmd/server/main.go
	GOOS=darwin GOARCH=amd64 go build -o bin/darwin/$(STORAGE) ./cmd/storage/main.go
	GOOS=darwin GOARCH=amd64 go build -o bin/darwin/$(WORKER) ./cmd/worker/main.go

# Meta targets
all: clean build test

cross-platform: build-linux build-windows build-darwin