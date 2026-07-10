<p align="center"><img src=".github/hero.svg" alt="commerce" width="880"></p>

# commerce

Checkout, billing, pricing, invoicing — the light commerce router for the Hanzo platform. **NOT in PCI-DSS scope** (PAN data lives in `hanzoai/vault`).

[![Status](https://img.shields.io/badge/status-stable-green)]()
[![License](https://img.shields.io/badge/license-MIT-blue)]()

## Quick start

```bash
docker run -p 8001:8001 ghcr.io/hanzoai/commerce:latest
```

## What this is

`commerce` handles orders, products, subscriptions, invoices, and customer records for every Hanzo deployment. It is **CDE-connected, not CDE-in-scope**: card data is tokenized at `hanzoai/vault` and `commerce` only ever sees the token. The service is production multi-tenant (37+ `X-Org-Id` call sites), IAM-integrated, and already exposes `func Mount(*zip.App, cloud.Deps) error` — it is one of the three reference implementations of the HIP-0106 Mount contract.

## Specs

Implements:
- HIP-0037 AI Cloud Platform (billing surface)
- HIP-0106 Unified Cloud Binary (commerce subsystem — already exposes `Mount()`)

## Architecture

```
   client  ->  gateway  ->  commerce (zip.App)
                              |
                     orders / products / subscriptions / invoices
                              |
                  +-----------+-----------+
                  |                       |
              hanzoai/vault          billing provider
              (CDE — tokenizes PAN)   (Stripe etc., webhook in)
                              |
                  per-tenant data via base (HIP-0302)
```


---

# Hanzo [![Build status](https://badge.buildkite.com/d7e68217b7c11a402384e82726433b30b9ebdb54cab934d89c.svg)](https://buildkite.com/hanzo/platform)

Hanzo is a scalable DX platform designed to power next-gen internet companies.

## Development

### Getting started
You can use `make` to setup your development enviroment. Running:

```
$ make deps
```

...will download the Go App Engine SDK and unzip it into `sdk/`. When hacking on
things you'll want to ensure `$GOROOT` and `$GOPATH` point to their respective
directories inside `sdk/`.

You can source the provided `.env` file to set these variables, or use
[`autoenv`](https://github.com/kennethreitz/autoenv) to set them automatically
whenever you `cd` into the project directory.

### Installing Go tools
You can install the common Go command line tools and configure `gocode` to work
with App Engine by running:

```bash
$ make tools
```

### Development server
You can then use `make serve` to run the local development server and `make
test` to run tests.

You can create a local `config.json` file containing configuration variables to
customize settings locally (for instance to disable the auto fixture loading).

This `Makefile` is used to build, test, and deploy a Go application running on Google App Engine, with dependencies managed through a combination of Go tools, Python SDK scripts, and Node.js for asset compilation. Here's a detailed breakdown of how it works and the general flow of operations:

### Key Concepts

- **Google App Engine**: This is the platform used for deploying and managing the application.
- **Go SDK and App Engine SDK**: Go is the language used, and the App Engine SDK is required for running the app locally and deploying it to the cloud.
- **Node.js Assets**: JavaScript and CSS assets are handled using tools like `requisite`, `uglifyjs`, and `stylus`.
- **Staging and Production Environments**: Different configurations are used for deploying to development, staging, and production environments.

### How the Makefile Works

#### 1. **System Variables**

- **`os`**: Detects the operating system (`linux` or `darwin`) and converts it to lowercase for uniformity.
- **`pwd`**: Stores the current directory path.
- **`platform`**: Constructs the platform string based on the OS and architecture.
- **`sdk`**: Defines the specific App Engine SDK version to be used.
- **`sdk_path`, `goroot`, `gopath`**: Defines paths where the Go SDK and Go environment will be installed.

#### 2. **SDK Installation**

The SDK is downloaded if it doesn’t exist. The following steps are performed:

- **`.sdk` target**: Downloads the App Engine SDK as a zip file and unzips it into a `.sdk` folder.
  - It also performs extra setup (e.g., downloading `mtime_file_watcher.py` to monitor file changes).

- **`.sdk/go`**: Creates a wrapper script to use the `goapp` command from the SDK.

- **`.sdk/gpm`**: Downloads `gpm`, a Go dependency manager, to the `.sdk` directory.

#### 3. **Dependency Management**

- **`deps` target**: Ensures all Go and JS/CSS dependencies are installed.
  - **`deps-assets`**: Runs `npm update` to update JS/CSS dependencies.
  - **`deps-go`**: Installs Go dependencies using `gpm` from the `Godeps` file.

#### 4. **Environment Configuration**

- The `gae_development`, `gae_staging`, `gae_production`, and `gae_sandbox` variables hold different configurations (YAML files) for deploying the app to various environments.

- **`project_env`, `project_id`, `gae_config`**: Select the appropriate environment based on Makefile options (`production`, `sandbox`, etc.).

#### 5. **Building the Project**

- **`build` target**: Builds the Go modules listed under `modules` using `goapp` from the App Engine SDK.

- **`install` target**: Installs the Go application and all dependencies.

#### 6. **Asset Compilation**

- **JavaScript**: Files are compiled and minified using `requisite`, `coffee`, and `uglifyjs`.
  - **`compile-js`**: Compiles CoffeeScript files and aggregates JavaScript with `requisite`.
  - **`compile-js-min`**: Minifies the compiled JavaScript files.

- **CSS**: Files are compiled and autoprefixed using `stylus` and `autoprefixer`.
  - **`compile-css`**: Compiles Stylus files into CSS.
  - **`compile-css-min`**: Minifies CSS and applies prefixes for cross-browser compatibility.

#### 7. **Testing and Linting**

- **`test` target**: Runs unit tests using `ginkgo`, with options for verbose and focused tests.

- **`bench` target**: Runs benchmark tests.

- **`test-watch`**: Watches the files and runs the tests on changes.

#### 8. **Running the Development Server**

- **`serve` target**: Starts the App Engine development server using the `dev_appserver.py` command from the SDK.
  - **`serve-clear-datastore`**: Clears the local datastore before starting the server.
  - **`serve-public`**: Starts the server with a public-facing IP (`0.0.0.0`).

#### 9. **Deployment**

- **`deploy` target**: Deploys the app to the selected environment (`staging`, `production`, etc.).
  - **`rollback` target**: Rolls back the deployment if needed.

- **`deploy-app` target**: Updates the environment configuration and deploys each module using `appcfg.py` (App Engine deployment tool).

- **`datastore-export`, `datastore-import`**: Commands for exporting and importing Google Cloud Datastore data.

### Local Setup Steps

1. **Download Dependencies**: Run `make deps` to install all Go, JS, and CSS dependencies.
2. **Build the Project**: Use `make build` to build the Go application.
3. **Run Development Server**: Use `make serve` to start the local App Engine server.

### Staging and Production Setup

1. **Staging**: Run `make deploy` to deploy the app to the staging environment.
   - Make sure the environment variables are set to the correct staging project ID (`project_id`).

2. **Production**: Set the `production=1` flag when calling `make deploy` to deploy to the production environment.

3. **Rolling Back**: Use `make rollback` to revert to a previous version if needed.

### Tools

Various Go tools are included in the `tools` target to assist with development (linting, formatting, etc.). These tools are installed using the `goapp get` and `goapp install` commands.
