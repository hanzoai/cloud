export GO111MODULE=on

os				 			= $(shell uname | tr '[A-Z]' '[a-z]')
pwd							= $(shell pwd)
current_date		= $(shell date +"%Y-%m-%d")

sdk     				= $(shell dirname $(shell readlink $(shell which gcloud)))
cloud_sdk     	= $(gcloud --format='value(installation.sdk_root)' info)
gopath        	= $(shell go env GOPATH)
goroot        	= $(shell go env GOROOT)

go 							= go
gpm 						= gpm
gover 					= $(gopath)/bin/gover
goveralls     	= $(gopath)/bin/goveralls
ginkgo					= $(gopath)/bin/ginkgo

gae_development = $(pwd)/config/development $(pwd)/api/app.development.yaml
gae_production  = config/production api/app.production.yaml analytics/app.production.yaml
gae_sandbox 	  = config/sandbox api/app.sandbox.yaml analytics/app.sandbox.yaml
gae_staging     = config/staging api/app.staging.yaml analytics/app.staging.yaml

tools = github.com/nsf/gocode \
        github.com/fatih/motion \
        github.com/josharian/impl \
        github.com/jstemmer/gotags \
        github.com/kisielk/errcheck \
        github.com/klauspost/asmfmt/cmd/asmfmt \
        github.com/rogpeppe/godef \
        github.com/zmb3/gogetdoc \
	github.com/onsi/ginkgo/v2/ginkgo \
        golang.org/x/tools/cmd/goimports \
        golang.org/x/tools/cmd/gorename \
        golang.org/x/tools/cmd/guru

# Various patches for SDK
mtime_file_watcher = https://gist.githubusercontent.com/zeekay/5eba991c39426ca42cbb/raw/8db2e910b89e3927adc9b7c183387186facee17b/mtime_file_watcher.py

dev_appserver = python3 /lib/google-cloud-sdk/bin/dev_appserver.py \
					--skip_sdk_update_check \
					--datastore_path=$(pwd)/.datastore.bin \
					--enable_task_running=true \
					--dev_appserver_log_level=info \
					--log_level=info \
					--admin_port=8000 \
					--port=8080

# find command differs between bsd/linux thus the two versions
ifeq ($(os), linux)
	packages = $(shell find . -maxdepth 4 -mindepth 2 -name '*.go' \
			   				  -not -path "./sdk/*" \
			   				  -not -path "./test*" \
			   				  -not -path "./assets/*" \
			   				  -not -path "./replace/*" \
			   				  -not -path "./static/*" \
			   				  -not -path "./node_modules/*" \
			   				  -printf '%h\n' | sort -u | sed -e 's/.\//hanzo.ai\//')
	sed = @sed -i -e
else
	packages = $(shell find . -maxdepth 4 -mindepth 2 -name '*.go' \
			   				  -not -path "./sdk/*" \
			   				  -not -path "./test*" \
			   				  -not -path "./assets/*" \
			   				  -not -path "./replace/*" \
			   				  -not -path "./static/*" \
			   				  -not -path "./node_modules/*" \
			   				  -print0 | xargs -0 -n1 dirname | sort --unique | sed -e 's/.\//hanzo.ai\//')
	sdk_install_extra := $(sdk_install_extra) && \
						 curl $(mtime_file_watcher) > $(pwd)/google/appengine/tools/devappserver2/mtime_file_watcher.py && \
						 pip2 install macfsevents --upgrade
	sed = @sed -i .bak -e
endif

# set v=1 to enable verbose mode
ifeq ($(v), 1)
	test_verbose = --v --progress -- -test.v=true
else
	test_verbose =
endif

project_env = development
project_id  = None

# set production=1 to set datastore export/import target to use production
ifeq ($(production), 1)
	project_env = production
	project_id  = hanzo-production
	gae_config  = $(gae_production)
else ifeq ($(sandbox), 1)
	project_env = sandbox
	project_id  = hanzo-sandbox
	gae_config  = $(gae_sandbox)
else
	project_env = staging
	project_id  = hanzo-staging
	gae_config  = $(gae_staging)
endif

# force a single module to deploy
ifneq ($(strip $(module)),)
	gae_config = $(module)
endif

datastore_admin_url = https://datastore-admin-dot-$(project_id).appspot.com/_ah/remote_api

