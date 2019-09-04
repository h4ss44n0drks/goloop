#-------------------------------------------------------------------------------
#
# 	Makefile for building target binaries.
#

# Configuration
BUILD_ROOT = $(abspath ./)
BIN_DIR = ./bin
LINUX_BIN_DIR = ./linux

GOBUILD = go build
GOBUILD_TAGS =
GOBUILD_ENVS = CGO_ENABLED=0
GOBUILD_LDFLAGS =
GOBUILD_FLAGS = -tags "$(GOBUILD_TAGS)" -ldflags "$(GOBUILD_LDFLAGS)"
GOBUILD_ENVS_LINUX = $(GOBUILD_ENVS) GOOS=linux GOARCH=amd64

GOTEST = go test
GOTEST_FLAGS = -test.short

# Build flags
GL_VERSION ?= $(shell git describe --always --tags --dirty)
GL_TAG ?= latest
BUILD_INFO = $(shell go env GOOS)/$(shell go env GOARCH) tags($(GOBUILD_TAGS))-$(shell date '+%Y-%m-%d-%H:%M:%S')

#
# Build scripts for command binaries.
#
CMDS = $(patsubst cmd/%,%,$(wildcard cmd/*))
.PHONY: $(CMDS) $(addsuffix -linux,$(CMDS))
define CMD_template
$(BIN_DIR)/$(1) $(1) : GOBUILD_LDFLAGS+=$$($(1)_LDFLAGS)
$(BIN_DIR)/$(1) $(1) :
	@ \
	rm -f $(BIN_DIR)/$(1) ; \
	echo "[#] go build ./cmd/$(1)"
	$$(GOBUILD_ENVS) \
	go build $$(GOBUILD_FLAGS) \
	    -o $(BIN_DIR)/$(1) ./cmd/$(1)

$(LINUX_BIN_DIR)/$(1) $(1)-linux : GOBUILD_LDFLAGS+=$$($(1)_LDFLAGS)
$(LINUX_BIN_DIR)/$(1) $(1)-linux :
	@ \
	rm -f $(LINUX_BIN_DIR)/$(1) ; \
	echo "[#] go build ./cmd/$(1)"
	$$(GOBUILD_ENVS_LINUX) \
	go build $$(GOBUILD_FLAGS) \
	    -o $(LINUX_BIN_DIR)/$(1) ./cmd/$(1)
endef
$(foreach M,$(CMDS),$(eval $(call CMD_template,$(M))))

# Build flags for each command
gochain_LDFLAGS = -X 'main.version=$(GL_VERSION)' -X 'main.build=$(BUILD_INFO)'
BUILD_TARGETS += gochain
goloop_LDFLAGS = -X 'main.version=$(GL_VERSION)' -X 'main.build=$(BUILD_INFO)'
BUILD_TARGETS += goloop

linux : $(addsuffix -linux,$(BUILD_TARGETS))

DOCKER_IMAGE_TAG ?= latest
GOLOOP_ENV_IMAGE = goloop-env:$(GL_TAG)
GOENV_DOCKER_DIR = $(BUILD_ROOT)/build/goenv

GOCHAIN_IMAGE = gochain:$(GL_TAG)
GOCHAIN_DOCKER_DIR = $(BUILD_ROOT)/build/gochain/

GOLOOP_IMAGE = goloop:$(GL_TAG)
GOLOOP_DOCKER_DIR = $(BUILD_ROOT)/build/goloop

PYDEPS_IMAGE = goloop/py-deps:$(GL_TAG)
PYDEPS_DOCKER_DIR = $(BUILD_ROOT)/build/pydeps

GOLOOP_WORK_DIR = /work

goloop-env-image :
	@ \
	if [ "`docker images -q $(GOLOOP_ENV_IMAGE)`" == "" ] ; then \
	    rm -rf $(GOENV_DOCKER_DIR) ; \
	    mkdir -p $(GOENV_DOCKER_DIR) ; \
	    cp ./go.mod ./go.sum $(GOENV_DOCKER_DIR) ; \
	    cp ./docker/goloop-env/* $(GOENV_DOCKER_DIR) ; \
	    docker build -t $(GOLOOP_ENV_IMAGE) $(GOENV_DOCKER_DIR) ; \
	fi

run-% : goloop-env-image
	@ \
	docker run -it --rm \
	    -v $(BUILD_ROOT):$(GOLOOP_WORK_DIR) \
	    -w $(GOLOOP_WORK_DIR) \
	    $(GOLOOP_ENV_IMAGE) \
	    make "GL_VERSION=$(GL_VERSION)" $(patsubst run-%,%,$@)
pydeps-image: 
	@ \
	if [ "`docker images -q $(PYDEPS_IMAGE)`" == "" ] ; then \
	    rm -rf $(PYDEPS_DOCKER_DIR) ; \
	    mkdir -p $(PYDEPS_DOCKER_DIR) ; \
	    cp $(BUILD_ROOT)/docker/py-deps/* $(PYDEPS_DOCKER_DIR) ; \
	    cp $(BUILD_ROOT)/pyee/requirements.txt $(PYDEPS_DOCKER_DIR) ; \
	    docker build -t $(PYDEPS_IMAGE) $(PYDEPS_DOCKER_DIR) ; \
	fi

pyrun-% : pydeps-image
	@ \
	docker run -it --rm \
	    -v $(BUILD_ROOT):$(GOLOOP_WORK_DIR) \
	    -w $(GOLOOP_WORK_DIR) \
	    $(PYDEPS_IMAGE) \
	    make "GL_VERSION=$(GL_VERSION)" $(patsubst pyrun-%,%,$@)

pyexec:
	@ \
	cd $(BUILD_ROOT)/pyee ; \
	rm -rf build dist ; \
	python3 setup.py bdist_wheel

goloop-image: pyrun-pyexec run-goloop-linux
	@ rm -rf $(GOLOOP_DOCKER_DIR)
	@ mkdir -p $(GOLOOP_DOCKER_DIR)/dist/pyee
	@ mkdir -p $(GOLOOP_DOCKER_DIR)/dist/bin
	@ cp $(BUILD_ROOT)/docker/goloop/* $(GOLOOP_DOCKER_DIR)
	@ cp $(BUILD_ROOT)/pyee/dist/* $(GOLOOP_DOCKER_DIR)/dist/pyee
	@ cp $(BUILD_ROOT)/linux/goloop $(GOLOOP_DOCKER_DIR)/dist/bin
	@ docker build -t $(GOLOOP_IMAGE) \
	    --build-arg TAG_PY_DEPS=$(GL_TAG) \
	    $(GOLOOP_DOCKER_DIR)

gochain-image: pyrun-pyexec run-gochain-linux
	@ rm -rf $(GOCHAIN_DOCKER_DIR)
	@ mkdir -p $(GOCHAIN_DOCKER_DIR)/dist
	@ cp $(BUILD_ROOT)/docker/gochain/* $(GOCHAIN_DOCKER_DIR)
	@ cp $(BUILD_ROOT)/pyee/dist/* $(GOCHAIN_DOCKER_DIR)/dist
	@ cp $(BUILD_ROOT)/linux/gochain $(GOCHAIN_DOCKER_DIR)/dist
	@ docker build -t $(GOCHAIN_IMAGE) \
	    --build-arg TAG_PY_DEPS=$(GL_TAG) \
	    $(GOCHAIN_DOCKER_DIR)

.PHONY: test

test :
	$(GOBUILD_ENVS) $(GOTEST) $(GOBUILD_FLAGS) ./... $(GOTEST_FLAGS)

test% : $(BIN_DIR)/gochain
	@ cd testsuite ; ./gradlew $@

.DEFAULT_GOAL := all
all : $(BUILD_TARGETS)
