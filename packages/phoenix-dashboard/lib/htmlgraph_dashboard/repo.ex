defmodule HtmlgraphDashboard.Repo do
  @moduledoc """
  Direct SQLite3 reader for the HtmlGraph database.

  Read-only access to the existing .htmlgraph/htmlgraph.db file.
  Uses exqlite for lightweight SQLite3 connectivity.
  """

  @doc """
  Returns the configured database path, resolved relative to the app root.
  """
  def db_path do
    case Application.get_env(:htmlgraph_dashboard, :db_path) do
      nil -> find_project_db()
      path when is_binary(path) -> path
    end
  end

  defp find_project_db do
    # Walk up from CWD to find the nearest .htmlgraph/htmlgraph.db
    find_project_db_from(File.cwd!())
  end

  defp find_project_db_from("/"), do: Path.expand("~/.htmlgraph/htmlgraph.db")

  defp find_project_db_from(dir) do
    candidate = Path.join([dir, ".htmlgraph", "htmlgraph.db"])

    if File.exists?(candidate) do
      candidate
    else
      find_project_db_from(Path.dirname(dir))
    end
  end

  @doc """
  Execute a read-only query against the HtmlGraph database.
  Returns {:ok, rows} or {:error, reason}.
  """
  def query(sql, params \\ []) do
    path = db_path()

    case Exqlite.Sqlite3.open(path, [:readonly]) do
      {:ok, conn} ->
        try do
          execute_query(conn, sql, params)
        after
          Exqlite.Sqlite3.close(conn)
        end

      {:error, reason} ->
        {:error, {:open_failed, reason, path}}
    end
  end

  @doc """
  Execute a query and return rows as maps with column name keys.
  """
  def query_maps(sql, params \\ []) do
    path = db_path()

    case Exqlite.Sqlite3.open(path, [:readonly]) do
      {:ok, conn} ->
        try do
          case Exqlite.Sqlite3.prepare(conn, sql) do
            {:ok, stmt} ->
              bind_params(conn, stmt, params)
              rows = collect_rows(conn, stmt)
              columns = get_columns(conn, stmt)
              Exqlite.Sqlite3.release(conn, stmt)

              maps =
                Enum.map(rows, fn row ->
                  columns
                  |> Enum.zip(row)
                  |> Map.new()
                end)

              {:ok, maps}

            {:error, reason} ->
              {:error, reason}
          end
        after
          Exqlite.Sqlite3.close(conn)
        end

      {:error, reason} ->
        {:error, {:open_failed, reason, path}}
    end
  end

  defp execute_query(conn, sql, params) do
    case Exqlite.Sqlite3.prepare(conn, sql) do
      {:ok, stmt} ->
        bind_params(conn, stmt, params)
        rows = collect_rows(conn, stmt)
        Exqlite.Sqlite3.release(conn, stmt)
        {:ok, rows}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp bind_params(_conn, _stmt, []), do: :ok

  defp bind_params(_conn, stmt, params) do
    Exqlite.Sqlite3.bind(stmt, params)
  end

  defp collect_rows(conn, stmt) do
    collect_rows(conn, stmt, [])
  end

  defp collect_rows(conn, stmt, acc) do
    case Exqlite.Sqlite3.step(conn, stmt) do
      {:row, row} -> collect_rows(conn, stmt, [row | acc])
      :done -> Enum.reverse(acc)
      {:error, _reason} -> Enum.reverse(acc)
    end
  end

  defp get_columns(conn, stmt) do
    case Exqlite.Sqlite3.columns(conn, stmt) do
      {:ok, column_names} -> column_names
      {:error, _reason} -> []
    end
  end
end
