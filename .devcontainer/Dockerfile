# syntax=docker/dockerfile:1.7

FROM mcr.microsoft.com/devcontainers/go:1-1.24-bookworm

ENV DEBIAN_FRONTEND=noninteractive
ENV PATH="/home/vscode/.local/bin:${PATH}"
ENV TERM=xterm-256color
ENV COLORTERM=truecolor

RUN rm -f /etc/apt/sources.list.d/yarn.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        bash-completion \
        build-essential \
        curl \
        direnv \
        fd-find \
        jq \
        less \
        make \
        pkg-config \
        ripgrep \
        shellcheck \
        sqlite3 \
        tree \
        unzip \
        xz-utils \
        zsh \
    && rm -rf /var/lib/apt/lists/* \
    && if [ -x /usr/bin/fdfind ] && [ ! -e /usr/local/bin/fd ]; then ln -s /usr/bin/fdfind /usr/local/bin/fd; fi \
    && mkdir -p /home/vscode/.local/bin /home/vscode/.local/share \
    && chown -R vscode:vscode /home/vscode/.local
