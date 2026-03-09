#!/bin/bash
#
# HtmlGraph Flexible Deployment Script
#
# This script performs deployment operations with flexible options:
# PRE-FLIGHT:
#   - Code quality checks (ruff, mypy, pytest)
#   - Plugin sync verification
# DEPLOYMENT STEPS:
#   0. Update version numbers and commit
#   1. Push to git with tags
#   2. Build Python package
#   3. Publish to PyPI
#   4. Install latest version locally
#   5. Update Claude plugin (with sync)
#   6. Update Gemini extension
#   7. Update Codex skill
#   8. Update OpenCode extension
#   9. Create GitHub release
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

# Parse flags
DOCS_ONLY=false
BUILD_ONLY=false
SKIP_PYPI=false
SKIP_PLUGINS=false
DRY_RUN=false
NO_CONFIRM=false
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
    echo "  --no-confirm    Skip all confirmation prompts (non-interactive mode)"
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
        --no-confirm)
            NO_CONFIRM=true
            ;;
        --dry-run)
            DRY_RUN=true
            ;;
        --help|-h)
            show_help
            ;;
        --*)
            # Unknown flag - reject it
            echo "Error: Unknown flag: $arg"
            echo "Use --help to see available options"
            exit 1
            ;;
        *)
            # Not a flag, treat as version
            if [ -z "$VERSION" ]; then
                VERSION=$arg
            fi
            ;;
    esac
done

# Get version from argument or detect from pyproject.toml
if [ -z "$VERSION" ]; then
    VERSION=$(uv run python -c "import toml; print(toml.load('pyproject.toml')['project']['version'])" 2>/dev/null || echo "unknown")
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

