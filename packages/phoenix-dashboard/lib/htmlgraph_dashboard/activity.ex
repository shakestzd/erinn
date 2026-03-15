defmodule HtmlgraphDashboard.Activity do
  @moduledoc """
  Queries and structures the activity feed data from the HtmlGraph database.

  Builds a multi-level nested tree:
    Session -> UserQuery (conversation turn) -> Tool events -> Subagent events
  """

  alias HtmlgraphDashboard.Repo

  @max_depth 4

  # --- Work Item Resolution ---

  @doc """
  Resolve the .htmlgraph directory path (sibling to the database file).
  """
  def htmlgraph_dir do
    Repo.db_path() |> Path.dirname()
  end

  @doc """
  Batch-fetch work item metadata for a list of feature_ids.
  Returns %{feature_id => %{"id" => ..., "title" => ..., "type" => ...}}.

  Tries the SQLite features table first, then falls back to HTML file parsing.
  """
  def fetch_work_item_titles(feature_ids) when is_list(feature_ids) do
    ids = feature_ids |> Enum.reject(&is_nil/1) |> Enum.uniq()
    if ids == [], do: %{}, else: do_fetch_work_item_titles(ids)
  end

  defp do_fetch_work_item_titles(ids) do
    # Try SQLite features table first
    placeholders = ids |> Enum.map(fn _ -> "?" end) |> Enum.join(", ")

    sql = """
    SELECT id, title, type, status FROM features WHERE id IN (#{placeholders})
    """

    db_results =
      case Repo.query_maps(sql, ids) do
        {:ok, rows} -> Map.new(rows, fn r -> {r["id"], r} end)
        {:error, _} -> %{}
      end

    # For any IDs not found in DB, fall back to HTML file parsing
    missing = Enum.reject(ids, fn id -> Map.has_key?(db_results, id) end)

    html_results = Map.new(missing, fn id -> {id, parse_work_item_html(id)} end)

    Map.merge(db_results, html_results)
  end

  @doc """
  Fetch full work item details by ID, including steps parsed from HTML.
  Returns a map with id, title, type, status, priority, created_at, steps.
  """
  def fetch_work_item_detail(feature_id) when is_binary(feature_id) do
    parse_work_item_html(feature_id)
  end

  def fetch_work_item_detail(_), do: nil

  defp parse_work_item_html(feature_id) do
    features_dir = Path.join(htmlgraph_dir(), "features")
    path = Path.join(features_dir, "#{feature_id}.html")

    if File.exists?(path) do
      case File.read(path) do
        {:ok, content} -> extract_work_item_from_html(feature_id, content)
        {:error, _} -> fallback_work_item(feature_id)
      end
    else
      fallback_work_item(feature_id)
    end
  end

  defp extract_work_item_from_html(feature_id, html) do
    title =
      case Regex.run(~r/<title>([^<]+)<\/title>/i, html) do
        [_, t] -> String.trim(t)
        _ -> feature_id
      end

    type =
      case Regex.run(~r/data-type="([^"]+)"/, html) do
        [_, t] -> t
        _ -> infer_type_from_id(feature_id)
      end

    status =
      case Regex.run(~r/data-status="([^"]+)"/, html) do
        [_, s] -> s
        _ -> "unknown"
      end

    priority =
      case Regex.run(~r/data-priority="([^"]+)"/, html) do
        [_, p] -> p
        _ -> "medium"
      end

    created_at =
      case Regex.run(~r/data-created="([^"]+)"/, html) do
        [_, c] -> c
        _ -> nil
      end

    track_id =
      case Regex.run(~r/data-track-id="([^"]+)"/, html) do
        [_, t] -> t
        _ -> nil
      end

    # Extract steps from <li> elements inside the steps section
    steps = extract_steps(html)

    # Extract description from the first <p> in <section class="description">
    description =
      case Regex.run(~r/<section[^>]*class="description"[^>]*>[\s\S]*?<p>([^<]+)<\/p>/i, html) do
        [_, d] -> String.trim(d)
        _ -> nil
      end

    %{
      "id" => feature_id,
      "title" => title,
      "type" => type,
      "status" => status,
      "priority" => priority,
      "created_at" => created_at,
      "track_id" => track_id,
      "steps" => steps,
      "description" => description
    }
  end

  defp extract_steps(html) do
    # Look for steps section and extract list items
    case Regex.run(~r/<section[^>]*data-steps[^>]*>([\s\S]*?)<\/section>/i, html) do
      [_, steps_html] ->
        Regex.scan(~r/<li[^>]*>([^<]+)<\/li>/i, steps_html)
        |> Enum.map(fn [_, text] -> String.trim(text) end)

      _ ->
        # Try alternate pattern: <ol> or <ul> with steps
        case Regex.run(~r/<(?:ol|ul)[^>]*class="[^"]*steps[^"]*"[^>]*>([\s\S]*?)<\/(?:ol|ul)>/i, html) do
          [_, steps_html] ->
            Regex.scan(~r/<li[^>]*>([^<]+)<\/li>/i, steps_html)
            |> Enum.map(fn [_, text] -> String.trim(text) end)

          _ ->
            []
        end
    end
  end

  defp infer_type_from_id(id) do
    cond do
      String.starts_with?(id, "feat-") -> "feature"
      String.starts_with?(id, "bug-") -> "bug"
      String.starts_with?(id, "spk-") -> "spike"
      String.starts_with?(id, "trk-") -> "track"
      true -> "feature"
    end
  end

  defp fallback_work_item(feature_id) do
    # No HTML file found -- create a minimal entry from the ID prefix
    %{
      "id" => feature_id,
      "title" => feature_id,
      "type" => infer_type_from_id(feature_id),
      "status" => "unknown",
      "priority" => "medium",
      "created_at" => nil,
      "track_id" => nil,
      "steps" => [],
      "description" => nil
    }
  end

  @doc """
  Fetch recent conversation turns with nested children, grouped by session.
  Returns a list of session groups, each containing conversation turns.
  """
  def list_activity_feed(opts \\ []) do
    limit = Keyword.get(opts, :limit, 50)
    session_id = Keyword.get(opts, :session_id, nil)

    # Fetch UserQuery events (conversation turns) — these are the top-level entries
    user_queries = fetch_user_queries(limit, session_id)

    # For each UserQuery, recursively fetch children + adopt orphans
    raw_turns =
      Enum.map(user_queries, fn uq ->
        children = fetch_children_with_subagents(uq["event_id"], uq["session_id"], 0)

        # Adopt orphan events that belong to this UserQuery's time window
        orphans = fetch_orphan_events(uq, user_queries)
        all_children = merge_children_by_timestamp(children, orphans)

        displayed_children =
          all_children
          |> Enum.map(&sanitize_tree/1)
          |> Enum.filter(&has_meaningful_content/1)

        stats = compute_stats(displayed_children)

        %{
          user_query: sanitize_event(uq),
          children: displayed_children,
          stats: stats,
          raw_feature_id: uq["feature_id"]
        }
      end)

    # Collect all unique feature_ids from turns and their children, then batch-fetch
    all_feature_ids =
      raw_turns
      |> Enum.flat_map(fn turn ->
        turn_id = turn.raw_feature_id
        child_ids = collect_feature_ids(turn.children)
        [turn_id | child_ids]
      end)
      |> Enum.reject(&is_nil/1)
      |> Enum.uniq()

    work_items = fetch_work_item_titles(all_feature_ids)

    # Attach work_item to each turn, with fallback to most common child feature_id
    turns =
      Enum.map(raw_turns, fn turn ->
        work_item =
          cond do
            # Turn has its own feature_id
            turn.raw_feature_id && Map.has_key?(work_items, turn.raw_feature_id) ->
              Map.get(work_items, turn.raw_feature_id)

            # Infer from children: use the most common feature_id
            true ->
              child_ids = collect_feature_ids(turn.children)

              case most_common(child_ids) do
                nil -> nil
                id -> Map.get(work_items, id)
              end
          end

        turn
        |> Map.put(:work_item, work_item)
        |> Map.delete(:raw_feature_id)
      end)

    # Group by session
    turns
    |> Enum.group_by(fn t -> t.user_query["session_id"] end)
    |> Enum.map(fn {sid, session_turns} ->
      session = fetch_session(sid)

      %{
        session_id: sid,
        session: session,
        turns: session_turns
      }
    end)
    |> Enum.sort_by(
      fn group ->
        case group.turns do
          [first | _] -> first.user_query["timestamp"]
          [] -> ""
        end
      end,
      :desc
    )
  end

  # Recursively collect all feature_ids from a nested children tree
  defp collect_feature_ids(children) when is_list(children) do
    Enum.flat_map(children, fn child ->
      id = child["feature_id"]
      grandchild_ids = collect_feature_ids(child["children"] || [])
      if id, do: [id | grandchild_ids], else: grandchild_ids
    end)
  end

  defp collect_feature_ids(_), do: []

  # Return the most frequently occurring element in a list, or nil if empty
  defp most_common([]), do: nil

  defp most_common(list) do
    list
    |> Enum.frequencies()
    |> Enum.max_by(fn {_id, count} -> count end)
    |> elem(0)
  end

  @doc """
  Fetch a single event by ID with its full subtree.
  """
  def get_event_tree(event_id) do
    sql = """
    SELECT event_id, tool_name, event_type, timestamp, input_summary,
           output_summary, session_id, agent_id, parent_event_id,
           subagent_type, model, status, cost_tokens,
           execution_duration_seconds, feature_id, context
    FROM agent_events
    WHERE event_id = ?
    """

    case Repo.query_maps(sql, [event_id]) do
      {:ok, [event]} ->
        children = fetch_children_with_subagents(event_id, event["session_id"], 0)
        {:ok, Map.put(event, "children", children)}

      {:ok, []} ->
        {:error, :not_found}

      {:error, reason} ->
        {:error, reason}
    end
  end

  # --- Summary Sanitization ---

  @doc """
  Sanitize a summary string by stripping noise:
  - XML tags (task-notification, system-reminder) and their content
  - Raw JSON objects (context/metadata dumps)
  - Truncate to 120 chars
  """
  def sanitize_summary(nil), do: ""
  def sanitize_summary(""), do: ""

  def sanitize_summary(text) when is_binary(text) do
    trimmed = String.trim(text)

    # Early exit: if string starts with {", it's a raw JSON metadata dump — discard entirely
    if String.starts_with?(trimmed, "{\"") do
      ""
    else
      trimmed
      |> strip_xml_tags()
      |> strip_json_dumps()
      |> String.trim()
      |> strip_agent_prefix()
      |> truncate_text(120)
    end
  end

  defp strip_xml_tags(text) do
    text
    # Strip matched pairs first (greedy within each pair)
    |> String.replace(~r/<task-notification>[\s\S]*?<\/task-notification>/i, "")
    |> String.replace(~r/<system-reminder>[\s\S]*?<\/system-reminder>/i, "")
    |> String.replace(~r/<[a-zA-Z_-]+>[\s\S]*?<\/[a-zA-Z_-]+>/i, "")
    # Strip orphaned opening/closing tags (no matching pair in string)
    |> String.replace(~r/<\/?[a-zA-Z_-]+>/i, "")
  end

  defp strip_json_dumps(text) do
    # If the entire string looks like a JSON object, replace it
    trimmed = String.trim(text)

    if String.starts_with?(trimmed, "{") and String.ends_with?(trimmed, "}") do
      case Jason.decode(trimmed) do
        {:ok, map} when is_map(map) ->
          # Extract useful fields if present, otherwise discard
          cond do
            Map.has_key?(map, "subagent_type") ->
              prompt = Map.get(map, "prompt", "")
              type = Map.get(map, "subagent_type", "")

              if prompt != "" do
                "Task (#{type}): #{prompt}"
              else
                "Task delegation: #{type}"
              end

            Map.has_key?(map, "session_id") ->
              # Pure context/metadata dump — discard
              ""

            true ->
              trimmed
          end

        _ ->
          trimmed
      end
    else
      text
    end
  end

  defp strip_agent_prefix(text) do
    # Strip agent type prefix like "(htmlgraph:haiku-coder): " since it's shown as a badge
    Regex.replace(~r/^\([a-zA-Z0-9:_-]+\):\s*/, text, "")
  end

  defp truncate_text(text, max_len) do
    if String.length(text) > max_len do
      String.slice(text, 0, max_len) <> "..."
    else
      text
    end
  end

  defp sanitize_event(event) do
    event
    |> Map.update("input_summary", "", &sanitize_summary/1)
    |> Map.update("output_summary", "", &sanitize_summary/1)
  end

  defp sanitize_tree(event) do
    event
    |> sanitize_event()
    |> Map.update("children", [], fn children ->
      children || []
      |> Enum.map(&sanitize_tree/1)
      |> Enum.filter(&has_meaningful_content/1)
    end)
  end

  # Check if an event has meaningful content.
  # Only filters out known noise events.
  # A Bash/Edit/Read/Task event with an empty summary after sanitization is still real work.
  defp has_meaningful_content(event) do
    tool = event["tool_name"] || ""
    # Only filter out known noise events
    tool not in ["Stop", "SessionResume", "InstructionsLoaded", "SessionStart", "SessionEnd"]
  end

  # --- Private: Data fetching ---

  defp fetch_user_queries(limit, nil) do
    sql = """
    SELECT event_id, tool_name, event_type, timestamp, input_summary,
           output_summary, session_id, agent_id, parent_event_id,
           subagent_type, model, status, cost_tokens,
           execution_duration_seconds, feature_id, context
    FROM agent_events
    WHERE tool_name = 'UserQuery'
    ORDER BY timestamp DESC
    LIMIT ?
    """

    case Repo.query_maps(sql, [limit]) do
      {:ok, rows} -> rows
      {:error, _} -> []
    end
  end

  defp fetch_user_queries(limit, session_id) do
    sql = """
    SELECT event_id, tool_name, event_type, timestamp, input_summary,
           output_summary, session_id, agent_id, parent_event_id,
           subagent_type, model, status, cost_tokens,
           execution_duration_seconds, feature_id, context
    FROM agent_events
    WHERE tool_name = 'UserQuery' AND session_id = ?
    ORDER BY timestamp DESC
    LIMIT ?
    """

    case Repo.query_maps(sql, [session_id, limit]) do
      {:ok, rows} -> rows
      {:error, _} -> []
    end
  end

  defp fetch_children_with_subagents(_parent_id, _session_id, depth) when depth >= @max_depth,
    do: []

  defp fetch_children_with_subagents(parent_id, session_id, depth) do
    # Fetch direct children by parent_event_id
    sql = """
    SELECT event_id, tool_name, event_type, timestamp, input_summary,
           output_summary, session_id, agent_id, parent_event_id,
           subagent_type, model, status, cost_tokens,
           execution_duration_seconds, feature_id, context
    FROM agent_events
    WHERE parent_event_id = ?
      AND NOT (tool_name = 'Agent' AND event_type != 'task_delegation')
    ORDER BY timestamp DESC
    """

    rows =
      case Repo.query_maps(sql, [parent_id]) do
        {:ok, rows} -> rows
        {:error, _} -> []
      end

    Enum.map(rows, fn row ->
      grandchildren =
        if row["event_type"] == "task_delegation" do
          # For task delegations, also pull subagent session events
          subagent_children =
            fetch_subagent_events(row["event_id"], session_id, row["subagent_type"], depth + 1)

          direct = fetch_children_with_subagents(row["event_id"], session_id, depth + 1)
          merge_children_by_timestamp(direct, subagent_children)
        else
          fetch_children_with_subagents(row["event_id"], session_id, depth + 1)
        end

      row
      |> Map.put("children", grandchildren)
      |> Map.put("depth", depth)
      |> Map.put("descendant_count", count_descendants(grandchildren))
    end)
  end

  defp fetch_subagent_events(_task_event_id, _parent_session_id, _subagent_type, depth)
       when depth >= @max_depth,
       do: []

  defp fetch_subagent_events(_task_event_id, parent_session_id, subagent_type, depth) do
    # Subagent sessions follow the pattern: {parent_session_id}-{agent_name}
    # Try multiple patterns to find subagent events
    patterns = build_subagent_session_patterns(parent_session_id, subagent_type)

    Enum.flat_map(patterns, fn pattern ->
      sql = """
      SELECT event_id, tool_name, event_type, timestamp, input_summary,
             output_summary, session_id, agent_id, parent_event_id,
             subagent_type, model, status, cost_tokens,
             execution_duration_seconds, feature_id, context
      FROM agent_events
      WHERE session_id LIKE ?
        AND tool_name != 'UserQuery'
        AND NOT (tool_name = 'Agent' AND event_type != 'task_delegation')
      ORDER BY timestamp DESC
      """

      case Repo.query_maps(sql, [pattern]) do
        {:ok, rows} ->
          # Only include events that don't already have a parent pointing elsewhere
          # (they may already be fetched via parent_event_id)
          rows
          |> Enum.reject(fn r -> r["parent_event_id"] != nil end)
          |> Enum.map(fn r ->
            r
            |> Map.put("depth", depth)
            |> Map.put("children", [])
            |> Map.put("descendant_count", 0)
          end)

        {:error, _} ->
          []
      end
    end)
  end

  defp build_subagent_session_patterns(parent_session_id, nil) do
    ["#{parent_session_id}-%"]
  end

  defp build_subagent_session_patterns(parent_session_id, subagent_type) do
    # Try exact match first, then wildcard
    [
      "#{parent_session_id}-#{subagent_type}%"
    ]
  end

  # --- Orphan Adoption ---

  defp fetch_orphan_events(user_query, all_user_queries) do
    session_id = user_query["session_id"]
    uq_timestamp = user_query["timestamp"]
    uq_event_id = user_query["event_id"]

    # Find the next UserQuery in the same session (by timestamp)
    next_uq =
      all_user_queries
      |> Enum.filter(fn uq ->
        uq["session_id"] == session_id and
          uq["timestamp"] > uq_timestamp and
          uq["event_id"] != uq_event_id
      end)
      |> Enum.sort_by(fn uq -> uq["timestamp"] end)
      |> List.first()

    # Query for orphan events in the time window
    {sql, params} =
      if next_uq do
        {"""
         SELECT event_id, tool_name, event_type, timestamp, input_summary,
                output_summary, session_id, agent_id, parent_event_id,
                subagent_type, model, status, cost_tokens,
                execution_duration_seconds, feature_id, context
         FROM agent_events
         WHERE session_id = ?
           AND parent_event_id IS NULL
           AND tool_name != 'UserQuery'
           AND NOT (tool_name = 'Agent' AND event_type != 'task_delegation')
           AND timestamp >= ?
           AND timestamp < ?
         ORDER BY timestamp DESC
         """, [session_id, uq_timestamp, next_uq["timestamp"]]}
      else
        {"""
         SELECT event_id, tool_name, event_type, timestamp, input_summary,
                output_summary, session_id, agent_id, parent_event_id,
                subagent_type, model, status, cost_tokens,
                execution_duration_seconds, feature_id, context
         FROM agent_events
         WHERE session_id = ?
           AND parent_event_id IS NULL
           AND tool_name != 'UserQuery'
           AND NOT (tool_name = 'Agent' AND event_type != 'task_delegation')
           AND timestamp >= ?
         ORDER BY timestamp DESC
         """, [session_id, uq_timestamp]}
      end

    case Repo.query_maps(sql, params) do
      {:ok, rows} ->
        rows
        |> Enum.map(fn row ->
          row
          |> Map.put("depth", 0)
          |> Map.put("children", [])
          |> Map.put("descendant_count", 0)
        end)
        |> Enum.filter(&has_meaningful_content/1)

      {:error, _} ->
        []
    end
  end

  # --- Helpers ---

  defp merge_children_by_timestamp(list_a, list_b) do
    # Deduplicate by event_id, then sort by timestamp descending
    (list_a ++ list_b)
    |> Enum.uniq_by(fn e -> e["event_id"] end)
    |> Enum.sort_by(fn e -> e["timestamp"] end, :desc)
  end

  defp count_descendants(children) do
    Enum.reduce(children, 0, fn child, acc ->
      acc + 1 + (child["descendant_count"] || count_descendants(child["children"] || []))
    end)
  end

  defp compute_stats(children) do
    flat = flatten_children(children)

    %{
      tool_count: length(flat),
      total_duration:
        flat
        |> Enum.map(fn c -> c["execution_duration_seconds"] || 0 end)
        |> Enum.sum()
        |> to_float()
        |> Float.round(2),
      success_count:
        Enum.count(flat, fn c -> c["status"] in ["recorded", "success", "completed"] end),
      error_count: Enum.count(flat, fn c -> c["event_type"] == "error" end),
      models:
        flat |> Enum.map(fn c -> c["model"] end) |> Enum.reject(&is_nil/1) |> Enum.uniq(),
      total_tokens:
        flat
        |> Enum.map(fn c -> c["cost_tokens"] || 0 end)
        |> Enum.sum()
    }
  end

  defp to_float(value) when is_float(value), do: value
  defp to_float(value) when is_integer(value), do: value * 1.0

  defp flatten_children(children) do
    Enum.flat_map(children, fn child ->
      [child | flatten_children(child["children"] || [])]
    end)
  end

  defp fetch_session(nil), do: nil

  defp fetch_session(session_id) do
    sql = """
    SELECT session_id, agent_assigned, status, created_at, completed_at,
           total_events, total_tokens_used, is_subagent, last_user_query,
           model
    FROM sessions
    WHERE session_id = ?
    """

    case Repo.query_maps(sql, [session_id]) do
      {:ok, [session]} -> derive_session_status(session)
      _ -> nil
    end
  end

  defp derive_session_status(session) do
    cond do
      # If completed_at is set, it's completed
      session["completed_at"] != nil ->
        Map.put(session, "status", "completed")

      # If status is already explicitly set to something other than active, keep it
      session["status"] not in [nil, "active"] ->
        session

      # Check if the session's last event is older than 30 minutes
      true ->
        case last_event_timestamp(session["session_id"]) do
          nil ->
            session

          ts_string ->
            case NaiveDateTime.from_iso8601(ts_string) do
              {:ok, last_event_ts} ->
                cutoff = NaiveDateTime.add(NaiveDateTime.utc_now(), -30, :minute)

                if NaiveDateTime.compare(last_event_ts, cutoff) == :lt do
                  Map.put(session, "status", "idle")
                else
                  session
                end

              _ ->
                session
            end
        end
    end
  end

  defp last_event_timestamp(nil), do: nil

  defp last_event_timestamp(session_id) do
    sql = """
    SELECT MAX(timestamp) AS last_ts
    FROM agent_events
    WHERE session_id = ?
    """

    case Repo.query_maps(sql, [session_id]) do
      {:ok, [%{"last_ts" => ts}]} -> ts
      _ -> nil
    end
  end

end
