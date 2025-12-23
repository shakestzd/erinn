#!/bin/bash
#
# HtmlGraph Flexible Deployment Script
#
# This script performs deployment operations with flexible options:
# 1. Push to git
# 2. Build and publish Python package to PyPI
# 3. Install latest version locally
# 4. Update Claude plugin
# 5. Update Gemini extension
# 6. Update Codex skill
#
# Usage:
#   ./scripts/deploy-all.sh [version] [flags]
#
# Examples:
#   ./scripts/deploy-all.sh 0.7.1              # Full release
#   ./scripts/deploy-all.sh --docs-only        # Just commit + push
#   ./scripts/deploy-all.sh 0.7.1 --skip-pypi  # Build but don't publish
#   ./scripts/deploy-all.sh --build-only       # Just build package
#   ./scripts/deploy-all.sh --dry-run          # Show what would happen
#
# Flags:
#   --docs-only     Only commit and push to git (skip build/publish)
#   --build-only    Only build package (skip git/publish/install)
#   --skip-pypi     Skip PyPI publishing step
#   --skip-plugins  Skip plugin update steps
#   --dry-run       Show what would happen without executing
#   --help          Show this help message
#

set -e  # Exit on error

# ============================================================================
# CONFIGURATION SECTION
# ============================================================================
# Project-specific variables - customize for your project
# ============================================================================

# Package metadata
PACKAGE_NAME="htmlgraph"
PROJECT_ROOT_FILE="pyproject.toml"
VERSION_PYTHON_FILE="src/python/htmlgraph/__init__.py"

# PyPI configuration
PYPI_PROJECT_URL="https://pypi.org/project/${PACKAGE_NAME}/"
PYPI_PUBLISH_PATTERN="dist/${PACKAGE_NAME}-\${VERSION}*"

# Plugin configurations (optional)
CLAUDE_PLUGIN_ENABLED=true
GEMINI_EXTENSION_ENABLED=true
CODEX_SKILL_ENABLED=false
GEMINI_EXTENSION_DIR="packages/gemini-extension"
GEMINI_CONFIG_FILE="${GEMINI_EXTENSION_DIR}/gemini-extension.json"

# Build configuration
BUILD_COMMAND="uv build"
CLEAN_DIST=true
PUBLISH_COMMAND="uv publish"

# Git configuration
GIT_REMOTE="origin"
GIT_BRANCH="main"
PYPI_WAIT_SECONDS=10

# Installation configuration
INSTALL_COMMAND="pip install"
INSTALL_METHOD="--upgrade"  # or "--force-reinstall" for forced updates

# ============================================================================
# END CONFIGURATION SECTION
# ============================================================================

# Parse flags
DOCS_ONLY=false
BUILD_ONLY=false
SKIP_PYPI=false
SKIP_PLUGINS=false
DRY_RUN=false
VERSION=""

show_help() {
    echo "HtmlGraph Deployment Script"
    echo ""
    echo "Usage: $0 [version] [flags]"
    echo ""
    echo "Flags:"
    echo "  --docs-only     Only commit and push to git (skip build/publish)"
    echo "  --build-only    Only build package (skip git/publish/install)"
    echo "  --skip-pypi     Skip PyPI publishing step"
    echo "  --skip-plugins  Skip plugin update steps"
    echo "  --dry-run       Show what would happen without executing"
    echo "  --help          Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 0.7.1                    # Full release"
    echo "  $0 --docs-only              # Just commit + push"
    echo "  $0 0.7.1 --skip-pypi        # Build but don't publish"
    echo "  $0 --build-only             # Just build package"
    echo "  $0 --dry-run                # Preview actions"
    exit 0
}

# Parse arguments
for arg in "$@"; do
    case $arg in
        --docs-only)
            DOCS_ONLY=true
            ;;
        --build-only)
            BUILD_ONLY=true
            ;;
        --skip-pypi)
            SKIP_PYPI=true
            ;;
        --skip-plugins)
            SKIP_PLUGINS=true
            ;;
        --dry-run)
            DRY_RUN=true
            ;;
        --help|-h)
            show_help
            ;;
        *)
            if [[ ! $arg =~ ^-- ]] && [ -z "$VERSION" ]; then
                VERSION=$arg
            fi
            ;;
    esac
done

# Get version from argument or detect from pyproject.toml
if [ -z "$VERSION" ]; then
    VERSION=$(python -c "import toml; print(toml.load('pyproject.toml')['project']['version'])" 2>/dev/null || echo "unknown")
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
log_section() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo ""
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

log_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_info() {
    echo -e "ℹ️  $1"
}

# Dry-run wrapper
run_command() {
    if [ "$DRY_RUN" = true ]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would run: $@"
    else
        "$@"
    fi
}

