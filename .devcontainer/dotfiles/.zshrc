# =============================================================================
# ZSH CONFIGURATION - Container-adapted (devcontainer)
# =============================================================================

# Export truecolor environment for shell tools (Oh My Posh, Powerlevel10k)
export TERM=xterm-256color
export COLORTERM=truecolor

# Official direnv + Powerlevel10k integration (prevents instant prompt warnings)
(( ${+commands[direnv]} )) && emulate zsh -c "$(direnv export zsh)"

# Enable Powerlevel10k instant prompt
if [[ -r "${XDG_CACHE_HOME:-$HOME/.cache}/p10k-instant-prompt-${(%):-%n}.zsh" ]]; then
  source "${XDG_CACHE_HOME:-$HOME/.cache}/p10k-instant-prompt-${(%):-%n}.zsh"
fi

# =============================================================================
# OH-MY-ZSH SETUP
# =============================================================================

# Path to your oh-my-zsh installation
export ZSH="$HOME/.oh-my-zsh"

# Theme configuration - Empty since we're using Powerlevel10k
ZSH_THEME=""

# Plugins (nvm dropped — Node comes from the devcontainer feature)
plugins=(git zsh-syntax-highlighting zsh-autosuggestions)

# Source Oh My Zsh
source $ZSH/oh-my-zsh.sh

# Load Powerlevel10k theme
source ~/powerlevel10k/powerlevel10k.zsh-theme

# Load Powerlevel10k configuration
[[ ! -f ~/.p10k.zsh ]] || source ~/.p10k.zsh

# =============================================================================
# SMART DIRECTORY NAVIGATION WITH UV AUTO-ACTIVATION
# =============================================================================

# Enhanced cd function - UV environment auto-activation
cd() {
  builtin cd "$@"

  # Auto-activate UV virtual environment if .venv exists
  current_venv_path="$(pwd -P)/.venv"

  if [ -d ".venv" ]; then
    # If VIRTUAL_ENV is set but doesn't match current directory, deactivate and unset it
    if [ -n "$VIRTUAL_ENV" ]; then
      venv_abs_path="$(cd "$VIRTUAL_ENV" 2>/dev/null && pwd -P 2>/dev/null || echo "$VIRTUAL_ENV")"
      if [ "$venv_abs_path" != "$current_venv_path" ]; then
        if type deactivate &>/dev/null; then
          deactivate 2>/dev/null || true
        fi
        unset VIRTUAL_ENV
      fi
    fi

    # Activate if not already activated
    if [ -z "$VIRTUAL_ENV" ]; then
      echo "Activating UV environment (.venv found)"
      # Explicitly set VIRTUAL_ENV before sourcing activate to ensure correct path
      export VIRTUAL_ENV="$current_venv_path"
      source .venv/bin/activate
    fi
  elif [ -n "$VIRTUAL_ENV" ] && [ ! -d ".venv" ]; then
    # Deactivate if we left a UV project directory (only if deactivate function exists)
    if type deactivate &>/dev/null; then
      echo "Deactivating environment (no .venv in current directory)"
      deactivate
    fi
  fi
}

# =============================================================================
# PATH CONFIGURATION
# =============================================================================

export PATH="/usr/local/bin:$PATH"
export PATH="$HOME/.local/bin:$PATH"         # UV binaries + htmlgraph

# =============================================================================
# PYTHON ENVIRONMENT SETUP
# =============================================================================

# Python alias to ensure python3
alias python=python3

# =============================================================================
# UV PACKAGE MANAGER - Primary Python Tool
# =============================================================================

# UV aliases for common operations
alias pip="uv pip"
alias venv="uv venv"
alias pipi="uv pip install"
alias pipu="uv pip uninstall"
alias uvs="uv sync"
alias uva="source .venv/bin/activate"
alias uvd="deactivate"

# UV shell completion
if command -v uv &> /dev/null; then
  source <(uv --generate-shell-completion zsh)
fi

# Enhanced UV utility functions
uvenv() {
  case "$1" in
    "create")
      echo "Creating UV virtual environment..."
      uv venv
      if [ -d ".venv" ]; then
        source .venv/bin/activate
        echo "Environment created and activated"
      fi
      ;;
    "activate")
      if [ -d ".venv" ]; then
        source .venv/bin/activate
        echo "Environment activated"
      else
        echo "No .venv directory found. Run 'uvenv create' first."
      fi
      ;;
    "sync")
      if [ -f "pyproject.toml" ]; then
        echo "Syncing UV project..."
        uv sync --all-extras
      elif [ -f "requirements.txt" ]; then
        echo "Syncing from requirements.txt..."
        uv pip sync requirements.txt
      else
        echo "No pyproject.toml or requirements.txt found"
      fi
      ;;
    "status")
      echo "Environment Status:"
      if [ -n "$VIRTUAL_ENV" ]; then
        echo "  Active: $(basename $VIRTUAL_ENV)"
        echo "  Path: $VIRTUAL_ENV"
        echo "  Python: $(which python)"
        echo "  Version: $(python --version)"
      else
        echo "  No active environment"
      fi
      if [ -f "pyproject.toml" ]; then
        echo "  UV project: yes"
      fi
      if [ -d ".venv" ]; then
        echo "  Local .venv: yes"
      fi
      ;;
    "deactivate")
      if [ -n "$VIRTUAL_ENV" ]; then
        deactivate
        echo "Environment deactivated"
      else
        echo "No active environment to deactivate"
      fi
      ;;
    *)
      echo "Usage: uvenv [create|activate|sync|status|deactivate]"
      ;;
  esac
}

# =============================================================================
# GENERAL ALIASES
# =============================================================================

# Quick navigation
alias ..="cd .."
alias ...="cd ../.."

# Git shortcuts
alias gs="git status"
alias gp="git push"
alias gl="git pull"
alias ga="git add"
alias gc="git commit"
alias gco="git checkout"

# =============================================================================
# PERFORMANCE OPTIMIZATIONS
# =============================================================================

# Reduce startup time by lazy-loading heavy tools
autoload -U compinit
compinit

# Enable command completion caching
zstyle ':completion:*' use-cache on
zstyle ':completion:*' cache-path ~/.zsh/cache

# Direnv hook (part 2 of official Powerlevel10k integration)
(( ${+commands[direnv]} )) && emulate zsh -c "$(direnv hook zsh)"
