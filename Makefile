BINARY = meg_sender
GITHUB_PROJECT_PATH = meg-sender
VET_REPORT = vet.report
TEST_REPORT = tests.xml
GOARCH = amd64

VERSION=1.0.0.0
COMMIT=$(shell git rev-parse HEAD)
BRANCH=$(shell git rev-parse --abbrev-ref HEAD)

# Symlink into GOPATH
GITHUB_USERNAME=alex19861108
BUILD_DIR=${GOPATH}/src/github.com/${GITHUB_USERNAME}/${GITHUB_PROJECT_PATH}
CURRENT_DIR=$(shell pwd)
BUILD_OUTPUT_DIR=${CURRENT_DIR}/output

# Setup the -ldflags option for go build here, interpolate the variable values
LDFLAGS = -ldflags "-X main.VERSION=${VERSION} -X main.COMMIT=${COMMIT} -X main.BRANCH=${BRANCH}"

# Build the project
all: clean makedir test vet linux darwin windows

makedir:
	cd ${BUILD_DIR}; \
	mkdir -p ${BUILD_OUTPUT_DIR}/bin ; \
	cd - >/dev/null

linux:
	cd ${BUILD_DIR}; \
	GOOS=linux GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BUILD_OUTPUT_DIR}/bin/${BINARY}-linux-${GOARCH} . ; \
	cd - >/dev/null

darwin:
	cd ${BUILD_DIR}; \
	GOOS=darwin GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BUILD_OUTPUT_DIR}/bin/${BINARY}-darwin-${GOARCH} . ; \
	cd - >/dev/null

windows:
	cd ${BUILD_DIR}; \
	GOOS=windows GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BUILD_OUTPUT_DIR}/bin/${BINARY}-windows-${GOARCH}.exe . ; \
	cd - >/dev/null

test:
	if ! hash go2xunit 2>/dev/null; then go install github.com/tebeka/go2xunit; fi
	cd ${BUILD_DIR}; \
	godep go test -v ./... 2>&1 | go2xunit -output ${BUILD_OUTPUT_DIR}/${TEST_REPORT} ; \
	cd - >/dev/null

vet:
	-cd ${BUILD_DIR}; \
	godep go vet ./... > ${BUILD_OUTPUT_DIR}/${VET_REPORT} 2>&1 ; \
	cd - >/dev/null

fmt:
	cd ${BUILD_DIR}; \
	go fmt $$(go list ./... | grep -v /vendor/) ; \
	cd - >/dev/null

clean:
	if [ -e "${BUILD_OUTPUT_DIR}" ]; then \
		rm -rf ${BUILD_OUTPUT_DIR}; \
	fi

.PHONY: linux darwin windows makedir test vet fmt clean