test_target = -r=true test/*
test_focus := $(focus)
ifdef test_focus
	test_target=$(focus)
endif

test_batch := $(batch)
ifdef test_batch
	test_target=$(batch)
endif

all: deps test install

build: deps update-env
	$(go) build $(packages)

clean:
	$(go) clean -modcache

deps:
	$(go) list ./...
	$(go) get ./...

# INSTALL
install:
	$(go) install $(packages)

# DEV SERVER
serve: update-env
	$(dev_appserver) $(gae_development)

serve-clear-datastore: update-env
	$(dev_appserver) --clear_datastore=true $(gae_development)

serve-public: update-env
	$(dev_appserver) --host=0.0.0.0 $(gae_development)

serve-no-reload: assets update-env
	$(dev_appserver) $(gae_development)

# GOLANG TOOLS
tools:
	$(go) get $(tools)
	$(go) install $(tools)
	$(gopath)/bin/gocode set propose-builtins true
	$(gopath)/bin/gocode set lib-path "$(gopath)/pkg:$(goroot)/pkg"

# TEST/ BENCH
test: update-env-test
	$(ginkgo) $(test_target) --compilers=2 --randomize-all --fail-fast --trace --skip-package=test-integration/* $(test_verbose)

test-watch: update-env-test
	$(ginkgo) watch -r=true --compilers=2 --fail-fast --trace $(test_verbose)

bench: update-env-test
	$(ginkgo) test --randomize-all --fail-fast --trace --skip-package=integration $(test_verbose)

test-ci: update-env-test
	cd $(pwd); $(ginkgo) $(test_target) --randomize-all --randomize-suites --fail-fast --fail-on-pending --trace $(test_verbose)

coverage:
	# $(gover) test/ coverage.out
	# $(goveralls) -coverprofile=coverage.out -service=circle-ci -repotoken=$(COVERALLS_REPO_TOKEN)

# DEPLOY
auth:
	@echo If you have issues authenticating try:
	@echo "   gcloud components reinstall"
	gcloud auth login

deploy: build
	@cd $(gopath)/src/hanzo.ai
	gcloud app deploy $(gae_config) --project $(project_id) --version v1
	gcloud app deploy config/$(project_env)/dispatch.yaml --project $(project_id) --version v1

deploy-dispatch:
	@cd $(gopath)/src/hanzo.ai
	gcloud app deploy config/$(project_env)/dispatch.yaml --project $(project_id) --version v1

update-env:
	@printf 'package config\n\nvar Env = "$(project_env)"' > config/env.go

update-env-test:
	@printf 'package config\n\nvar Env = "test"' > config/env.go

rollback:
	for module in $(gae_config); do \
		$(appcfg.py) rollback $$module; \
	done

# EXPORT / Usage: make datastore-export kind=user namespace=bellabeat
datastore-export:
	@mkdir -p _export/
	$(appcfg.py) download_data \
				 --bandwidth_limit 1000000000 \
				 --rps_limit 10000 \
				 --batch_size 250 \
				 --http_limit 200 \
				 --url $(datastore_admin_url) \
				 --config_file util/bulkloader/bulkloader.yaml \
				 --db_filename /tmp/bulkloader-$$kind.db \
				 --log_file /tmp/bulkloader-$$kind.log \
				 --result_db_filename /tmp/bulkloader-result-$$kind.db \
				 --namespace $$namespace \
				 --kind $$kind \
				 --filename _export/$$namespace-$$kind-$(project_id)-$(current_date).csv
	rm -rf /tmp/bulkloader-$$kind.db \
		   /tmp/bulkloader-$$kind.log \
		   /tmp/bulkloader-result-$$kind.db

# IMPORT / Usage: make datastore-import kind=user file=user.csv
datastore-import:
	@$(appcfg.py) upload_data --bandwidth_limit 1000000000 \
						      --rps_limit 10000 \
						      --batch_size 250 \
						      --http_limit 200 \
						      --url $(datastore_admin_url) \
						      --config_file util/bulkloader/bulkloader.yaml \
				  	          --namespace $$namespace \
						      --kind $$kind \
						      --filename $$file \
						      --log_file /tmp/bulkloader-upload-$$kind.log
	rm -rf /tmp/bulkloader-upload-$$kind.log

# Generate config for use with datastore-export target
datastore-config:
	$(appcfg.py) create_bulkloader_config \
				 --url=$(datastore_admin_url) \
				 --filename=bulkloader.yaml

# Replicate production data to localhost
datastore-replicate:
	$(appcfg.py) download_data --application=s~$(project_id) --url=http://datastore-admin-dot-$(project_id).appspot.com/_ah/remote_api/ --filename=datastore.bin
	$(appcfg.py) --url=http://localhost:8080/_ah/remote_api --filename=datastore.bin upload_data

# Helpers to store and retrieve build artifacts
artifact-download:
	buildkite-agent artifact download sdk-$(BUILDKITE_BRANCH).tar . && tar -xf sdk-$(BUILDKITE_BRANCH).tar || echo no sdk artifact found

artifact-download-prev : build_id = $(shell curl -H "Authorization: Bearer 08a7fd928cc9062dd7522f92f9781fb0d7ea822f" https://api.buildkite.com/v2/organizations/hanzo/pipelines/platform/builds/$$(( $$BUILDKITE_BUILD_NUMBER - 1 )) | jq -r .id) # gitleaks:allow
artifact-download-prev:
	buildkite-agent artifact download sdk-$(BUILDKITE_BRANCH).tar . --build $(build_id) && tar -xf sdk-$(BUILDKITE_BRANCH).tar || echo no sdk artifact found

artifact-upload:
	tar -cf sdk-$(BUILDKITE_BRANCH).tar sdk
	buildkite-agent artifact upload '*.tar'

.PHONY: all auth bench build buildkite-artifact-download \
	buildkite-artifact-upload compile-js compile-js-min compile-css \
	compile-css-min datastore-import datastore-export datastore-config \
	deploy deploy-staging deploy-production deps deps-assets deps-go \
	live-reload serve serve-clear-datastore serve-public test \
	test-integration test-watch tools
.sdk:
	wget https://storage.googleapis.com/appengine-sdks/featured/$(sdk).zip
	unzip $(sdk).zip
	mv go_appengine $(sdk_path)
	rm $(sdk).zip
	$(sdk_install_extra)
