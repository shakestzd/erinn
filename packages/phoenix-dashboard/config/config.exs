import Config

config :htmlgraph_dashboard, HtmlgraphDashboardWeb.Endpoint,
  url: [host: "localhost"],
  render_errors: [
    formats: [html: HtmlgraphDashboardWeb.ErrorHTML],
    layout: false
  ],
  pubsub_server: HtmlgraphDashboard.PubSub,
  live_view: [signing_salt: "htmlgraph_lv"]

config :htmlgraph_dashboard,
  db_path: System.get_env("HTMLGRAPH_DB_PATH")

config :logger, :console,
  format: "$time $metadata[$level] $message\n",
  metadata: [:request_id]

config :phoenix, :json_library, Jason

config :esbuild,
  version: "0.25.0",
  default: [
    args: ~w(js/app.js --bundle --target=es2017 --outdir=../priv/static/assets --external:/fonts/* --external:/images/*),
    cd: Path.expand("../assets", __DIR__),
    env: %{"NODE_PATH" => Path.expand("../deps", __DIR__)}
  ]

config :tailwind,
  version: "4.1.12",
  default: [
    args: ~w(--input=css/app.css --output=../priv/static/assets/app.css),
    cd: Path.expand("../assets", __DIR__)
  ]

import_config "#{config_env()}.exs"
