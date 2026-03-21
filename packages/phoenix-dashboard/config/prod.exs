import Config

config :htmlgraph_dashboard, HtmlgraphDashboardWeb.Endpoint,
  cache_static_manifest: "priv/static/cache_manifest.json",
  server: true

config :logger, level: :info
