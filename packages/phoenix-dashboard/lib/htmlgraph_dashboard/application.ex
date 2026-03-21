defmodule HtmlgraphDashboard.Application do
  @moduledoc false
  use Application

  @impl true
  def start(_type, _args) do
    # Initialize embedded Python with htmlgraph SDK (optional — may fail in Docker)
    python_available =
      try do
        Pythonx.uv_init("""
        [project]
        name = "htmlgraph-dashboard"
        version = "0.0.0"
        requires-python = ">=3.10"
        dependencies = ["htmlgraph>=0.33.80"]
        """)
        true
      rescue
        e ->
          IO.puts("[warning] Pythonx unavailable: #{Exception.message(e)}. Graph stats and work item details will be limited.")
          false
      end

    Application.put_env(:htmlgraph_dashboard, :python_available, python_available)

    children =
      [
        if(python_available, do: HtmlgraphDashboard.PythonSDK),
        {Phoenix.PubSub, name: HtmlgraphDashboard.PubSub},
        HtmlgraphDashboardWeb.Endpoint,
        {HtmlgraphDashboard.EventPoller, []}
      ]
      |> Enum.reject(&is_nil/1)

    opts = [strategy: :one_for_one, name: HtmlgraphDashboard.Supervisor]
    Supervisor.start_link(children, opts)
  end

  @impl true
  def config_change(changed, _new, removed) do
    HtmlgraphDashboardWeb.Endpoint.config_change(changed, removed)
    :ok
  end
end
