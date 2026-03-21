# Build stage
FROM elixir:1.18-alpine AS build

RUN apk add --no-cache build-base git python3

WORKDIR /app

# Install hex + rebar
RUN mix local.hex --force && mix local.rebar --force

# Set build env
ENV MIX_ENV=prod

# Install deps
COPY mix.exs mix.lock ./
COPY deps deps
RUN mix deps.get --only prod || true
RUN mix deps.compile

# Copy app source
COPY config config
COPY lib lib
COPY assets assets
COPY priv priv

# Build assets (tailwind + esbuild)
RUN mix assets.deploy

# Compile and build release
RUN mix compile
RUN mix release

# Runtime stage
FROM alpine:3.23 AS runtime

RUN apk add --no-cache libstdc++ openssl ncurses-libs python3 && \
    wget -qO- https://astral.sh/uv/install.sh | sh && \
    ln -s /root/.local/bin/uv /usr/local/bin/uv

WORKDIR /app

# Copy release from build stage
COPY --from=build /app/_build/prod/rel/htmlgraph_dashboard ./

# Create storage directory for mounted volumes
RUN mkdir -p /storage

# Expose port
EXPOSE 4000

# Set env vars
ENV HTMLGRAPH_DB_PATH=/storage/htmlgraph.db
ENV PHX_HOST=localhost
ENV PORT=4000

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -q --spider http://localhost:4000/up || exit 1

# Start the release
CMD ["bin/htmlgraph_dashboard", "start"]