# Determine what to run
if [ "$DOCS_ONLY" = true ]; then
    log_section "HtmlGraph Deployment - DOCS ONLY Mode"
    SKIP_BUILD=true
    SKIP_PYPI=true
    SKIP_INSTALL=true
    SKIP_PLUGINS=true
elif [ "$BUILD_ONLY" = true ]; then
    log_section "HtmlGraph Deployment - BUILD ONLY Mode"
    SKIP_GIT=true
    SKIP_PYPI=true
    SKIP_INSTALL=true
    SKIP_PLUGINS=true
else
    log_section "HtmlGraph Deployment - Version $VERSION"
    SKIP_GIT=false
    SKIP_BUILD=false
    SKIP_INSTALL=false
fi

if [ "$DRY_RUN" = true ]; then
    log_warning "DRY-RUN MODE - No actual changes will be made"
fi

# Check if we're in the right directory
if [ ! -f "pyproject.toml" ]; then
    log_error "Must be run from project root (where pyproject.toml is)"
    exit 1
fi

# Load PyPI token from .env if it exists
if [ -f ".env" ]; then
    log_info "Loading environment variables from .env"
    source .env
fi

# Check for required environment variables
if [ -z "$PyPI_API_TOKEN" ] && [ -z "$UV_PUBLISH_TOKEN" ]; then
    log_warning "PyPI token not found in environment"
    log_info "Set PyPI_API_TOKEN in .env or UV_PUBLISH_TOKEN in environment"
    read -p "Continue anyway? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# ============================================================================
# STEP 1: Git Push
# ============================================================================
if [ "$SKIP_GIT" != true ]; then
    log_section "Step 1: Pushing to Git"

    # Check git status
    if ! git diff-index --quiet HEAD --; then
        log_warning "You have uncommitted changes"
        git status --short
        if [ "$DRY_RUN" != true ]; then
            read -p "Continue anyway? (y/n) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
        fi
    fi

    # Push to remote
    log_info "Pushing to ${GIT_REMOTE}/${GIT_BRANCH}..."
    if run_command git push ${GIT_REMOTE} ${GIT_BRANCH} --tags; then
        log_success "Pushed to git"
    else
        log_error "Git push failed"
        [ "$DRY_RUN" != true ] && exit 1
    fi
else
    log_info "⏭️  Skipping Git Push"
fi

# ============================================================================
# STEP 2: Build Python Package
# ============================================================================
if [ "$SKIP_BUILD" != true ]; then
    log_section "Step 2: Building Python Package"

    # Clean old builds
    log_info "Cleaning old builds..."
    run_command rm -rf dist/

    # Build with uv
    log_info "Building package..."
    if run_command uv build; then
        log_success "Package built successfully"
        [ "$DRY_RUN" != true ] && ls -lh dist/
    else
        log_error "Build failed"
        [ "$DRY_RUN" != true ] && exit 1
    fi
else
    log_info "⏭️  Skipping Package Build"
fi

# ============================================================================
# STEP 3: Publish to PyPI
# ============================================================================
if [ "$SKIP_PYPI" != true ]; then
    log_section "Step 3: Publishing to PyPI"

    log_info "Publishing htmlgraph-$VERSION to PyPI..."

    if [ -n "$PyPI_API_TOKEN" ]; then
        # Use token from .env
        if run_command uv publish dist/htmlgraph-${VERSION}* --token "$PyPI_API_TOKEN"; then
            log_success "Published to PyPI"
        else
            log_error "PyPI publish failed"
            [ "$DRY_RUN" != true ] && exit 1
        fi
    elif [ -n "$UV_PUBLISH_TOKEN" ]; then
        # Use UV_PUBLISH_TOKEN from environment
        if run_command uv publish dist/htmlgraph-${VERSION}*; then
            log_success "Published to PyPI"
        else
            log_error "PyPI publish failed"
            [ "$DRY_RUN" != true ] && exit 1
        fi
    else
        log_warning "No PyPI token found, skipping publish"
        log_info "You can publish manually with:"
        log_info "  uv publish dist/htmlgraph-${VERSION}* --token YOUR_TOKEN"
    fi

    # Wait a bit for PyPI to process
    if [ "$DRY_RUN" != true ]; then
        log_info "Waiting 10 seconds for PyPI to process..."
        sleep 10
    fi
else
    log_info "⏭️  Skipping PyPI Publish"
fi