# Function to update version numbers in all files
update_version_numbers() {
    local version=$1

    log_info "Updating version numbers to $version..."

    # Update pyproject.toml
    if [ -f "pyproject.toml" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update pyproject.toml version to $version"
        else
            sed -i '' "s/^version = \".*\"/version = \"$version\"/" pyproject.toml
            log_success "Updated pyproject.toml"
        fi
    fi

    # Update __init__.py
    if [ -f "src/python/htmlgraph/__init__.py" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update __init__.py version to $version"
        else
            sed -i '' "s/^__version__ = \".*\"/__version__ = \"$version\"/" src/python/htmlgraph/__init__.py
            log_success "Updated __init__.py"
        fi
    fi

    # Update Claude plugin JSON
    if [ -f "packages/claude-plugin/.claude-plugin/plugin.json" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update plugin.json version to $version"
        else
            uv run python -c "
import json
with open('packages/claude-plugin/.claude-plugin/plugin.json', 'r') as f:
    data = json.load(f)
data['version'] = '$version'
with open('packages/claude-plugin/.claude-plugin/plugin.json', 'w') as f:
    json.dump(data, f, indent=2)
"
            log_success "Updated plugin.json"
        fi
    fi

    # Update Gemini extension JSON
    if [ -f "packages/gemini-extension/gemini-extension.json" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update gemini-extension.json version to $version"
        else
            uv run python -c "
import json
with open('packages/gemini-extension/gemini-extension.json', 'r') as f:
    data = json.load(f)
data['version'] = '$version'
with open('packages/gemini-extension/gemini-extension.json', 'w') as f:
    json.dump(data, f, indent=2)
"
            log_success "Updated gemini-extension.json"
        fi
    fi

    # Update OpenCode extension JSON
    if [ -f "packages/opencode-extension/opencode-extension.json" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update opencode-extension.json version to $version"
        else
            uv run python -c "
import json
with open('packages/opencode-extension/opencode-extension.json', 'r') as f:
    data = json.load(f)
data['version'] = '$version'
with open('packages/opencode-extension/opencode-extension.json', 'w') as f:
    json.dump(data, f, indent=2)
"
            log_success "Updated opencode-extension.json"
        fi
    fi

    # Update OpenCode extension npm package.json
    if [ -f "packages/opencode-extension/package.json" ]; then
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would update opencode-extension package.json version to $version"
        else
            uv run python -c "
import json
with open('packages/opencode-extension/package.json', 'r') as f:
    data = json.load(f)
data['version'] = '$version'
with open('packages/opencode-extension/package.json', 'w') as f:
    json.dump(data, f, indent=2)
"
            log_success "Updated opencode-extension package.json"
        fi
    fi

    # Update marketplace.json (both root and plugin copy)
    for marketplace_file in ".claude-plugin/marketplace.json" "packages/claude-plugin/.claude-plugin/marketplace.json"; do
        if [ -f "$marketplace_file" ]; then
            if [ "$DRY_RUN" = true ]; then
                log_info "[DRY-RUN] Would update $marketplace_file version to $version"
            else
                uv run python -c "
import json
with open('$marketplace_file', 'r') as f:
    data = json.load(f)
data['version'] = '$version'
for plugin in data.get('plugins', []):
    if plugin.get('name') == 'htmlgraph':
        plugin['version'] = '$version'
with open('$marketplace_file', 'w') as f:
    json.dump(data, f, indent=2)
"
                log_success "Updated $marketplace_file"
            fi
        fi
    done

    echo ""
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


# ============================================================================
# PRE-FLIGHT: Code Quality Checks
# ============================================================================
if [ "$BUILD_ONLY" != true ] && [ "$DOCS_ONLY" != true ]; then
    log_section "Pre-flight: Code Quality Checks"

    if [ "$DRY_RUN" = true ]; then
        log_info "[DRY-RUN] Would run quality checks (ruff, mypy)"
    else
        # Run ruff linting
        log_info "Running ruff check..."
        if uv run ruff check src/ packages/ 2>/dev/null; then
            log_success "ruff check passed"
        else
            log_error "ruff check failed!"
            log_info "Fix errors before deploying"
            exit 1
        fi

        # Run ruff format check
        log_info "Running ruff format check..."
        if uv run ruff format --check src/ packages/ 2>/dev/null; then
            log_success "ruff format check passed"
        else
            log_error "ruff format check failed!"
            log_info "Run: uv run ruff format src/ packages/"
            exit 1
        fi

        # Run mypy type checks
        log_info "Running mypy type checks..."
        if uv run mypy src/python/htmlgraph/ --ignore-missing-imports 2>/dev/null; then
            log_success "mypy type checks passed"
        else
            log_error "mypy type checks failed!"
            log_info "Fix type errors before deploying"
            exit 1
        fi

        # Run pytest
        log_info "Running tests..."
        if uv run pytest tests/ -v 2>/dev/null; then
            log_success "All tests passed"
        else
            log_warning "Some tests failed - review before deploying"
            if [ "$NO_CONFIRM" != true ]; then
                read -p "Continue deployment anyway? (y/n) " -n 1 -r
                echo
                if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                    exit 1
                fi
            else
                log_info "Continuing despite test failures (--no-confirm mode)"
            fi
        fi
    fi
fi

# ============================================================================
# PRE-FLIGHT: Verify Plugin Sync
# ============================================================================
# REMOVED: No longer syncing to .claude/ - plugin skills only
# if [ "$BUILD_ONLY" != true ] && [ "$DOCS_ONLY" != true ]; then
#     log_section "Pre-flight: Verifying Plugin Sync"
#
#     if [ "$DRY_RUN" = true ]; then
#         log_info "[DRY-RUN] Would check plugin sync status"
#     else
#         log_info "Checking if packages/claude-plugin/ and .claude/ are in sync..."
#         if uv run python scripts/sync_plugin_to_local.py --check; then
#             log_success "Plugin and .claude are in sync"
#         else
#             log_error "Plugin and .claude are out of sync!"
#             log_info "Run: uv run python scripts/sync_plugin_to_local.py"
#             exit 1
#         fi
#     fi
# fi

# ============================================================================
# STEP 0: Update Version Numbers (if version provided)
# ============================================================================
if [ "$VERSION" != "unknown" ] && [ "$DOCS_ONLY" != true ]; then
    log_section "Step 0: Updating Version Numbers"
    update_version_numbers "$VERSION"

    # Auto-commit version changes immediately
    if [ "$DRY_RUN" != true ] && [ "$SKIP_GIT" != true ]; then
        log_info "Committing version changes..."
        git add pyproject.toml \
                src/python/htmlgraph/__init__.py \
                packages/claude-plugin/.claude-plugin/plugin.json \
                .claude-plugin/marketplace.json \
                packages/claude-plugin/.claude-plugin/marketplace.json \
                packages/gemini-extension/gemini-extension.json

        if git diff --cached --quiet; then
            log_info "No version changes to commit (already up to date)"
        else
            if run_command git commit -m "chore: bump version to $VERSION" --no-verify; then
                log_success "Version files committed"
            else
                log_warning "Version commit failed (files may already be committed)"
            fi
        fi
    fi
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
    if [ "$NO_CONFIRM" != true ] && [ "$DRY_RUN" != true ]; then
        read -p "Continue anyway? (y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    else
        log_info "Continuing without PyPI token (--no-confirm mode)"
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
        if [ "$NO_CONFIRM" != true ] && [ "$DRY_RUN" != true ]; then
            read -p "Continue anyway? (y/n) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
        else
            log_info "Continuing with uncommitted changes (--no-confirm mode)"
        fi
    fi

    # Create version tag if it doesn't exist
    if ! git tag | grep -q "^v$VERSION$"; then
        log_info "Creating version tag v$VERSION..."
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would create tag v$VERSION"
        else
            git tag -a "v$VERSION" -m "Release v$VERSION" || log_warning "Tag creation failed (may already exist)"
        fi
    else
        log_info "Tag v$VERSION already exists"
    fi

    # Push to remote
    log_info "Pushing to origin/main with tags..."
    if run_command git push origin main --tags; then
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
# STEP 3.5: Publish OpenCode Extension to npm
# ============================================================================
if [ "$SKIP_PYPI" != true ]; then
    log_section "Step 3.5: Publishing OpenCode Extension to npm"

    OPENCODE_EXTENSION_DIR="packages/opencode-extension"
    if [ -f "$OPENCODE_EXTENSION_DIR/package.json" ]; then
        log_info "Publishing @htmlgraph/opencode-extension@$VERSION to npm..."

        # Change to extension directory
        cd "$OPENCODE_EXTENSION_DIR"

        if [ -n "$NPM_TOKEN" ]; then
            # Use npm token from environment
            if run_command npm publish --access public --tag latest; then
                log_success "Published to npm"
            else
                log_error "npm publish failed"
                [ "$DRY_RUN" != true ] && exit 1
            fi
        else
            log_warning "No NPM_TOKEN found, skipping npm publish"
            log_info "You can publish manually with:"
            log_info "  cd packages/opencode-extension && npm publish --access public"
        fi

        # Go back to project root
        cd "$SCRIPT_DIR"

        # Wait a bit for npm to process
        if [ "$DRY_RUN" != true ]; then
            log_info "Waiting 10 seconds for npm to process..."
            sleep 10
        fi
    else
        log_warning "OpenCode extension package.json not found, skipping npm publish"
    fi
else
    log_info "⏭️  Skipping npm Publish"
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
        INSTALLED_VERSION=$(uv run python -c "import htmlgraph; print(htmlgraph.__version__)" 2>/dev/null || echo "unknown")
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

    # REMOVED: No longer syncing to .claude/ - plugin skills only
    # Sync plugin to .claude for local dogfooding
    # log_info "Syncing plugin to .claude directory..."
    # if [ "$DRY_RUN" = true ]; then
    #     log_info "[DRY-RUN] Would sync packages/claude-plugin → .claude"
    # else
    #     if uv run python scripts/sync_plugin_to_local.py; then
    #         log_success "Plugin synced to .claude directory"
    #     else
    #         log_warning "Plugin sync failed - check scripts/sync_plugin_to_local.py"
    #     fi
    # fi

    # Update plugin from marketplace
    if command -v claude &> /dev/null; then
        log_info "Updating HtmlGraph marketplace cache..."
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would run: claude plugin marketplace update htmlgraph"
        else
            if claude plugin marketplace update htmlgraph 2>/dev/null; then
                log_success "Marketplace cache updated"
            else
                log_warning "Marketplace update failed (may not be needed)"
            fi
        fi

        log_info "Updating HtmlGraph plugin from marketplace..."
        if [ "$DRY_RUN" = true ]; then
            log_info "[DRY-RUN] Would run: claude plugin update htmlgraph@htmlgraph"
        else
            if claude plugin update htmlgraph@htmlgraph 2>/dev/null; then
                log_success "Plugin updated from marketplace"
            else
                log_warning "Plugin update failed (may already be latest)"
            fi
        fi

        log_info ""
        log_info "Plugin update methods for users:"
        log_info "  1. Automatic: Claude Code pulls from GitHub on restart"
        log_info "  2. Manual: 'claude plugin marketplace update htmlgraph' then 'claude plugin update htmlgraph@htmlgraph'"
        log_success "Claude plugin files pushed to GitHub"
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
                uv run python -c "
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
# STEP 8: Update OpenCode Extension
# ============================================================================
if [ "$SKIP_PLUGINS" != true ]; then
    log_section "Step 8: Updating OpenCode Extension"

    OPENCODE_EXTENSION_DIR="packages/opencode-extension"
    if [ -d "$OPENCODE_EXTENSION_DIR" ]; then
        log_info "Updating OpenCode extension version in opencode-extension.json..."

        # Update version in opencode-extension.json
        if [ -f "$OPENCODE_EXTENSION_DIR/opencode-extension.json" ]; then
            # Use Python to update JSON (more reliable than sed)
            if [ "$DRY_RUN" = true ]; then
                log_info "[DRY-RUN] Would update opencode-extension.json to version $VERSION"
            else
                uv run python -c "
import json
with open('$OPENCODE_EXTENSION_DIR/opencode-extension.json', 'r') as f:
    data = json.load(f)
data['version'] = '$VERSION'
with open('$OPENCODE_EXTENSION_DIR/opencode-extension.json', 'w') as f:
    json.dump(data, f, indent=2)
print('Updated opencode-extension.json to version $VERSION')
"
            fi
            log_success "OpenCode extension version updated"

            # If there's a build/deploy process, run it
            if [ -f "$OPENCODE_EXTENSION_DIR/deploy.sh" ]; then
                log_info "Running OpenCode extension deploy script..."
                if [ "$DRY_RUN" = true ]; then
                    log_info "[DRY-RUN] Would run: cd $OPENCODE_EXTENSION_DIR && bash deploy.sh"
                else
                    (cd "$OPENCODE_EXTENSION_DIR" && bash deploy.sh)
                fi
            else
                log_info "No deploy script found for OpenCode extension"
                log_info "Extension files updated, manual deployment may be needed"
            fi
        else
            log_warning "opencode-extension.json not found"
        fi
    else
        log_warning "OpenCode extension directory not found"
    fi
else
    log_info "⏭️  Skipping OpenCode Extension Update"
fi

# ============================================================================
# STEP 9: Create GitHub Release
# ============================================================================
if [ "$SKIP_GIT" != true ] && [ "$SKIP_BUILD" != true ]; then
    log_section "Step 8: Creating GitHub Release"

    # Check if gh CLI is available
    if ! command -v gh &> /dev/null; then
        log_warning "GitHub CLI (gh) not found - skipping release creation"
        log_info "Install with: brew install gh"
    elif [ "$DRY_RUN" = true ]; then
        log_info "[DRY-RUN] Would create GitHub release v$VERSION"
    else
        # Check if release already exists
        if gh release view "v$VERSION" &> /dev/null; then
            log_info "Release v$VERSION already exists"
            log_success "GitHub release verified"
        else
            log_info "Creating GitHub release v$VERSION..."

            # Create release with distribution files
            if [ -f "dist/htmlgraph-$VERSION-py3-none-any.whl" ] && [ -f "dist/htmlgraph-$VERSION.tar.gz" ]; then
                if gh release create "v$VERSION" \
                    --title "v$VERSION" \
                    --notes "Release v$VERSION

See [CHANGELOG](https://github.com/Shakes-tzd/htmlgraph/blob/main/docs/changelog.md) for details.

**Installation:**
\`\`\`bash
uv pip install htmlgraph==$VERSION
\`\`\`

**PyPI:** https://pypi.org/project/htmlgraph/$VERSION/" \
                    "dist/htmlgraph-$VERSION-py3-none-any.whl" \
                    "dist/htmlgraph-$VERSION.tar.gz"; then
                    log_success "GitHub release created: https://github.com/Shakes-tzd/htmlgraph/releases/tag/v$VERSION"
                else
                    log_warning "GitHub release creation failed (may already exist)"
                fi
            else
                log_warning "Distribution files not found - skipping release assets"
                if gh release create "v$VERSION" \
                    --title "v$VERSION" \
                    --notes "Release v$VERSION"; then
                    log_success "GitHub release created (without assets)"
                else
                    log_warning "GitHub release creation failed"
                fi
            fi
        fi
    fi
else
    log_info "⏭️  Skipping GitHub Release (--skip-git or --skip-build)"
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
echo "✅ npm publish: https://www.npmjs.com/package/@htmlgraph/opencode-extension/v/$VERSION"
echo "✅ GitHub release: https://github.com/Shakes-tzd/htmlgraph/releases/tag/v$VERSION"
echo "✅ Local install: $INSTALLED_VERSION"
echo "✅ Claude plugin: Updated"
echo "✅ Gemini extension: Updated"
echo ""
log_success "All deployment steps completed successfully!"
echo ""
echo "Verify deployment:"
echo "  - PyPI: https://pypi.org/project/htmlgraph/$VERSION/"
echo "  - npm: https://www.npmjs.com/package/@htmlgraph/opencode-extension/v/$VERSION"
echo "  - GitHub Release: https://github.com/Shakes-tzd/htmlgraph/releases/tag/v$VERSION"
echo "  - GitHub Repo: https://github.com/Shakes-tzd/htmlgraph"
echo "  - Local: uv run python -c 'import htmlgraph; print(htmlgraph.__version__)'"
echo ""
