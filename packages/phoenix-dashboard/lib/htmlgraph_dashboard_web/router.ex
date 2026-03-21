defmodule HtmlgraphDashboardWeb.Router do
  use Phoenix.Router, helpers: false

  import Plug.Conn
  import Phoenix.LiveView.Router

  pipeline :browser do
    plug :accepts, ["html"]
    plug :fetch_session
    plug :fetch_live_flash
    plug :put_root_layout, html: {HtmlgraphDashboardWeb.Layouts, :root}
    plug :protect_from_forgery
    plug :put_secure_browser_headers
  end

  # Healthcheck endpoint — no session or CSRF needed
  scope "/" do
    get "/up", HtmlgraphDashboardWeb.HealthController, :index
  end

  scope "/", HtmlgraphDashboardWeb do
    pipe_through :browser

    live "/", ActivityFeedLive, :index
    live "/session/:session_id", ActivityFeedLive, :session
    live "/graph", GraphLive, :index
    live "/kanban", KanbanLive, :index
  end
end
