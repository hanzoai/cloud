#!/bin/bash
# Hanzo Commerce - Deployment Script
#
# Usage:
#   ./deploy/scripts/deploy.sh [environment] [version]
#
# Environments: local, staging, production
#
# Examples:
#   ./deploy/scripts/deploy.sh local
#   ./deploy/scripts/deploy.sh staging v2.1.0
#   ./deploy/scripts/deploy.sh production v2.1.0 --blue-green

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_DIR="$(dirname "$DEPLOY_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Configuration
ENVIRONMENT="${1:-local}"
VERSION="${2:-latest}"
BLUE_GREEN="${3:-}"

log_info "============================================"
log_info "  Hanzo Commerce Deployment"
log_info "============================================"
log_info "  Environment: ${ENVIRONMENT}"
log_info "  Version:     ${VERSION}"
log_info "  Blue-Green:  ${BLUE_GREEN:-disabled}"
log_info "============================================"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    if ! command -v docker &>/dev/null; then
        log_error "Docker is not installed"
        exit 1
    fi

    if ! command -v docker compose &>/dev/null && ! command -v docker-compose &>/dev/null; then
        log_error "Docker Compose is not installed"
        exit 1
    fi

    # Check .env file
    if [ ! -f "$DEPLOY_DIR/.env" ]; then
        log_warn ".env file not found, copying from .env.example"
        cp "$DEPLOY_DIR/.env.example" "$DEPLOY_DIR/.env"
        log_warn "Please configure $DEPLOY_DIR/.env before deploying to production"
    fi

    log_success "Prerequisites check passed"
}

# Build image
build_image() {
    log_info "Building Commerce image..."

    local build_time=$(date -Iseconds)
    local git_commit=$(git -C "$PROJECT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")

    docker build \
        -f "$DEPLOY_DIR/Dockerfile" \
        -t "hanzoai/commerce:${VERSION}" \
        --build-arg VERSION="$VERSION" \
        --build-arg BUILD_TIME="$build_time" \
        --build-arg GIT_COMMIT="$git_commit" \
        "$PROJECT_DIR"

    if [ "$VERSION" != "latest" ]; then
        docker tag "hanzoai/commerce:${VERSION}" "hanzoai/commerce:latest"
    fi

    log_success "Image built successfully"
}

# Deploy local
deploy_local() {
    log_info "Deploying locally..."

    cd "$DEPLOY_DIR"

    export VERSION
    export COMMERCE_DEV=true
    export ENV=development

    docker compose -f compose.yml down --remove-orphans 2>/dev/null || true
    docker compose -f compose.yml up -d

    log_success "Local deployment complete"
    log_info "Commerce available at: http://localhost:8001"
}

# Deploy staging
deploy_staging() {
    log_info "Deploying to staging..."

    cd "$DEPLOY_DIR"

    export VERSION
    export ENV=staging

    docker compose -f compose.yml -f compose.production.yml down --remove-orphans 2>/dev/null || true
    docker compose -f compose.yml -f compose.production.yml up -d

    log_success "Staging deployment complete"
}

# Deploy production with optional blue-green
deploy_production() {
    log_info "Deploying to production..."

    cd "$DEPLOY_DIR"

    export VERSION
    export ENV=production

    if [ "$BLUE_GREEN" == "--blue-green" ]; then
        log_info "Using blue-green deployment strategy"

        # Determine inactive color
        local active_color
        if [ -f "$DEPLOY_DIR/nginx/conf.d/active.conf" ]; then
            active_color=$(grep -oP 'server commerce-\K(blue|green)' "$DEPLOY_DIR/nginx/conf.d/active.conf" 2>/dev/null | head -1 || echo "blue")
        else
            active_color="blue"
        fi

        local deploy_color
        if [ "$active_color" == "blue" ]; then
            deploy_color="green"
        else
            deploy_color="blue"
        fi

        log_info "Current active: ${active_color}, deploying to: ${deploy_color}"

        export DEPLOY_COLOR="$deploy_color"

        # Deploy to inactive color
        docker compose -f compose.yml -f compose.blue-green.yml up -d --no-deps "commerce-${deploy_color}"

        # Wait for health
        log_info "Waiting for deployment to become healthy..."
        local retries=30
        while [ $retries -gt 0 ]; do
            local health=$(docker inspect --format='{{.State.Health.Status}}' "commerce-${deploy_color}" 2>/dev/null || echo "starting")
            if [ "$health" == "healthy" ]; then
                break
            fi
            sleep 5
            retries=$((retries - 1))
        done

        if [ $retries -eq 0 ]; then
            log_error "Deployment failed health check"
            exit 1
        fi

        # Switch traffic
        "$SCRIPT_DIR/switch-active.sh" "$deploy_color"

        log_success "Blue-green deployment complete"
        log_info "Active deployment: ${deploy_color}"
    else
        # Standard rolling deployment
        docker compose -f compose.yml -f compose.production.yml down --remove-orphans 2>/dev/null || true
        docker compose -f compose.yml -f compose.production.yml up -d

        log_success "Production deployment complete"
    fi
}

# Main
main() {
    check_prerequisites

    case "$ENVIRONMENT" in
        local|dev|development)
            build_image
            deploy_local
            ;;
        staging)
            build_image
            deploy_staging
            ;;
        production|prod)
            build_image
            deploy_production
            ;;
        *)
            log_error "Unknown environment: $ENVIRONMENT"
            echo "Usage: $0 [local|staging|production] [version] [--blue-green]"
            exit 1
            ;;
    esac

    log_info ""
    log_info "Deployment Summary"
    log_info "============================================"
    docker compose -f "$DEPLOY_DIR/compose.yml" ps
}

main
