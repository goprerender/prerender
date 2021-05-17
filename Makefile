MAIN_VERSION:=$(shell git describe --abbrev=0 --tags || echo "0.1.0")
VERSION:=${MAIN_VERSION}\#$(shell git log -n 1 --pretty=format:"%h")

LDFLAGS:=-ldflags "-X prerender/cmd/server/Version=${VERSION}"

define \n


endef


default: run


build:
	go build -o prerender ./cmd/server/main.go
	go build -o storage ./cmd/storage/main.go
#	go build ${LDFLAGS} -o prerender ./cmd/server/main.go
#	go build ${LDFLAGS} -o storage ./cmd/storage/main.go
clean:
	del prerender
	del storage
test:
	for /f "" %G in ('go list ./... ^| find /i /v "/vendor/"') do @go test %G