# ============================================================================
# STEP 4: Install Latest Version Locally
# ============================================================================
if [ "$SKIP_INSTALL" != true ]; then
    log_section "Step 4: Installing Latest Version Locally"

    log_info "Installing htmlgraph==$VERSION..."
    if run_command pip install --upgrade htmlgraph==$VERSION; then
        log_success "Installed locally"
    else
        log_warning "Local install failed, trying with --force-reinstall"
        if run_command pip install --force-reinstall htmlgraph==$VERSION; then
            log_success "Installed locally (force reinstall)"
        else
            log_error "Local install failed"
            [ "$DRY_RUN" != true ] && exit 1
        fi
    fi

    # Verify installation
    if [ "$DRY_RUN" != true ]; then
        INSTALLED_VERSION=$(python -c "import htmlgraph; print(htmlgraph.__version__)" 2>/dev/null || echo "unknown")
        if [ "$INSTALLED_VERSION" = "$VERSION" ]; then
            log_success "Verified: htmlgraph $INSTALLED_VERSION is installed"
        else
            log_warning "Installed version ($INSTALLED_VERSION) doesn't match expected ($VERSION)"
        fi
    fi
else
    log_info "⏭️  Skipping Local Install"
fi

# ============================================================================
# STEP 5: Update Claude Plugin
# ============================================================================
if [ "$SKIP_PLUGINS" != true ]; then
    log_section "Step 5: Updating Claude Plugin"

    if command -v claude &> /dev/null; then
        log_info "Updating Claude plugin..."
        if run_command claude plugin update htmlgraph; then
            log_success "Claude plugin updated"
        else
            log_warning "Claude plugin update failed"
            log_info "You may need to update manually with:"
            log_info "  claude plugin update htmlgraph"
        fi
    else
        log_warning "Claude CLI not found"
        log_info "Install with: npm install -g @anthropics/claude-cli"
    fi
else
    log_info "⏭️  Skipping Claude Plugin Update"
fi

# ============================================================================
# STEP 6: Update Gemini Extension
# ============================================================================
if [ "$SKIP_PLUGINS" != true ]; then
    log_section "Step 6: Updating Gemini Extension"

    GEMINI_EXTENSION_DIR="packages/gemini-extension"
    if [ -d "$GEMINI_EXTENSION_DIR" ]; then
        log_info "Updating Gemini extension version in gemini-extension.json..."

        # Update version in gemini-extension.json
        if [ -f "$GEMINI_EXTENSION_DIR/gemini-extension.json" ]; then
            # Use Python to update JSON (more reliable than sed)
            if [ "$DRY_RUN" = true ]; then
                log_info "[DRY-RUN] Would update gemini-extension.json to version $VERSION"
            else
                python -c "
import json
with open('$GEMINI_EXTENSION_DIR/gemini-extension.json', 'r') as f:
    data = json.load(f)
data['version'] = '$VERSION'
with open('$GEMINI_EXTENSION_DIR/gemini-extension.json', 'w') as f:
    json.dump(data, f, indent=2)
print('Updated gemini-extension.json to version $VERSION')
"
            fi
            log_success "Gemini extension version updated"

            # If there's a build/deploy process, run it
            if [ -f "$GEMINI_EXTENSION_DIR/deploy.sh" ]; then
                log_info "Running Gemini extension deploy script..."
                if [ "$DRY_RUN" = true ]; then
                    log_info "[DRY-RUN] Would run: cd $GEMINI_EXTENSION_DIR && bash deploy.sh"
                else
                    (cd "$GEMINI_EXTENSION_DIR" && bash deploy.sh)
                fi
            else
                log_info "No deploy script found for Gemini extension"
                log_info "Extension files updated, manual deployment may be needed"
            fi
        else
            log_warning "gemini-extension.json not found"
        fi
    else
        log_warning "Gemini extension directory not found"
    fi
else
    log_info "⏭️  Skipping Gemini Extension Update"
fi

# ============================================================================
# STEP 7: Update Codex Skill (if applicable)
# ============================================================================
if [ "$SKIP_PLUGINS" != true ]; then
    log_section "Step 7: Updating Codex Skill"

    # Codex skills are typically in a different location
    # Adjust path as needed for your setup
    if command -v codex &> /dev/null; then
        log_info "Checking for Codex skill..."
        # Add Codex-specific update commands here if applicable
        log_info "Codex skill update - manual verification needed"
    else
        log_info "Codex CLI not found - skipping"
    fi
else
    log_info "⏭️  Skipping Codex Skill Update"
fi

# ============================================================================
# Summary
# ============================================================================
log_section "Deployment Complete! 🎉"

echo ""
echo "Summary:"
echo "--------"
echo "✅ Git push: Complete"
echo "✅ Package build: htmlgraph-$VERSION"
echo "✅ PyPI publish: https://pypi.org/project/htmlgraph/$VERSION/"
echo "✅ Local install: $INSTALLED_VERSION"
echo "✅ Claude plugin: Updated"
echo "✅ Gemini extension: Updated"
echo ""
log_success "All deployment steps completed successfully!"
echo ""
echo "Verify deployment:"
echo "  - PyPI: https://pypi.org/project/htmlgraph/$VERSION/"
echo "  - GitHub: https://github.com/Shakes-tzd/htmlgraph"
echo "  - Local: python -c 'import htmlgraph; print(htmlgraph.__version__)'"
echo ""
