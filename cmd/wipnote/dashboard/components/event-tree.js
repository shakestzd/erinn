/* ── <hg-event-tree> Web Component ─────────────────────────── */

// File-extension → Prism/highlight.js language identifier. Hoisted to module
// scope so the lookup is a single map indexing instead of a long switch
// (drops _detectLanguage's cyclomatic complexity from ~38 to ~3).
const EXT_TO_LANGUAGE = {
  go: 'go',
  js: 'javascript', mjs: 'javascript',
  ts: 'typescript', tsx: 'typescript',
  jsx: 'jsx',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  c: 'c', h: 'c',
  cpp: 'cpp', cc: 'cpp', hpp: 'cpp',
  cs: 'csharp',
  sh: 'bash', bash: 'bash', zsh: 'bash', fish: 'bash',
  html: 'html', htm: 'html',
  css: 'css',
  scss: 'scss', sass: 'scss',
  json: 'json',
  yaml: 'yaml', yml: 'yaml',
  toml: 'toml',
  xml: 'xml',
  md: 'markdown', markdown: 'markdown',
  sql: 'sql',
  proto: 'protobuf',
  dockerfile: 'docker',
};

// Per-tool detail-key lists. _spanHasDetail returns true when ANY listed key
// is truthy on span.details — same semantics as the original if-chain, but
// expressed as a table + .some() so the cyclomatic complexity is constant in
// the number of tools instead of linear.
//
// The mcp__ prefix family, generic tool_input, and _precedingApi cases stay
// as explicit checks in the function (they aren't a fixed key list).
const TOOL_DETAIL_KEYS = {
  Bash: ['description', 'timeout', 'git_commit_id', 'full_command'],
  Read: ['file_path', 'offset', 'limit'],
  Edit: ['file_path', 'old_string_len', 'new_string_len', 'replace_all', 'old_string', 'new_string'],
  Write: ['file_path', 'content_len', 'content'],
  NotebookEdit: ['file_path'],
  Grep: ['pattern', 'path', 'output_mode'],
  Glob: ['pattern', 'path'],
  WebFetch: ['url', 'query'],
  Skill: ['skill_name'],
  TodoWrite: ['todo_count'],
};
const _taskAgentKeys = ['subagent_type', 'description', 'prompt'];
TOOL_DETAIL_KEYS.Task = _taskAgentKeys;
TOOL_DETAIL_KEYS.Agent = _taskAgentKeys;
TOOL_DETAIL_KEYS.WebSearch = TOOL_DETAIL_KEYS.WebFetch;
const _taskFamilyKeys = ['description', 'prompt', 'subagent_type'];
['TaskCreate', 'TaskUpdate', 'TaskList', 'TaskGet', 'TaskStop', 'TaskOutput'].forEach(function(t) {
  TOOL_DETAIL_KEYS[t] = _taskFamilyKeys;
});

// Per-tool summary-text builders. Each takes (d, self) and returns the short
// summary string shown on the span row. Multi-name groups (Read/Edit/Write,
// Grep/Glob, WebFetch/WebSearch, Task/Agent, TaskCreate-family) share a
// function reference. Collapses _spanSummary's tool_name if-chain into a
// table lookup (drops its cyclomatic complexity from ~54 below threshold).
const _summaryFile = function(d, self) { return self._relativizePath(d.file_path || ''); };
const _summaryBash = function(d) { return d.description || d.full_command || d.bash_command || ''; };
const _summaryPattern = function(d) { return d.pattern || ''; };
const _summaryUrl = function(d) { return d.url || ''; };
const _summarySkill = function(d) { return d.skill_name || ''; };
const _summaryDesc = function(d) { return d.description || ''; };
const _summaryTodo = function(d) { return d.todo_count ? d.todo_count + ' todos' : ''; };
const _summaryTaskFamily = function(d) { return d.description || d.subagent_type || ''; };
const TOOL_SUMMARY = {
  Bash: _summaryBash,
  Read: _summaryFile, Edit: _summaryFile, Write: _summaryFile, NotebookEdit: _summaryFile,
  Grep: _summaryPattern, Glob: _summaryPattern,
  WebFetch: _summaryUrl, WebSearch: _summaryUrl,
  Skill: _summarySkill,
  Task: _summaryDesc, Agent: _summaryDesc,
  TodoWrite: _summaryTodo,
  TaskCreate: _summaryTaskFamily, TaskUpdate: _summaryTaskFamily,
  TaskList: _summaryTaskFamily, TaskGet: _summaryTaskFamily,
  TaskStop: _summaryTaskFamily, TaskOutput: _summaryTaskFamily,
};

// Permission-gate decision_source → user-facing text. An unmapped value
// falls back to the explicit 'permission check' / 'decision: X' handling
// in _summaryBlocked below.
const BLOCKED_DECISION_TEXT = {
  config: 'auto-approved (config)',
  hook: 'auto-approved (hook)',
  user_permanent: 'user approved (remember)',
  user_temporary: 'user approved',
  user_reject: 'blocked by user',
  user_abort: 'aborted by user',
};

// Per-canonical summary builders. Mirror TOOL_SUMMARY but keyed on the
// span's canonical type (tool_blocked_on_user / tool_execution /
// interaction / api_request). Pulled out so _spanSummary itself stays
// under the complexity threshold.
const _summaryBlocked = function(d) {
  var ds = d.decision_source;
  var mapped = (typeof ds === 'string') ? BLOCKED_DECISION_TEXT[ds] : undefined;
  if (mapped) return mapped;
  if (!ds || ds === 'unknown') return 'permission check';
  return 'decision: ' + ds;
};
const _summaryApi = function(d) {
  var mode = d.mode || d.speed || '';
  return mode ? mode + ' mode' : '';
};
const CANONICAL_SUMMARY = {
  tool_blocked_on_user: _summaryBlocked,
  tool_execution: function() { return 'executed'; },
  interaction: function() { return ''; },
  api_request: _summaryApi,
};

// Per-tool kv-row builders for _spanDetailBlock. Each takes (d) and returns
// an array of [key, value] pairs that pass through the original truthy
// guards. The mcp__ family is special (prefix match + mcp_input iteration)
// and lives on the class as _mcpDetailRows. Multi-name groups share a
// function reference. Bodies are copied verbatim from the original chain to
// preserve behaviour byte-for-byte.
const _rowsBash = function(d) {
  var r = [];
  if (d.description)    r.push(['description', d.description]);
  if (d.timeout)        r.push(['timeout', d.timeout + 'ms']);
  if (d.git_commit_id)  r.push(['git commit', d.git_commit_id]);
  return r;
};
const _rowsRead = function(d) {
  var r = [];
  if (d.file_path)      r.push(['file', d.file_path]);
  if (d.offset || d.limit) {
    var start = d.offset || 1;
    var end = d.limit ? (start + d.limit - 1) : '';
    r.push(['range', end ? ('lines ' + start + '–' + end) : ('offset ' + start)]);
  }
  return r;
};
const _rowsEdit = function(d) {
  var r = [];
  if (d.file_path)      r.push(['file', d.file_path]);
  if (d.old_string_len || d.new_string_len) {
    r.push(['change', '−' + (d.old_string_len || 0) + ' → +' + (d.new_string_len || 0) + ' chars']);
  }
  if (d.replace_all)    r.push(['replace_all', 'true']);
  return r;
};
const _rowsWrite = function(d) {
  var r = [];
  if (d.file_path)      r.push(['file', d.file_path]);
  if (d.content_len)    r.push(['content', d.content_len + ' chars']);
  return r;
};
const _rowsNotebookEdit = function(d) { return d.file_path ? [['notebook', d.file_path]] : []; };
const _rowsGrep = function(d) {
  var r = [];
  if (d.pattern)        r.push(['pattern', d.pattern]);
  if (d.path)           r.push(['path', d.path]);
  if (d.output_mode)    r.push(['output', d.output_mode]);
  return r;
};
const _rowsGlob = function(d) {
  var r = [];
  if (d.pattern)        r.push(['pattern', d.pattern]);
  if (d.path)           r.push(['path', d.path]);
  return r;
};
const _rowsTaskOrAgent = function(d) {
  var r = [];
  if (d.subagent_type)  r.push(['subagent', d.subagent_type]);
  if (d.description)    r.push(['description', d.description]);
  if (d.prompt)         r.push(['prompt', d.prompt]);
  return r;
};
const _rowsWebFetch = function(d) { return d.url ? [['url', d.url]] : []; };
const _rowsWebSearch = function(d) { return d.query ? [['query', d.query]] : []; };
const _rowsSkill = function(d) { return d.skill_name ? [['skill', d.skill_name]] : []; };
const _rowsTodoWrite = function(d) { return d.todo_count ? [['todos', d.todo_count]] : []; };
const _rowsTaskFamily = function(d) {
  var r = [];
  if (d.description)    r.push(['description', d.description]);
  if (d.prompt)         r.push(['prompt', d.prompt]);
  if (d.subagent_type)  r.push(['subagent', d.subagent_type]);
  return r;
};
const TOOL_DETAIL_ROW_BUILDERS = {
  Bash: _rowsBash,
  Read: _rowsRead,
  Edit: _rowsEdit,
  Write: _rowsWrite,
  NotebookEdit: _rowsNotebookEdit,
  Grep: _rowsGrep,
  Glob: _rowsGlob,
  Task: _rowsTaskOrAgent, Agent: _rowsTaskOrAgent,
  WebFetch: _rowsWebFetch, WebSearch: _rowsWebSearch,
  Skill: _rowsSkill,
  TodoWrite: _rowsTodoWrite,
  TaskCreate: _rowsTaskFamily, TaskUpdate: _rowsTaskFamily, TaskList: _rowsTaskFamily,
  TaskGet: _rowsTaskFamily, TaskStop: _rowsTaskFamily, TaskOutput: _rowsTaskFamily,
};

// Per-tool code-block builders for _spanDetailBlock. Each takes (d, self) so
// the few that need _codeBlock/_detectLanguage can dispatch through `self`.
// The mcp__ and generic tool_input long-value cases reuse a single
// class-side helper (_longInputCodeBlocks) since they're byte-identical
// except for the source map.
const _codeBash = function(d, self) {
  return d.full_command
    ? self._codeBlock('command', d.full_command, d.full_command.length, false, 'bash')
    : '';
};
const _codeEdit = function(d, self) {
  var lang = self._detectLanguage(d.file_path);
  var out = '';
  if (d.old_string) out += self._codeBlock('old_string', d.old_string, d.old_string_len, d.content_truncated, lang);
  if (d.new_string) out += self._codeBlock('new_string', d.new_string, d.new_string_len, d.content_truncated, lang);
  return out;
};
const _codeWrite = function(d, self) {
  return d.content
    ? self._codeBlock('content', d.content, d.content_len, d.content_truncated, self._detectLanguage(d.file_path))
    : '';
};
const TOOL_CODE_BUILDERS = {
  Bash: _codeBash,
  Edit: _codeEdit,
  Write: _codeWrite,
};

class HgEventTree extends HTMLElement {
  constructor() {
    super();
    this.turns = [];
    this.featureTitles = {};
    this.expanded = new Set(JSON.parse(localStorage.getItem('hg-expanded') || '[]'));
    this._filterDebounce = null;
    // OTel data cache, keyed by session_id. Populated from /api/otel/prompts,
    // /api/otel/rollup, /api/otel/spans, and /api/otel/logs after turns load.
    // Absent when the receiver is disabled or no signals have arrived — rendering
    // degrades silently to the non-OTel path.
    this.otelPromptsBySession = {};
    this.otelRollupBySession = {};
    this.otelSpansBySession = {};
    this.otelLogsBySession = {};
    this.loadError = '';
  }

  connectedCallback() {
    // At the doorway landing page (root path, no /p/<id>/ prefix) the
    // server holds no per-project DB handles, so /api/events/* 404s.
    // Skip the load + SSE subscription entirely — the event tree only
    // belongs inside a per-project view.
    if (window.location.pathname.indexOf('/p/') !== 0) return;
    this.load();
    this.evtSource = new EventSource(buildProjectUrl('events/stream'));
    this.evtSource.onmessage = (msg) => this.handleSSE(JSON.parse(msg.data));
    this.evtSource.onopen = () => {
      var dot = document.getElementById('conn-dot');
      var label = document.getElementById('conn-label');
      if (dot) dot.className = 'conn-dot live';
      if (label) label.textContent = 'Live';
    };
    this.evtSource.onerror = () => {
      var dot = document.getElementById('conn-dot');
      var label = document.getElementById('conn-label');
      if (dot) dot.className = 'conn-dot dead';
      if (label) label.textContent = 'Disconnected';
      setTimeout(() => {
        if (this.evtSource && this.evtSource.readyState === EventSource.CONNECTING) {
          if (dot) dot.className = 'conn-dot retry';
          if (label) label.textContent = 'Reconnecting...';
        }
      }, 2000);
    };
    this._bindFilterListeners();
  }

  disconnectedCallback() {
    if (this.evtSource) this.evtSource.close();
  }

  _bindFilterListeners() {
    var textEl = document.getElementById('filter-text');
    var toolEl = document.getElementById('filter-tool');
    var agentEl = document.getElementById('filter-agent');
    if (textEl) {
      textEl.addEventListener('input', () => {
        clearTimeout(this._filterDebounce);
        this._filterDebounce = setTimeout(() => this.render(), 200);
      });
    }
    if (toolEl) toolEl.addEventListener('change', () => this.render());
    if (agentEl) agentEl.addEventListener('change', () => this.render());
  }

  async load() {
    var limit = this.dataset.limit || 50;
    this.innerHTML = '<div class="empty-state">Loading activity…</div>';
    try {
      var resp = await fetch(buildProjectUrl('events/tree', 'limit=' + limit));
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      this.loadError = '';
      this.turns = await resp.json();
    } catch(e) {
      this.loadError = 'Activity feed unavailable right now.';
      this.turns = [];
    }
    this.updateCount();
    this._populateDropdowns();
    this.render();
    await this.loadFeatureTitles();
    this.render();
    await this.loadOtelData();
    this.render();
  }

  // loadOtelData fetches per-prompt and per-session OTel aggregates for
  // every session currently in the turn list. One request per session.
  // Silently treats 404 / network errors as "no OTel data" — rendering
  // degrades to the non-OTel path without logging.
  async loadOtelData() {
    var sessionIds = new Set();
    this.turns.forEach(function(t) {
      if (t.user_query && t.user_query.session_id) {
        sessionIds.add(t.user_query.session_id);
      }
    });
    if (sessionIds.size === 0) return;

    var self = this;
    await Promise.all(Array.from(sessionIds).map(async function(sid) {
      try {
        var pResp = await fetch(buildProjectUrl('otel/prompts', 'session_id=' + encodeURIComponent(sid)));
        if (pResp.ok) {
          var body = await pResp.json();
          self.otelPromptsBySession[sid] = body.prompts || [];
        }
      } catch(_) {}
      try {
        var rResp = await fetch(buildProjectUrl('otel/rollup', 'session_id=' + encodeURIComponent(sid)));
        if (rResp.ok) {
          self.otelRollupBySession[sid] = await rResp.json();
        }
      } catch(_) {}
      try {
        var sResp = await fetch(buildProjectUrl('otel/spans', 'session_id=' + encodeURIComponent(sid)));
        if (sResp.ok) {
          var sBody = await sResp.json();
          self.otelSpansBySession[sid] = self._indexSpans(sBody.spans || []);
        }
      } catch(_) {}
      try {
        var lResp = await fetch(buildProjectUrl('otel/logs', 'session_id=' + encodeURIComponent(sid)));
        if (lResp.ok) {
          var lBody = await lResp.json();
          self.otelLogsBySession[sid] = lBody.logs || [];
        }
      } catch(_) {}
    }));
  }

  // _indexSpans groups spans by trace_id and builds parent→children lookup.
  //
  // Handles a real-life quirk: Claude Code emits the root `interaction`
  // span only at turn rollup, while child spans (api_request, tool_result,
  // tool.execution, tool.blocked_on_user) export continuously. For
  // in-flight sessions this leaves us with orphan children whose parent
  // span_id references a span we haven't received yet. Treating every
  // orphan as its own root explodes the tree horizontally.
  //
  // Fix: synthesize one placeholder parent per unique orphan parent_span,
  // flagged with is_pending=true so renderSpan can label it "pending
  // root — interaction span not yet received". All orphan children group
  // under their shared synthetic root, and the rendered tree looks right.
  _indexSpans(spans) {
    var byId = {};
    spans.forEach(function(s) { s.children = []; byId[s.span_id] = s; });

    // Identify orphan parents (parent_span non-empty but not in byId).
    var orphanParents = {};
    spans.forEach(function(s) {
      if (s.parent_span && !byId[s.parent_span]) {
        orphanParents[s.parent_span] = {
          span_id: s.parent_span,
          parent_span: '',
          trace_id: s.trace_id,
          canonical: 'interaction',
          native_name: 'claude_code.interaction (pending)',
          tool_name: '',
          model: '',
          ts_micros: s.ts_micros,
          duration_ms: 0,
          tokens_in: 0, tokens_out: 0, cost_usd: 0,
          decision: '',
          details: {},
          children: [],
          _pending: true,
        };
      }
    });
    // Merge synthetic parents into byId so the parent-link step below
    // sees them.
    Object.keys(orphanParents).forEach(function(id) {
      if (!byId[id]) byId[id] = orphanParents[id];
    });

    var roots = [];
    Object.values(byId).forEach(function(s) {
      if (s.parent_span && byId[s.parent_span]) {
        byId[s.parent_span].children.push(s);
      } else {
        roots.push(s);
      }
    });

    // Absorb each api_request span into the tool span that immediately
    // FOLLOWS it in its parent's child list. The api_request is the
    // LLM turn that decided to call that tool, so its model/cost/tokens
    // morally attribute to the tool call. The trailing api_request in a
    // turn (the final response) has no following tool and stays as its
    // own row. Done BEFORE reverse so "preceding" means chronologically
    // earlier.
    //
    // Guard for Task/Agent spans: when two subagents run concurrently, the
    // OTel receiver's re-attribution (strategy B: overlap window) is skipped
    // as "ambiguous", leaving each subagent's api_request spans parented to
    // the same orchestrator interaction span as the Task/Agent spans. If a
    // subagent's api_request appears chronologically just before an
    // orchestrator Task span, the naive sequential absorption would pair
    // the wrong api_request (subagent's model) with the Task row.
    //
    // Fix: before the absorption pass, identify the orchestrator model for
    // each parent span by looking for the model used in api_requests that
    // precede non-delegation (Bash/Read/Edit/…) tool spans — those api_requests
    // are definitively from the orchestrator. Then, when absorbing into a
    // Task/Agent span, skip if the candidate api_request's model doesn't match
    // the orchestrator model, and instead search backwards for the nearest
    // api_request with the right model.
    Object.values(byId).forEach(function(parent) {
      if (!parent.children || parent.children.length < 2) return;
      var kids = parent.children;

      // Determine the orchestrator model for this parent span. Scan all
      // consecutive (api_request, non-delegation tool) pairs — the first
      // one we find is definitively from the orchestrator.
      var orchModel = '';
      for (var k = 0; k < kids.length - 1; k++) {
        var kCur = kids[k], kNxt = kids[k + 1];
        if (kCur.canonical === 'api_request' && kCur.model && kNxt.tool_name &&
            kNxt.tool_name !== 'Task' && kNxt.tool_name !== 'Agent') {
          orchModel = kCur.model;
          break;
        }
      }

      for (var i = 0; i < kids.length - 1; i++) {
        var cur = kids[i], nxt = kids[i + 1];
        if (cur.canonical === 'api_request' && nxt.tool_name) {
          // For Task/Agent spans: guard against mis-attributed concurrent
          // subagent api_requests. When orchModel is known and the candidate
          // api_request has a different model, search backwards for the
          // nearest orchestrator api_request and use that instead.
          if ((nxt.tool_name === 'Task' || nxt.tool_name === 'Agent') &&
              orchModel && cur.model && cur.model !== orchModel) {
            var fallback = null;
            for (var j = i - 1; j >= 0; j--) {
              var cand = kids[j];
              if (cand.canonical === 'api_request' && cand.model === orchModel && !cand._absorbedInto) {
                fallback = cand;
                break;
              }
            }
            if (fallback) {
              nxt._precedingApi = fallback;
              fallback._absorbedInto = nxt.span_id;
            }
            // Leave the mis-attributed api_request un-absorbed — it will be
            // filtered from the rendered tree by filteredRootSpans (api_request
            // canonicals are not rendered as top-level rows).
            continue;
          }
          nxt._precedingApi = cur;
          cur._absorbedInto = nxt.span_id;
        }
      }
      // Drop absorbed api_requests from the child list in place.
      parent.children = kids.filter(function(c) { return !c._absorbedInto; });

      // Second pass: shared model reference for Task/Agent spans that
      // ended up with no _precedingApi. This happens when the orchestrator
      // dispatches N Tasks in a single LLM turn — ONE api_request precedes
      // all N Tasks, but exclusive-ownership absorption attaches it only to
      // Task #1. Tasks #2..N would otherwise render with no model pill.
      //
      // Fix: for each Task/Agent with no _precedingApi, walk backward to
      // find the nearest api_request whose model matches orchModel (or any
      // api_request when orchModel is unknown). Attach it as _modelRef —
      // a read-only reference used by the renderer for the model pill ONLY.
      // _modelRef is NOT used for cost/token accounting (the absorbing span
      // already counts those via _precedingApi; sharing would double-count).
      var updatedKids = parent.children;
      for (var m = 0; m < updatedKids.length; m++) {
        var mSpan = updatedKids[m];
        if ((mSpan.tool_name === 'Task' || mSpan.tool_name === 'Agent') && !mSpan._precedingApi) {
          // Walk the original kids array backward from the pre-absorption
          // position to find the nearest qualifying api_request. We search
          // all kids (including absorbed ones) so we can cross the absorption
          // boundary — i.e. find the api_request that was absorbed by Task #1.
          var modelRef = null;
          for (var n = kids.length - 1; n >= 0; n--) {
            var nCand = kids[n];
            if (nCand.canonical !== 'api_request') continue;
            if (orchModel && nCand.model && nCand.model !== orchModel) continue;
            if (!orchModel && !nCand.model) continue;
            modelRef = nCand;
            break;
          }
          if (modelRef) mSpan._modelRef = modelRef;
        }
      }
    });

    // Reverse every children array so the most recent span renders first,
    // matching the activity feed's overall "newest at top" ordering.
    // The endpoint returns spans in ascending ts_micros order; reversing
    // here keeps the server query simple (it's still chronologically
    // ordered, which helps span-to-span joining) while the UI surface
    // flips to reverse-chronological.
    Object.values(byId).forEach(function(s) {
      if (s.children.length > 1) s.children.reverse();
    });
    roots.reverse();

    var byTrace = {};
    roots.forEach(function(s) {
      (byTrace[s.trace_id] = byTrace[s.trace_id] || []).push(s);
    });
    return { byTrace: byTrace, roots: roots, byId: byId };
  }

  // _spansForTurn returns the root spans from the trace produced BY
  // this turn. A trace belongs to turn N when its earliest span timestamp
  // sits in the window [turn_N.ts, turn_N+1.ts). That's the causal
  // ordering: a tool call happens strictly AFTER the user prompt that
  // triggered it. Using absolute-time "nearest" is wrong because it can
  // pull a previous turn's trace (slightly earlier) instead of THIS
  // turn's in-flight trace (slightly later).
  //
  // Falls back to absolute-nearest only when no forward-window match
  // exists (handles pathological cases like mid-session resumes where
  // hook-timestamp alignment can drift).
  _spansForTurn(turn) {
    var uq = turn.user_query;
    if (!uq || !uq.session_id) return [];
    var idx = this.otelSpansBySession[uq.session_id];
    if (!idx || !idx.roots || idx.roots.length === 0) return [];
    if (!uq.timestamp) return idx.roots;
    var ts = Date.parse(uq.timestamp) * 1000;
    if (!ts) return idx.roots;

    // Find the next turn in THIS session (chronological next). tree.turns
    // is newest-first, so the "next" turn chronologically is the one with
    // a larger timestamp than ts within the same session.
    var sid = uq.session_id;
    var nextTs = Infinity;
    for (var i = 0; i < (this.turns||[]).length; i++) {
      var otherUQ = this.turns[i].user_query;
      if (!otherUQ || otherUQ.session_id !== sid) continue;
      if (!otherUQ.timestamp) continue;
      var otherTs = Date.parse(otherUQ.timestamp) * 1000;
      if (otherTs > ts && otherTs < nextTs) nextTs = otherTs;
    }

    // Cap the latest turn's window at ts + 15 minutes. Without a cap,
    // every span after the latest KNOWN turn gets attributed to it —
    // including spans from LATER turns we haven't loaded yet (SSE lag,
    // UserPromptSubmit hook failures, etc.). A 15-minute ceiling keeps
    // attribution conservative: live in-flight traces finish well
    // within that window, and stray later-turn spans are dropped rather
    // than misattributed.
    var latestTurnCap = 15 * 60 * 1_000_000; // 15 min in micros
    if (nextTs === Infinity) {
      nextTs = ts + latestTurnCap;
    }

    // Window match: smallest earliest_ts that falls in [ts - slop, nextTs).
    // 1-second slop on the lower bound absorbs harmless clock skew — OTel
    // exporters can start a span a few hundred ms BEFORE the hook-logged
    // user_query timestamp when they're batching aggressively. Any larger
    // slop risks stealing a previous turn's trailing trace.
    //
    // No absolute-nearest fallback. A turn without a forward-window
    // trace genuinely has no OTel data (pre-receiver session, or a turn
    // whose exporter dropped). Returning [] is the honest answer — the
    // renderer then falls through to hook-derived children.
    var slop = 1_000_000; // 1 s in micros
    var winner = null, winnerEarliest = Infinity;
    Object.keys(idx.byTrace).forEach(function(tid) {
      var earliest = Math.min.apply(null, idx.byTrace[tid].map(function(r) { return r.ts_micros; }));
      if (earliest >= (ts - slop) && earliest < nextTs && earliest < winnerEarliest) {
        winner = tid;
        winnerEarliest = earliest;
      }
    });
    if (!winner) return [];

    // Peel off the "interaction" wrapper (or synthetic pending root)
    // so the tool spans become the turn's direct children. The user
    // prompt IS the turn root — rendering "interaction" as an
    // intermediate row just repeats the same information at two depths.
    // Real tool calls (Bash, Edit, MCP, etc.) should attach straight
    // to the user query. Walk any interaction/pending roots one level
    // down; leave non-wrapper roots untouched.
    var roots = idx.byTrace[winner];
    var flat = [];
    roots.forEach(function(r) {
      var isWrapper = r._pending || r.canonical === 'interaction';
      if (isWrapper && r.children && r.children.length) {
        flat = flat.concat(r.children);
      } else {
        flat.push(r);
      }
    });
    return flat;
  }

  // _assistantTextsForTurn returns the assistant_text logs (from
  // /api/otel/logs) that belong to this turn. Uses two-hop matching:
  // turn (hook event_id) → user_prompt log (span_id) → assistant_text (parent_span).
  // Falls back to timestamp-window matching if user_prompt log is unavailable.
  _assistantTextsForTurn(turn) {
    var uq = turn.user_query;
    if (!uq || !uq.session_id) return [];
    var logs = this.otelLogsBySession[uq.session_id];
    if (!logs || logs.length === 0) return [];

    // Find the user_prompt log closest to this turn's timestamp (in the
    // same session). That log's span_id is the transcript-user-prompt-uuid,
    // which assistant_text.parent_span points at.
    var turnTs = uq.timestamp ? Date.parse(uq.timestamp) * 1000 : 0;
    if (!turnTs) {
      // Fallback: no timestamp available, cannot match
      return [];
    }

    var bestUP = null;
    var bestDelta = Infinity;
    var SLOP_MICROS = 60 * 1000 * 1000; // 60 seconds either side
    for (var i = 0; i < logs.length; i++) {
      var lg = logs[i];
      if (lg.canonical !== 'user_prompt') continue;
      var delta = Math.abs((lg.ts_micros || 0) - turnTs);
      if (delta < bestDelta && delta <= SLOP_MICROS) {
        bestDelta = delta;
        bestUP = lg;
      }
    }

    // Primary path: match by the user_prompt log's span_id.
    if (bestUP && bestUP.span_id) {
      var promptSpanId = bestUP.span_id;
      var primary = logs.filter(function(log) {
        return log.canonical === 'assistant_text' && log.parent_span === promptSpanId;
      });
      if (primary.length > 0) return primary;
      // fall through to timestamp-window fallback
    }

    // Fallback: timestamp-window match. For turns where no user_prompt
    // log exists yet (pre-backfill / pre-live-capture), attach any
    // assistant_text that falls in [turnTs, nextTurnTs) by session.
    // Compute nextTurnTs from the adjacent turn in this.turns.
    var nextTs = Infinity;
    for (var j = 0; j < this.turns.length; j++) {
      var other = this.turns[j].user_query;
      if (!other || other.session_id !== uq.session_id) continue;
      if (!other.timestamp) continue;
      var otherTs = Date.parse(other.timestamp) * 1000;
      if (otherTs > turnTs && otherTs < nextTs) nextTs = otherTs;
    }
    return logs.filter(function(log) {
      if (log.canonical !== 'assistant_text') return false;
      var ts = log.ts_micros || 0;
      return ts >= turnTs && ts < nextTs;
    });
  }

  // _otelForTurn returns the OTel prompt breakdown nearest (by wall-clock)
  // to the turn's user_query timestamp, or null if no OTel data exists for
  // this session. Matching by nearest-timestamp is a stand-in until hooks
  // and OTel events can be joined on a native prompt_id (later phase).
  _otelForTurn(turn) {
    var uq = turn.user_query;
    if (!uq || !uq.session_id) return null;
    var prompts = this.otelPromptsBySession[uq.session_id];
    if (!prompts || prompts.length === 0) return null;
    if (!uq.timestamp) return prompts[0];
    var ts = Date.parse(uq.timestamp) * 1000; // ms → micros
    if (!ts) return prompts[0];
    var best = null;
    var bestDiff = Infinity;
    for (var i = 0; i < prompts.length; i++) {
      var d = Math.abs((prompts[i].first_ts_micros || 0) - ts);
      if (d < bestDiff) { best = prompts[i]; bestDiff = d; }
    }
    return best;
  }

  // _otelBadges returns an HTML fragment with cost/token/retry badges
  // for a turn when OTel data is available; empty string otherwise.
  _otelBadges(turn) {
    var p = this._otelForTurn(turn);
    if (!p) return '';
    var parts = [];
    if (p.cost_usd > 0) parts.push('$' + p.cost_usd.toFixed(4));
    var totalTokens = (p.tokens_in || 0) + (p.tokens_out || 0) + (p.tokens_cache_read || 0) + (p.tokens_cache_creation || 0);
    if (totalTokens > 0) parts.push(this._fmtTokens(totalTokens) + ' tok');
    if (p.api_errors > 0) parts.push(p.api_errors + ' err');
    if (parts.length === 0) return '';
    return '<span class="badge badge-otel" title="From OTel api_request events">'
      + parts.map(esc).join(' · ')
      + '</span>';
  }

  _fmtTokens(n) {
    if (n < 1000) return '' + n;
    if (n < 1000000) return (n / 1000).toFixed(1) + 'k';
    return (n / 1000000).toFixed(2) + 'M';
  }

  // _turnStatsFromSpans walks the actual span subtree for a turn and
  // tallies the metrics that appear in the turn header (tool calls,
  // cost, tokens, api errors). Counting from the matched subtree is
  // inherently scoped to THIS turn — no risk of session-wide aggregates
  // leaking across turns, which is what the /api/otel/rollup data did.
  //
  // Returns { hasData: false } when the turn has no OTel spans; callers
  // fall back to hook-derived stats or no badge.
  _turnStatsFromSpans(turn) {
    var roots = this._spansForTurn(turn);
    if (!roots || roots.length === 0) return { hasData: false };
    var stats = { hasData: true, tool_calls: 0, api_errors: 0, cost: 0, tokens_in: 0, tokens_out: 0 };
    var self = this;
    var visit = function(span) {
      if (!span) return;
      // Tool calls: tool_result or subagent_invocation with a tool_name.
      if (span.tool_name && (span.canonical === 'tool_result' || span.canonical === 'subagent_invocation')) {
        stats.tool_calls++;
      }
      // API request spans carry cost/tokens directly.
      if (span.canonical === 'api_request') {
        stats.cost += span.cost_usd || 0;
        stats.tokens_in += span.tokens_in || 0;
        stats.tokens_out += span.tokens_out || 0;
      }
      // Absorbed api_request (attached to a tool span via _precedingApi
      // in _indexSpans) — those were pulled out of the rendered tree
      // but still contribute to the cost/token totals.
      if (span._precedingApi) {
        stats.cost += span._precedingApi.cost_usd || 0;
        stats.tokens_in += span._precedingApi.tokens_in || 0;
        stats.tokens_out += span._precedingApi.tokens_out || 0;
      }
      if (span.canonical === 'api_error' || span.success === false) {
        // rare: api_error rarely lands as a span, but guard both paths.
        if (span.canonical === 'api_error') stats.api_errors++;
      }
      (span.children || []).forEach(visit);
    };
    roots.forEach(visit);
    return stats;
  }

  // _turnCostBadge renders the per-turn cost/token/error badge from
  // stats computed by _turnStatsFromSpans. Same visual treatment as
  // _otelBadges so the per-turn and per-prompt paths look identical.
  _turnCostBadge(stats) {
    var parts = [];
    if (stats.cost > 0) parts.push('$' + stats.cost.toFixed(4));
    var tokens = (stats.tokens_in || 0) + (stats.tokens_out || 0);
    if (tokens > 0) parts.push(this._fmtTokens(tokens) + ' tok');
    if (stats.api_errors > 0) parts.push(stats.api_errors + ' err');
    if (parts.length === 0) return '';
    return '<span class="badge badge-otel" title="Computed from this turn\'s OTel span subtree">'
      + parts.map(esc).join(' · ')
      + '</span>';
  }

  async loadFeatureTitles() {
    var ids = new Set();
    this.turns.forEach(function collectIds(t) {
      if (t.user_query && t.user_query.feature_id) ids.add(t.user_query.feature_id);
      (t.children || []).forEach(function walk(c) {
        if (c.feature_id) ids.add(c.feature_id);
        (c.children || []).forEach(walk);
      });
    });
    if (ids.size === 0) return;
    try {
      var resp = await fetch(buildProjectUrl('features'));
      if (!resp.ok) return;
      var features = await resp.json();
      var self = this;
      features.forEach(function(f) { if (ids.has(f.id)) self.featureTitles[f.id] = f.title; });
    } catch(e) { /* non-fatal */ }
  }

  _collectFromChildren(children, tools, agents) {
    (children || []).forEach((c) => {
      if (c.tool_name) tools.add(c.tool_name);
      if (c.agent_id && c.agent_id !== 'claude-code') agents.add(c.agent_id);
      this._collectFromChildren(c.children, tools, agents);
    });
  }

  _populateDropdowns() {
    var tools = new Set();
    var agents = new Set();
    this.turns.forEach((t) => {
      this._collectFromChildren(t.children, tools, agents);
    });

    var toolEl = document.getElementById('filter-tool');
    var agentEl = document.getElementById('filter-agent');
    if (toolEl) {
      var prevTool = toolEl.value;
      toolEl.innerHTML = '<option value="">All Tools</option>';
      Array.from(tools).sort().forEach(function(t) {
        var opt = document.createElement('option');
        opt.value = t;
        opt.textContent = t;
        if (t === prevTool) opt.selected = true;
        toolEl.appendChild(opt);
      });
    }
    if (agentEl) {
      var prevAgent = agentEl.value;
      agentEl.innerHTML = '<option value="">All Agents</option>';
      Array.from(agents).sort().forEach(function(a) {
        var opt = document.createElement('option');
        opt.value = a;
        opt.textContent = a;
        if (a === prevAgent) opt.selected = true;
        agentEl.appendChild(opt);
      });
    }
  }

  getFilterValues() {
    var textEl = document.getElementById('filter-text');
    var toolEl = document.getElementById('filter-tool');
    var agentEl = document.getElementById('filter-agent');
    return {
      text: textEl ? textEl.value.trim().toLowerCase() : '',
      tool: toolEl ? toolEl.value : '',
      agent: agentEl ? agentEl.value : ''
    };
  }

  _turnMatchesFilters(turn, filters) {
    if (!filters.text && !filters.tool && !filters.agent) return true;

    var uq = turn.user_query || {};
    if (filters.text) {
      var summary = (uq.input_summary || '').toLowerCase();
      if (!this._childrenContainText(turn.children, filters.text) && !summary.includes(filters.text)) {
        return false;
      }
    }
    if (filters.tool && !this._childrenContainTool(turn.children, filters.tool)) {
      return false;
    }
    if (filters.agent && !this._childrenContainAgent(turn.children, filters.agent)) {
      return false;
    }
    return true;
  }

  _childrenContainText(children, text) {
    return (children || []).some((c) => {
      var s = ((c.input_summary || '') + ' ' + (c.output_summary || '') + ' ' + (c.tool_name || '')).toLowerCase();
      if (s.includes(text)) return true;
      return this._childrenContainText(c.children, text);
    });
  }

  _childrenContainTool(children, tool) {
    return (children || []).some((c) => {
      if (c.tool_name === tool) return true;
      return this._childrenContainTool(c.children, tool);
    });
  }

  _childrenContainAgent(children, agent) {
    return (children || []).some((c) => {
      if (c.agent_id === agent) return true;
      return this._childrenContainAgent(c.children, agent);
    });
  }

  parseBadgeCategory(title) {
    var prefixes = {
      'Dashboard:': 'badge-dashboard',
      'Fix:': 'badge-fix',
      'Plan ': 'badge-plan',
      'Plan:': 'badge-plan',
      'CLI:': 'badge-cli',
      'Refactor:': 'badge-refactor',
      'Test:': 'badge-test'
    };
    for (var prefix in prefixes) {
      if (title.startsWith(prefix)) {
        return {
          text: title.substring(prefix.length).trim(),
          className: prefixes[prefix]
        };
      }
    }
    return { text: title, className: 'badge-feature' };
  }

  agentBadgeColor(agentType) {
    var type = (agentType || '').toLowerCase();
    if (type.indexOf('researcher') !== -1) return '#06b6d4'; // cyan
    if (type.indexOf('haiku') !== -1) return '#22c55e';      // green
    if (type.indexOf('sonnet') !== -1) return '#3b82f6';     // blue
    if (type.indexOf('opus') !== -1) return '#a855f7';       // purple
    if (type.indexOf('test-runner') !== -1) return '#eab308'; // yellow
    return '#d29922'; // default gold
  }

  featureBadge(featureId, featureTitle) {
    if (!featureId) return '';
    var title = featureTitle || this.featureTitles[featureId] || '';
    // Treat degenerate "title == id" rows as missing — fall back to a
    // short hash label instead of uppercasing the full ID. This happens
    // when the feature was created without a title or the title field
    // was never populated (tracked in a separate data-side bug).
    if (title && title.toLowerCase() === featureId.toLowerCase()) {
      title = '';
    }
    var parsed = this.parseBadgeCategory(title);
    var shortId = featureId.replace(/^(feat|bug|spk|plan|trk)-/, '').substring(0, 6);
    var label = parsed.text
      ? (parsed.text.length > 25 ? parsed.text.substring(0, 22) + '...' : parsed.text)
      : 'untitled ' + shortId;
    return '<span class="badge ' + parsed.className + '" title="' + esc(featureId) + '">' + esc(label) + '</span>';
  }

  updateCount() {
    var countEl = document.getElementById('activity-count');
    if (countEl) countEl.textContent = this.turns.length;
  }

  saveExpanded() {
    localStorage.setItem('hg-expanded', JSON.stringify([...this.expanded]));
  }

  toggle(eventId) {
    if (this.expanded.has(eventId)) {
      this.expanded.delete(eventId);
      // Record explicit collapse so auto-expand-newest-turn doesn't
      // immediately re-expand on the next render. Limited to the most
      // recent 10 collapses — older entries age out automatically.
      var collapsed = JSON.parse(localStorage.getItem('hg-collapsed') || '[]');
      collapsed = collapsed.filter(function(id) { return id !== eventId; });
      collapsed.unshift(eventId);
      if (collapsed.length > 10) collapsed = collapsed.slice(0, 10);
      localStorage.setItem('hg-collapsed', JSON.stringify(collapsed));
    } else {
      this.expanded.add(eventId);
      // Clear from collapsed list on re-expansion so auto-expand can
      // reclaim it later if it becomes the top turn again.
      var collapsed2 = JSON.parse(localStorage.getItem('hg-collapsed') || '[]');
      collapsed2 = collapsed2.filter(function(id) { return id !== eventId; });
      localStorage.setItem('hg-collapsed', JSON.stringify(collapsed2));
    }
    this.saveExpanded();
    this.render();
  }

  handleSSE(data) {
    if (stats.total_events != null) {
      stats.total_events++;
      setVal('sv-events', stats.total_events);
    }
    if (this._reloadTimer) clearTimeout(this._reloadTimer);
    this._reloadTimer = setTimeout(() => this.load(), 500);
  }

  render() {
    if (this.loadError) {
      this.innerHTML = '<div class="empty-state">' + this.loadError + '</div>';
      this._updateFilterCount(0, 0);
      return;
    }
    if (!this.turns || this.turns.length === 0) {
      this.innerHTML = '<div class="empty-state">No activity yet.</div>';
      this._updateFilterCount(0, 0);
      return;
    }

    // Auto-expand the newest turn so live activity is visible without
    // a manual click. The user's explicit collapse wins — if they've
    // toggled off the top turn, we respect that via hg-collapsed. Older
    // turns stay user-controlled via the existing localStorage-backed
    // expand set.
    var topTurn = this.turns[0];
    if (topTurn && topTurn.user_query && topTurn.user_query.event_id) {
      var collapsed = JSON.parse(localStorage.getItem('hg-collapsed') || '[]');
      if (collapsed.indexOf(topTurn.user_query.event_id) === -1) {
        this.expanded.add(topTurn.user_query.event_id);
      }
    }

    var filters = this.getFilterValues();
    var filtered = this.turns.filter((t) => this._turnMatchesFilters(t, filters));
    this._updateFilterCount(filtered.length, this.turns.length);
    this.innerHTML = filtered.map(t => this.renderTurn(t)).join('');

    // Syntax-highlight any newly-injected <code class="language-xxx">
    // blocks. Prism.highlightAllUnder walks the subtree and tokenizes
    // each element that hasn't been highlighted yet. Silent no-op when
    // Prism isn't loaded (e.g. offline, CDN unreachable) — code still
    // renders as plain monospace.
    if (typeof Prism !== 'undefined' && Prism.highlightAllUnder) {
      try { Prism.highlightAllUnder(this); } catch (_) {}
    }
  }

  _updateFilterCount(shown, total) {
    var countEl = document.getElementById('filter-count');
    if (!countEl) return;
    countEl.textContent = (shown < total) ? shown + ' of ' + total : '';
  }

  renderTurn(turn) {
    var uq = turn.user_query;
    var isExp = this.expanded.has(uq.event_id);
    // A turn has children when EITHER hook events OR an OTel trace
    // exists. Previously we only checked turn.children (hook-derived),
    // so turns with only OTel data rendered chevron-less and couldn't
    // be collapsed — making the tree hard to navigate.
    var hasHookChildren = turn.children && turn.children.length > 0;
    var hasSpans = this._spansForTurn(turn).length > 0;
    var hasChildren = hasHookChildren || hasSpans;
    var expandIcon = hasChildren
      ? '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(uq.event_id) + '">\u25B6</span>'
      : '<span class="expand-icon-spacer"></span>';

    // Per-turn stats: prefer counts derived from the OTel span subtree
    // (spans attributed to THIS specific turn via the forward-window
    // matcher) over hook-derived turn.stats. The hook counts are stale
    // when UserPromptSubmit drops or when the receiver + hook clocks
    // are out of sync; _otelBadges likewise lookup per-prompt data via
    // nearest-match which can double-count across turns. Counting from
    // the actual matched subtree is the single source of truth.
    var turnStats = this._turnStatsFromSpans(turn);
    var s = turn.stats || {};
    var toolCount, errorCount;
    if (turnStats.hasData) {
      toolCount = turnStats.tool_calls;
      errorCount = turnStats.api_errors;
    } else {
      toolCount = s.tool_count || 0;
      errorCount = s.error_count || 0;
    }
    var statsHtml = '<span class="turn-stats">' + toolCount + ' tools' + (errorCount ? ', ' + errorCount + ' errors' : '') + '</span>';
    var featureBdg = this.featureBadge(uq.feature_id, uq.feature_title);
    // Cost / token badge: sum from the span subtree when it exists,
    // otherwise fall back to the prompt-endpoint nearest-match
    // (_otelBadges), otherwise empty.
    var otelBdg = turnStats.hasData
      ? this._turnCostBadge(turnStats)
      : this._otelBadges(turn);

    var html = '<div class="turn-group">'
      + '<div class="event-row depth-0 user-query-row accent-user"'
      + ' data-event-id="' + esc(uq.event_id) + '"'
      + ' data-timestamp="' + esc(uq.timestamp || '') + '">'
      + expandIcon
      + this._cliBadgeMarkupForEvent(uq)
      + '<span class="event-time">' + formatTime(uq.timestamp) + '</span>'
      + '<span class="event-summary">' + esc(uq.input_summary || '') + '</span>'
      + featureBdg
      + otelBdg
      + statsHtml
      + '</div>';

    if (isExp) {
      // Prefer OTel spans when present — they're the canonical source
      // of hierarchy (subagent tool calls nest natively, no custom
      // attribution logic needed). When a turn has no OTel data — e.g.
      // a pre-OTel session or a session without the receiver enabled —
      // fall back to hook-derived children so older content still
      // renders.
      var rootSpans = this._spansForTurn(turn);
      // Filter out top-level standalone api_request spans. The turn
      // header already shows per-turn cost/tokens via _turnCostBadge, so
      // the "api_request" footer row is redundant at the top level.
      // api_request spans nested INSIDE tool spans are handled by the
      // _precedingApi/_absorbedInto logic in _indexSpans and are NOT
      // affected here — this filter is scoped to the turn's direct roots.
      var filteredRootSpans = (rootSpans || []).filter(function(s) {
        return s.canonical !== 'api_request' && s.tool_name !== 'api_request';
      });
      // Determine the turn's model for badge dedup in child spans.
      // Walk the (unfiltered) root spans to find the first api_request
      // that carries a model string — that's the turn-level model.
      var turnModel = null;
      (rootSpans || []).forEach(function findModel(s) {
        if (turnModel) return;
        if (s.canonical === 'api_request' && s.model) { turnModel = s.model; return; }
        if (s._precedingApi && s._precedingApi.model) { turnModel = s._precedingApi.model; return; }
        (s.children || []).forEach(findModel);
      });
      var turnModelShort = turnModel ? this._shortModelName(turnModel) : null;
      // Post-pass: bundle consecutive same-server MCP spans into synthetic
      // group wrappers so repetitive MCP runs collapse to a single row.
      var groupedRootSpans = this._groupConsecutiveMcp(filteredRootSpans);

      var timelineItems = [];
      this._assistantTextsForTurn(turn).forEach(function(log) {
        timelineItems.push({
          kind: 'assistant',
          ts: log.ts_micros || 0,
          render: () => this.renderAssistantText(log, uq.feature_id),
        });
      }, this);

      if (groupedRootSpans.length > 0) {
        groupedRootSpans.forEach(function(span) {
          timelineItems.push({
            kind: 'span',
            ts: this._spanTimelineMicros(span),
            render: () => this.renderSpan(span, 1, 0, uq.feature_id, turnModelShort),
          });
        }, this);
      } else if (turn.children) {
        turn.children.forEach(function(child) {
          var isStop = child.tool_name === 'Stop' && child.event_type === 'end';
          if (isStop) return; // handled below
          timelineItems.push({
            kind: 'event',
            ts: this._eventTimelineMicros(child),
            render: () => this.renderEvent(child, 1),
          });
        }, this);
      }

      // Stop fallback: show when no assistant_text captured, regardless of whether OTel spans exist.
      if (!timelineItems.some(function(item) { return item.kind === 'assistant'; }) && turn.children) {
        var stopEvt = null;
        for (var sci = 0; sci < turn.children.length; sci++) {
          var sc = turn.children[sci];
          if (sc.tool_name === 'Stop' && sc.event_type === 'end') { stopEvt = sc; break; }
        }
        if (stopEvt) {
          timelineItems.push({
            kind: 'stop',
            ts: this._eventTimelineMicros(stopEvt),
            render: () => this.renderStopFallback(stopEvt, uq.feature_id),
          });
        }
      }

      timelineItems.sort(function(a, b) {
        if (a.ts === b.ts) {
          if (a.kind === 'assistant' && b.kind !== 'assistant') return -1;
          if (b.kind === 'assistant' && a.kind !== 'assistant') return 1;
          return 0;
        }
        return b.ts - a.ts;
      });
      html += timelineItems.map(function(item) { return item.render(); }).join('');
    }

    html += '</div>';
    return html;
  }

  _spanTimelineMicros(span) {
    if (!span) return 0;
    if (span.start_ts_micros) return span.start_ts_micros;
    if (span.ts_micros) return span.ts_micros;
    if (span.children && span.children.length) {
      var childTimes = span.children.map(s => this._spanTimelineMicros(s)).filter(Boolean);
      if (childTimes.length) return Math.max.apply(null, childTimes);
    }
    return 0;
  }

  _eventTimelineMicros(evt) {
    if (!evt || !evt.timestamp) return 0;
    var ms = Date.parse(evt.timestamp);
    return Number.isFinite(ms) ? ms * 1000 : 0;
  }

  // renderAssistantText renders a single assistant_text log as a depth-1
  // row showing the assistant's text response. Collapsed by default, expandable.
  // When expanded, applies markdown formatting if marked.js is available.
  renderAssistantText(log, parentFeatureId) {
    var logId = log.signal_id || 'atxt-' + Math.random().toString(36).slice(2);
    var isExp = this.expanded.has(logId);

    // Parse attrs_json to extract text, stop_reason, etc.
    var attrs = {};
    try {
      attrs = JSON.parse(log.attrs_json || '{}');
    } catch(_) {}

    var text = attrs.text || '';
    var stopReason = attrs.stop_reason || 'end_turn';
    var previewLen = 180;
    var preview = text.length > previewLen
      ? text.substring(0, previewLen) + '…'
      : text;

    var expandIcon = '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(logId) + '">▶</span>';

    var stopReasonBadge = '';
    if (stopReason && stopReason !== 'end_turn') {
      stopReasonBadge = '<span class="stop-reason-label">' + esc(stopReason) + '</span>';
    }

    var featureBdg = this.featureBadge(log.feature_id || '', log.feature_title || '');

    var html = '<div class="event-row depth-1 assistant-text-row accent-system"'
      + ' data-event-id="' + esc(logId) + '"'
      + ' data-timestamp="' + esc((attrs.timestamp || log.ts_micros || 0)) + '"'
      + ' style="padding-left: 2.5rem">'
      + expandIcon
      + '<span class="assistant-label">assistant</span>'
      + '<span class="event-summary assistant-preview">' + esc(preview) + '</span>'
      + stopReasonBadge
      + featureBdg
      + '</div>';

    if (isExp) {
      // Configure marked on first use
      if (window.marked && !window._markedConfigured) {
        window.marked.setOptions({ breaks: true, gfm: true });
        window._markedConfigured = true;
      }

      var body;
      if (window.marked && typeof window.marked.parse === 'function') {
        // Parse markdown and wrap in a safe div
        var markedHtml = window.marked.parse(text);
        // Sanitize if DOMPurify is available, otherwise use as-is (marked is already safe by default)
        if (window.DOMPurify && typeof window.DOMPurify.sanitize === 'function') {
          markedHtml = window.DOMPurify.sanitize(markedHtml);
        }
        body = '<div class="assistant-text-body markdown">' + markedHtml + '</div>';
      } else {
        // Fallback to plain pre if marked is not available
        body = '<pre class="assistant-text-pre" style="white-space: pre-wrap; word-wrap: break-word; max-width: 80ch; font-size: 0.9em; line-height: 1.4; margin: 0;">'
          + esc(text)
          + '</pre>';
      }

      html += '<div class="event-row depth-2 assistant-text-detail accent-system"'
        + ' style="padding-left: 3.75rem; padding-top: 0.5rem; padding-bottom: 0.5rem;">'
        + body
        + '</div>';
    }

    return html;
  }

  // renderStopFallback renders an agent_events Stop row as an assistant-text
  // block when no OTel-derived assistant_text log exists for the turn.
  // Mirrors renderAssistantText — same quiet-label, same preview, same
  // expand/collapse with markdown body. Strips the "Agent stopped: " prefix
  // from input_summary and shows the stop_reason quietly when non-default.
  renderStopFallback(evt, parentFeatureId) {
    var evtId = evt.event_id || 'stop-' + Math.random().toString(36).slice(2);
    var isExp = this.expanded.has(evtId);

    var rawSummary = evt.input_summary || '';
    var prefix = 'Agent stopped: ';
    var text = rawSummary.startsWith(prefix) ? rawSummary.slice(prefix.length) : rawSummary;

    var stopReason = evt.stop_reason || '';
    var previewLen = 180;
    var preview = text.length > previewLen ? text.substring(0, previewLen) + '…' : text;

    var expandIcon = '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(evtId) + '">▶</span>';

    var stopReasonBadge = '';
    if (stopReason && stopReason !== 'end_turn') {
      stopReasonBadge = '<span class="stop-reason-label">' + esc(stopReason) + '</span>';
    }

    var featureBdg = this.featureBadge(evt.feature_id || '', evt.feature_title || '');

    var html = '<div class="event-row depth-1 assistant-text-row accent-system"'
      + ' data-event-id="' + esc(evtId) + '"'
      + ' data-timestamp="' + esc(evt.timestamp || '') + '"'
      + ' style="padding-left: 2.5rem">'
      + expandIcon
      + '<span class="assistant-label">assistant</span>'
      + '<span class="event-summary assistant-preview">' + esc(preview) + '</span>'
      + stopReasonBadge
      + featureBdg
      + '</div>';

    if (isExp) {
      if (window.marked && !window._markedConfigured) {
        window.marked.setOptions({ breaks: true, gfm: true });
        window._markedConfigured = true;
      }

      var body;
      if (window.marked && typeof window.marked.parse === 'function') {
        var markedHtml = window.marked.parse(text);
        if (window.DOMPurify && typeof window.DOMPurify.sanitize === 'function') {
          markedHtml = window.DOMPurify.sanitize(markedHtml);
        }
        body = '<div class="assistant-text-body markdown">' + markedHtml + '</div>';
      } else {
        body = '<pre class="assistant-text-pre" style="white-space: pre-wrap; word-wrap: break-word; max-width: 80ch; font-size: 0.9em; line-height: 1.4; margin: 0;">'
          + esc(text)
          + '</pre>';
      }

      html += '<div class="event-row depth-2 assistant-text-detail accent-system"'
        + ' style="padding-left: 3.75rem; padding-top: 0.5rem; padding-bottom: 0.5rem;">'
        + body
        + '</div>';
    }

    return html;
  }

  renderEvent(evt, depth) {
    if (depth > 3) return '';
    var hasChildren = evt.children && evt.children.length > 0;
    var isExp = this.expanded.has(evt.event_id);
    var expandIcon = hasChildren
      ? '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(evt.event_id) + '">\u25B6</span>'
      : '<span class="expand-icon-spacer"></span>';

    var isTask = evt.tool_name === 'Task' || evt.tool_name === 'Agent' || evt.event_type === 'task_delegation';
    var isError = evt.event_type === 'error' || evt.status === 'failed';
    var borderClass = isTask ? 'border-task' : isError ? 'border-error' : '';

    var subagentBadge = '';
    if (isTask && evt.subagent_type) {
      var color = this.agentBadgeColor(evt.subagent_type);
      subagentBadge = '<span class="badge badge-subagent" style="background-color: ' + color + '">' + esc(evt.subagent_type) + '</span>';
    }
    var statusBdg = '<span class="badge badge-status-' + (evt.status || 'unknown') + '">' + esc(evt.status || 'unknown') + '</span>';

    var padLeft = (depth + 1) * 1.25;
    var bgAlpha = 0.05 + depth * 0.08;

    var accentClass = this._rowAccentClass(evt);
    var html = '<div class="event-row depth-' + depth + ' ' + borderClass + ' ' + accentClass + ' clickable-row"'
      + ' data-event-id="' + esc(evt.event_id) + '"'
      + (evt.session_id ? ' data-session="' + esc(evt.session_id) + '"' : '')
      + ' data-tool-use-id="' + esc(evt.tool_use_id || '') + '"'
      + ' data-tool-name="' + esc(evt.tool_name || '') + '"'
      + (evt.agent_id ? ' data-agent="' + esc(evt.agent_id) + '"' : '')
      + ' data-timestamp="' + esc(evt.timestamp || '') + '"'
      + ' style="padding-left: ' + padLeft + 'rem; background: rgba(0,0,0,' + bgAlpha + ')">'
      + expandIcon
      + '<span class="event-time">' + formatTime(evt.timestamp) + '</span>'
      + '<span class="tool-chip tool-' + esc(evt.tool_name) + '">' + esc(evt.tool_name) + toolChipRange(evt) + '</span>'
      + subagentBadge
      + '<span class="event-summary">' + esc(evt.input_summary || evt.output_summary || '') + '</span>'
      + this.featureBadge(evt.feature_id, evt.feature_title)
      + statusBdg
      + '</div>';

    if (evt.children && evt.children.length > 0) {
      var childVis = isExp ? '' : ' collapsed';
      html += '<div class="event-children' + childVis + '" data-parent="' + esc(evt.event_id) + '">'
        + evt.children.map(c => this.renderEvent(c, depth + 1)).join('')
        + '</div>';
    }
    return html;
  }

  _rowAccentClass(evt) {
    if (!evt) return 'accent-system';
    var tool = (evt.tool_name || '').toLowerCase();
    if (tool === 'userquery') return 'accent-user';
    var agent = (evt.agent_id || '').toLowerCase();
    if (agent.indexOf('claude') !== -1) return 'accent-claude';
    if (agent.indexOf('codex') !== -1) return 'accent-codex';
    if (agent.indexOf('gemini') !== -1) return 'accent-gemini';
    if (agent === 'human' || agent === 'user') return 'accent-user';
    return 'accent-system';
  }

  _cliSourceFromAccent(accentClass) {
    if (accentClass === 'accent-claude') return 'claude';
    if (accentClass === 'accent-codex') return 'codex';
    if (accentClass === 'accent-gemini') return 'gemini';
    return '';
  }

  _cliSourceForEvent(evt) {
    var agent = ((evt && evt.agent_id) || '').toLowerCase();
    if (agent.indexOf('openai') !== -1) return 'openai';
    if (agent.indexOf('claude') !== -1) return 'claude';
    if (agent.indexOf('codex') !== -1) return 'codex';
    if (agent.indexOf('gemini') !== -1) return 'gemini';
    return this._cliSourceFromAccent(this._rowAccentClass(evt));
  }

  _cliBadgeMarkup(source, label) {
    if (!source) return '';
    var titles = {
      claude: 'Claude Code',
      codex: 'Codex',
      openai: 'OpenAI',
      gemini: 'Gemini'
    };
    var classes = {
      claude: 'cli-logo cli-logo-claude-code',
      codex: 'cli-logo cli-logo-codex',
      openai: 'cli-logo cli-logo-openai',
      gemini: 'cli-logo cli-logo-gemini'
    };
    var glyphs = {
      claude: '<path d="M17.3041 3.541h-3.6718l6.696 16.918H24Zm-10.6082 0L0 20.459h3.7442l1.3693-3.5527h7.0052l1.3693 3.5528h3.7442L10.5363 3.5409Zm-.3712 10.2232 2.2914-5.9456 2.2914 5.9456Z"/>',
      codex: '<path d="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z"/>',
      openai: '<path d="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z"/>',
      gemini: '<path d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81"/>'
    };
    var title = titles[source] || label;
    return '<span class="' + classes[source] + '" role="img" aria-label="' + esc(title) + '" title="' + esc(title) + '">'
      + '<svg class="cli-logo-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">'
      + glyphs[source]
      + '</svg>'
      + '</span>';
  }

  _cliBadgeMarkupForEvent(evt) {
    var source = this._cliSourceForEvent(evt);
    if (!source) return '';
    var label = source === 'claude' ? 'Claude' : source === 'codex' ? 'Codex' : source === 'openai' ? 'OpenAI' : 'Gemini';
    return this._cliBadgeMarkup(source, label);
  }

  _spanAccentClass(span) {
    var nativeName = ((span && span.native_name) || '').toLowerCase();
    if (nativeName.indexOf('claude_code') === 0) return 'accent-claude';
    if (nativeName.indexOf('codex') === 0 || nativeName.indexOf('gen_ai') === 0 ||
        nativeName.indexOf('mcp.tools') === 0) return 'accent-codex';
    if (nativeName.indexOf('gemini') === 0) return 'accent-gemini';
    return 'accent-system';
  }

  // _relativizePath strips a project root prefix from an absolute file
  // path so spans show a compact relative path instead of the full
  // filesystem location. The full path is preserved in the row's title
  // attribute as a hover tooltip.
  //
  // Strategy (in order):
  //   1. Strip window.wipnoteProjectRoot if set.
  //   2. Find the first occurrence of a common repo-marker segment
  //      (/cmd/, /internal/, /plugin/, /packages/, /scripts/, /.wipnote/)
  //      and trim everything up to (but not including) that segment.
  //   3. Return unchanged when the path is not absolute or no marker matches.
  _relativizePath(path) {
    if (!path || path.charAt(0) !== '/') return path;
    // Strategy 1: explicit project root (set from /api/mode projectRoot).
    var root = window.wipnoteProjectRoot;
    if (root) {
      if (path.startsWith(root + '/')) return path.slice(root.length + 1);
      if (path === root) return '.';
    }
    // Strategy 2: repo-marker heuristic.
    var markers = ['/cmd/', '/internal/', '/plugin/', '/packages/', '/scripts/', '/.wipnote/'];
    for (var i = 0; i < markers.length; i++) {
      var idx = path.indexOf(markers[i]);
      if (idx !== -1) return path.slice(idx + 1); // keep the marker segment itself
    }
    return path;
  }

  // _spanHasDetail returns true when _spanDetailBlock would produce
  // non-empty output for this span. Used by renderSpan to decide whether
  // to show the expand chevron.
  _spanHasDetail(span) {
    if (!span.tool_name) return false;
    var d = span.details || {};
    var tn = span.tool_name;
    var keys = TOOL_DETAIL_KEYS[tn];
    if (keys) return keys.some(function(k) { return !!d[k]; });
    if (tn.indexOf('mcp__') === 0) {
      return !!(d.mcp_input || d.url || d.query || d.pattern || d.file_path);
    }
    if (d.tool_input) return true;
    // Also check for absorbed api_request details.
    if (span._precedingApi) return true;
    return false;
  }

  // renderSpan emits a tree row for an OTel span and recursively renders
  // its children. Uses the same tool-chip classes as hook-sourced rows
  // (tool-Bash/Read/Edit/etc.) so Bash is green, Read is blue, Agent is
  // pink regardless of where the row came from. The "trace" chip appears
  // only on trace roots — every descendant already inherits the blue
  // left-border + tinted background, which is enough provenance.
  //
  // Summary text prefers tool-specific detail (bash full_command, Read
  // file_path, Grep pattern, Agent subagent_type) over the native span
  // name. This mirrors how hook rows show input_summary rather than
  // "tool_call".
  //
  // Span IDs are used as toggle keys, namespaced with "span:" so they
  // don't collide with event_id-keyed toggles from the hook-derived tree.
  //
  // parentFeatureId — feature_id shown on the parent turn row; when this
  // span's feature_id matches, we suppress the badge (dedup). Same logic
  // for parentModel vs. the resolved model badge string.
  renderSpan(span, depth, subagentDepth = 0, parentFeatureId = null, parentModel = null) {
    if (depth > 5) return '';
    if (!span) return '';

    // Synthetic MCP group: render a summary row collapsing a consecutive run
    // of same-server MCP tool calls. Collapsed by default; expand reveals
    // the real child spans each rendered via the normal renderSpan path.
    if (span.synthetic === 'mcp_group') {
      var groupToggleKey = 'span:' + span.span_id;
      var collapsed = JSON.parse(localStorage.getItem('hg-collapsed') || '[]');
      var isUserExpanded = this.expanded.has(groupToggleKey);
      // mcp_group rows default to collapsed (the opposite of subagent spans).
      var isUserCollapsedGroup = collapsed.indexOf(groupToggleKey) !== -1;
      var isExpGroup = isUserExpanded && !isUserCollapsedGroup;
      var effectiveDepthGroup = depth + subagentDepth * 2;
      var padLeftGroup = (effectiveDepthGroup + 1) * 1.25;
      var bgAlphaGroup = 0.05 + depth * 0.08;
      var serverColor = this._mcpServerColor(span.mcp_server);
      var durGroup = span.duration_ms
        ? (span.duration_ms >= 1000 ? (span.duration_ms / 1000).toFixed(2) + 's' : span.duration_ms + 'ms')
        : '';
      var durBdgGroup = durGroup ? '<span class="turn-stats">' + durGroup + '</span>' : '';
      var htmlGroup = '<div class="event-row depth-' + depth + ' ' + this._spanAccentClass(span) + '"'
        + ' data-span-id="' + esc(span.span_id) + '"'
        + ' style="padding-left: ' + padLeftGroup + 'rem; background: rgba(0,0,0,' + bgAlphaGroup + ')">'
        + '<span class="expand-icon ' + (isExpGroup ? 'expanded' : '') + '" data-toggle="' + esc(groupToggleKey) + '">▶</span>'
        + '<span class="tool-chip tool-mcp-server" style="background-color: ' + serverColor + '; color: #ffffff" title="MCP server">' + esc(span.mcp_server) + '</span>'
        + '<span class="tool-chip tool-mcp">' + span.count + ' tools</span>'
        + '<span class="event-summary"></span>'
        + durBdgGroup
        + '</div>';
      if (isExpGroup && span.children && span.children.length > 0) {
        htmlGroup += span.children.map(c => this.renderSpan(c, depth + 1, subagentDepth, parentFeatureId, parentModel)).join('');
      }
      return htmlGroup;
    }

    var toggleKey = 'span:' + span.span_id;
    var hasChildren = span.children && span.children.length > 0;
    var hasDetailPanel = this._spanHasDetail(span);
    // Synthetic pending roots default to expanded so the actual tool
    // activity is visible without an extra click — users shouldn't have
    // to discover a placeholder node just to see their own tool calls.
    // Subagent invocation spans (Task/Agent tool calls) also default to
    // expanded to show their children without requiring interaction.
    var collapsed = JSON.parse(localStorage.getItem('hg-collapsed') || '[]');
    var isUserCollapsed = collapsed.indexOf(toggleKey) !== -1;
    var isExp = this.expanded.has(toggleKey)
      || (span._pending && !isUserCollapsed)
      || (this._isSubagentSpan(span) && !isUserCollapsed);
    var expandIcon = (hasDetailPanel || hasChildren)
      ? '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(toggleKey) + '">\u25B6</span>'
      : '<span class="expand-icon-spacer"></span>';

    var d = span.details || {};
    var isToolSpan = Boolean(span.tool_name);
    var isRoot = !span.parent_span;

    // Label + chip class + optional MCP-server pill.
    // - Built-in tool spans (Bash/Read/Edit/Agent/...): use existing
    //   .tool-{Name} classes for color consistency with hook rows.
    // - MCP tools (name pattern mcp__server__tool): strip the prefix,
    //   render a small color-coded server pill + the tool's own name.
    // - Non-tool spans (interaction, llm_request, tool_execution,
    //   tool_blocked_on_user): neutral .tool-otel class.
    var label, chipClass, chipStyle = '', mcpServerPill = '';
    if (isToolSpan) {
      var mcp = this._parseMCPToolName(span.tool_name);
      if (mcp) {
        label = mcp.toolName;
        chipClass = 'tool-chip tool-mcp';
        mcpServerPill = '<span class="tool-chip tool-mcp-server" '
          + 'style="background-color: ' + this._mcpServerColor(mcp.serverName) + '; color: #ffffff"'
          + ' title="MCP server">' + esc(mcp.serverName) + '</span>';
      } else {
        label = span.tool_name;
        chipClass = 'tool-chip tool-' + span.tool_name;
      }
      // Subagent delegations (Task/Agent tool) take on the agent's
      // color family — researcher=cyan, haiku=green, etc.
      if ((span.tool_name === 'Task' || span.tool_name === 'Agent') && d.subagent_type) {
        chipStyle = ' style="background-color: ' + this.agentBadgeColor(d.subagent_type) + '; color: #ffffff"';
      }
    } else {
      label = this._spanCanonicalLabel(span);
      chipClass = 'tool-chip tool-otel';
    }

    // Summary text: tool-specific detail beats the native span name.
    var summary = this._spanSummary(span);

    var dur = span.duration_ms
      ? (span.duration_ms >= 1000 ? (span.duration_ms / 1000).toFixed(2) + 's' : span.duration_ms + 'ms')
      : '';

    var errBorder = (span.success === false) ? 'border-error' : '';
    // Match hook-row visual treatment: same padding ladder, same
    // bg-alpha ladder, same base row class. Span rows no longer look
    // visually distinct from hook rows — the span tree IS the tree now.
    // Subagent-rooted children get extra visual nesting: each subagent
    // boundary adds 2 * 1.25rem to the indent (beyond the per-level step).
    var effectiveDepth = depth + subagentDepth * 2;
    var padLeft = (effectiveDepth + 1) * 1.25;
    var bgAlpha = 0.05 + depth * 0.08;

    var traceChip = '';
    if (isRoot) {
      if (span._pending) {
        traceChip = '<span class="tool-chip tool-otel-trace" title="Root interaction span not yet received — children are grouped here provisionally">pending</span>';
      } else {
        traceChip = '<span class="tool-chip tool-otel-trace" title="OTel trace root: ' + esc(span.native_name) + '">trace</span>';
      }
    }

    var subagentBadge = '';
    if (isToolSpan && (span.tool_name === 'Task' || span.tool_name === 'Agent') && d.subagent_type) {
      var col = this.agentBadgeColor(d.subagent_type);
      subagentBadge = '<span class="badge badge-subagent" style="background-color: ' + col + '">' + esc(d.subagent_type) + '</span>';
    }

    // Work-item attribution: span rows mirror hook rows by rendering the
    // feature badge when feature_id is populated. Attribution comes from
    // active_work_items at ingest time (see writer.go); this lights up
    // only for sessions whose root agent claimed a work item before the
    // signal arrived. Pre-attribution rows stay silent.
    // Dedup: suppress the badge when the feature matches the parent turn's
    // feature (parentFeatureId). Render it only when different — that's the
    // interesting case (e.g. a subagent grabbing a different work item).
    var featureBdg = (isToolSpan && span.feature_id && span.feature_id !== parentFeatureId)
      ? this.featureBadge(span.feature_id, span.feature_title)
      : '';

    // For tool spans, also surface the preceding api_request (the LLM
    // turn that chose this tool) — its model / cost / duration attribute
    // to "deciding this tool call" and so belong on the tool row.
    var api = (isToolSpan && span._precedingApi) ? span._precedingApi : null;
    var apiModel = (api && api.model) || span.model;
    var apiCost = api ? api.cost_usd : span.cost_usd;
    // Fallback model source for Task/Agent rows that share an api_request
    // with a sibling (N Tasks dispatched in one LLM turn). _modelRef is
    // read-only — used for the model pill ONLY, never for cost/tokens (to
    // avoid double-counting the tokens already attributed to the absorbing
    // sibling Task via its _precedingApi).
    if (!apiModel && isToolSpan && span._modelRef && span._modelRef.model) {
      apiModel = span._modelRef.model;
    }

    // Compact, color-coded model badge. Inline-styled so each family
    // (Opus/Sonnet/Haiku, plus generic for OpenAI/Google) gets its
    // own color without inflating CSS. The short label ("Opus 4.7",
    // "Sonnet 4.6", "Haiku 4.5") scans far faster than the full
    // "claude-opus-4-7-20251014" id at row scale.
    // Dedup: suppress model badge when it matches the parent turn's model
    // (parentModel). Show only when different (e.g. Haiku subagent inside
    // an Opus turn).
    var resolvedModelName = apiModel ? this._shortModelName(apiModel) : '';
    var modelBdg = (apiModel && resolvedModelName !== parentModel)
      ? this._modelBadge(apiModel, api ? 'Model for the api_request that decided this tool call' : 'Model')
      : '';
    var costBdg = (apiCost > 0)
      ? '<span class="badge badge-otel">$' + apiCost.toFixed(4) + '</span>'
      : '';
    var retryBdg = (d.attempt && d.attempt > 1)
      ? '<span class="badge badge-otel" title="Attempt number">attempt ' + d.attempt + '</span>'
      : '';
    var durBdg = dur ? '<span class="turn-stats">' + dur + '</span>' : '';

    // For tool spans, roll up the child permission + exec spans into
    // compact badges on this row (instead of forcing the user to expand
    // the tool just to see the outcome). The children still render
    // in full when the tool span is expanded.
    var rollup = this._toolChildRollup(span);
    var permissionBdg = rollup.permissionBadge;
    var execErrorBdg = rollup.execErrorBadge;
    var rangeBdg = this._rangeBadge(span);

    // Hover tooltip: for Bash show the full command (since summary is
    // description); for file tools (Read/Edit/Write/NotebookEdit) show the
    // full absolute path (since the summary was relativized); for other
    // tools, show the native span name for provenance.
    var rowTitle = '';
    if (span.tool_name === 'Bash' && d.full_command) {
      rowTitle = d.full_command;
    } else if ((span.tool_name === 'Read' || span.tool_name === 'Edit' ||
                span.tool_name === 'Write' || span.tool_name === 'NotebookEdit') && d.file_path) {
      rowTitle = d.file_path;
    } else if (span.native_name) {
      rowTitle = span.native_name;
    }

    var html = '<div class="event-row depth-' + depth + ' ' + errBorder + ' ' + this._spanAccentClass(span) + '"'
      + ' data-span-id="' + esc(span.span_id) + '"'
      + ' data-trace-id="' + esc(span.trace_id) + '"'
      + (span.parent_span ? ' data-parent-span="' + esc(span.parent_span) + '"' : '')
      + (rowTitle ? ' title="' + esc(rowTitle) + '"' : '')
      + ' style="padding-left: ' + padLeft + 'rem; background: rgba(0,0,0,' + bgAlpha + ')">'
      + expandIcon
      + traceChip
      + mcpServerPill
      + '<span class="' + chipClass + '"' + chipStyle + '>' + esc(label) + '</span>'
      + subagentBadge
      + featureBdg
      + modelBdg
      + costBdg
      + permissionBdg
      + execErrorBdg
      + retryBdg
      + rangeBdg
      + '<span class="event-summary">' + esc(summary) + '</span>'
      + durBdg
      + '</div>';

    if (isExp && hasChildren) {
      // When expanded, first render a detail block for the tool row
      // itself (full command, timeout, prompt, URL, etc.), then the
      // child spans below it. When collapsed, no detail block — the
      // badges already summarize.
      var detailBlock = this._spanDetailBlock(span, depth + 1);
      if (detailBlock) html += detailBlock;
      // Track subagent nesting: if this span is a subagent invocation,
      // increment subagentDepth for children so they render deeper.
      var nextSubagentDepth = subagentDepth + (this._isSubagentSpan(span) ? 1 : 0);
      // Propagate feature/model context so children can dedup badges.
      // If this span introduced a different feature/model, pass it down;
      // otherwise keep propagating what we received.
      var childFeatureId = (span.feature_id && span.feature_id !== parentFeatureId)
        ? span.feature_id : parentFeatureId;
      var childModel = resolvedModelName || parentModel;
      // F4: filter infrastructure children before rendering.
      // 1. Always drop tool_execution — parent duration covers it.
      // 2. Drop tool_blocked_on_user when auto-approved (config source, or
      //    missing decision_source). User-visible decisions (user-approve,
      //    reject, abort) keep their own rows; a dim badge on the parent row
      //    (via _toolChildRollup) summarises the auto-approved path.
      var visibleChildren = span.children.filter(function(c) {
        if (c.canonical === 'tool_execution') return false;
        if (c.canonical === 'tool_blocked_on_user') {
          var ds = (c.details || {}).decision_source;
          if (!ds || ds === 'config' || ds === 'hook') return false;
        }
        return true;
      });
      html += visibleChildren.map(c => this.renderSpan(c, depth + 1, nextSubagentDepth, childFeatureId, childModel)).join('');
    }
    return html;
  }

  // _spanDetailBlock renders a single fixed-width panel below a tool row
  // when it's expanded, showing the full input context the summary line
  // couldn't fit. Returns '' when there's nothing worth showing.
  //
  // The per-tool branching is dispatched through TOOL_DETAIL_ROW_BUILDERS
  // and TOOL_CODE_BUILDERS (module-level tables above); this method only
  // orchestrates and assembles HTML. The MCP and generic tool_input paths
  // live on the class because they need _parseMCPToolName and the shared
  // long-value scanner _longInputCodeBlocks.
  _spanDetailBlock(span, depth) {
    var d = span.details || {};
    if (!span.tool_name) return '';
    var rows = this._spanDetailRows(span, d);
    var codeBlocks = this._spanCodeBlocks(span, d);
    if (rows.length === 0 && !codeBlocks) return '';
    var padLeft = (depth + 1) * 1.25;
    var bgAlpha = 0.05 + depth * 0.08;
    var kvHtml = rows.map(function(r) {
      return '<div class="otel-detail-row"><span class="otel-detail-key">' + esc(r[0]) + '</span>'
        + '<span class="otel-detail-val">' + esc(String(r[1])) + '</span></div>';
    }).join('');
    return '<div class="event-row event-row-otel-detail depth-' + depth + ' ' + this._spanAccentClass(span) + '"'
      + ' style="padding-left: ' + padLeft + 'rem; background: rgba(0,0,0,' + bgAlpha + ')">'
      + kvHtml
      + codeBlocks
      + '</div>';
  }

  // _spanDetailRows builds the kv-row array: per-tool rows via the dispatch
  // table, then optional api_request rows from an absorbed _precedingApi,
  // then any short scalar tool_input keys not covered by the tool's own
  // branch (skipped for mcp__ tools — they use _mcpDetailRows instead).
  _spanDetailRows(span, d) {
    var tn = span.tool_name;
    var isMcp = tn && tn.indexOf('mcp__') === 0;
    var rows;
    if (isMcp) {
      rows = this._mcpDetailRows(span, d);
    } else {
      var builder = tn ? TOOL_DETAIL_ROW_BUILDERS[tn] : null;
      rows = builder ? builder(d) : [];
    }
    if (span._precedingApi) this._appendApiRows(rows, span._precedingApi);
    if (!isMcp) this._appendShortToolInputRows(rows, d);
    return rows;
  }

  // _appendApiRows appends rows summarising an absorbed api_request span
  // (model, token counts, cost, duration, request id, mode, command type).
  // Extracted from _spanDetailRows so each method stays below the cyclomatic
  // threshold; the eight conditional pushes still live here, but only here.
  _appendApiRows(rows, api) {
    var ad = api.details || {};
    if (api.model)            rows.push(['model', api.model]);
    if (api.tokens_in)        rows.push(['input tokens', api.tokens_in.toLocaleString()]);
    if (api.tokens_out)       rows.push(['output tokens', api.tokens_out.toLocaleString()]);
    if (api.cost_usd > 0)     rows.push(['cost', '$' + api.cost_usd.toFixed(6)]);
    if (api.duration_ms)      rows.push(['api duration', api.duration_ms + 'ms']);
    if (ad.request_id)        rows.push(['request id', ad.request_id]);
    if (ad.mode || ad.speed)  rows.push(['mode', ad.mode || ad.speed]);
    if (ad.command_type)      rows.push(['command type', ad.command_type]);
  }

  // _appendShortToolInputRows appends generic short-scalar tool_input keys
  // as kv rows. Long/multi-line values are skipped — they're emitted as
  // code blocks via _longInputCodeBlocks. Called for non-mcp tools only;
  // mcp tools have their own input handling in _mcpDetailRows.
  _appendShortToolInputRows(rows, d) {
    if (!d.tool_input || typeof d.tool_input !== 'object') return;
    var keys = Object.keys(d.tool_input);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      var v = d.tool_input[k];
      var vStr = (typeof v === 'string') ? v : JSON.stringify(v, null, 2);
      if (vStr.length <= 200 && vStr.indexOf('\n') === -1) {
        rows.push([k, vStr]);
      }
    }
  }

  // _mcpDetailRows renders server/tool name plus the short scalar entries
  // of mcp_input. Long/multi-line values are deferred to _longInputCodeBlocks
  // via _spanCodeBlocks. When mcp_input is absent we fall back to the legacy
  // url/query/pattern/file_path detail fields.
  _mcpDetailRows(span, d) {
    var rows = [];
    var mcp = this._parseMCPToolName(span.tool_name);
    if (mcp) {
      rows.push(['server', mcp.serverName]);
      rows.push(['tool', mcp.toolName]);
    }
    if (d.mcp_input && typeof d.mcp_input === 'object') {
      var keys = Object.keys(d.mcp_input);
      for (var i = 0; i < keys.length; i++) {
        var k = keys[i];
        var v = d.mcp_input[k];
        var vStr = (typeof v === 'string') ? v : JSON.stringify(v, null, 2);
        if (vStr.length <= 200 && vStr.indexOf('\n') === -1) {
          rows.push([k, vStr]);
        }
      }
    } else {
      if (d.url)        rows.push(['url', d.url]);
      if (d.query)      rows.push(['query', d.query]);
      if (d.pattern)    rows.push(['pattern', d.pattern]);
      if (d.file_path)  rows.push(['file', d.file_path]);
    }
    return rows;
  }

  // _spanCodeBlocks emits the <pre><code> panels below the kv rows. Per-tool
  // long content (Bash command, Edit old/new_string, Write content) goes
  // through TOOL_CODE_BUILDERS. The mcp__ and generic tool_input paths share
  // _longInputCodeBlocks. tool_output is always appended if present.
  _spanCodeBlocks(span, d) {
    var tn = span.tool_name;
    var blocks = '';
    if (tn) {
      var builder = TOOL_CODE_BUILDERS[tn];
      if (builder) {
        blocks = builder(d, this);
      } else if (tn.indexOf('mcp__') === 0 && d.mcp_input && typeof d.mcp_input === 'object') {
        blocks = this._longInputCodeBlocks(d.mcp_input);
      } else if (d.tool_input && typeof d.tool_input === 'object') {
        blocks = this._longInputCodeBlocks(d.tool_input);
      }
    }
    if (d.tool_output) {
      blocks += this._codeBlock('output', d.tool_output, d.tool_output.length, false, '');
    }
    return blocks;
  }

  // _longInputCodeBlocks renders any long-or-multiline entries in a generic
  // input map (mcp_input or tool_input) as labeled code blocks. Short scalar
  // entries are skipped — they were already rendered as kv rows upstream.
  _longInputCodeBlocks(input) {
    var out = '';
    var keys = Object.keys(input);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      var v = input[k];
      var vStr = (typeof v === 'string') ? v : JSON.stringify(v, null, 2);
      if (vStr.length > 200 || vStr.indexOf('\n') !== -1) {
        var lang = (typeof v !== 'string') ? 'json' : '';
        out += this._codeBlock(k, vStr, vStr.length, false, lang);
      }
    }
    return out;
  }

  // _codeBlock emits a labeled <pre><code> block for a string attribute
  // (bash command, edit old_string/new_string, write content). The full
  // length is shown in the header so users know whether truncation lost
  // content. The language-* class on <code> lets a future syntax
  // highlighter (Prism / highlight.js per feat-292f87fe) colorize the
  // block without further JS changes.
  _codeBlock(label, content, fullLen, wasTruncated, language) {
    var header = esc(label);
    if (fullLen) header += ' (' + fullLen + ' chars' + (wasTruncated ? ', truncated' : '') + ')';
    if (language) header += ' · ' + esc(language);
    var codeClass = language ? ' class="language-' + esc(language) + '"' : '';
    return '<div class="otel-detail-code">'
      + '<div class="otel-detail-code-header">' + header + '</div>'
      + '<pre class="otel-detail-code-body"><code' + codeClass + '>' + esc(content) + '</code></pre>'
      + '</div>';
  }

  // _detectLanguage maps a file path extension to a Prism/highlight.js
  // language identifier. Returns empty string when the extension isn't
  // recognized (code block still renders, just without language class).
  // Extend this as we add languages — the full list tracked in
  // feat-292f87fe.
  _detectLanguage(filePath) {
    if (!filePath) return '';
    var dot = filePath.lastIndexOf('.');
    if (dot === -1) return '';
    var ext = filePath.slice(dot + 1).toLowerCase();
    return EXT_TO_LANGUAGE[ext] || '';
  }

  // _toolChildRollup scans a tool span's immediate children for
  // infrastructure spans (permission + exec) and returns badges that
  // surface only the meaningful outcomes.
  //
  // Auto-approval (config / hook) is the silent happy path — don't
  // render a chip for it (would clutter every row with "✓ auto"). A
  // tiny dot on the tool row's left indicates "yes this ran" if
  // visual presence is needed; see .event-row-otel-span::before in
  // components.css.
  //
  // User-approved, blocked, rejected, aborted, and exec-failed states
  // DO get loud badges — they're the exceptional cases worth seeing.
  _toolChildRollup(span) {
    var empty = { permissionBadge: '', execErrorBadge: '' };
    if ((span.canonical !== 'tool_result' && span.canonical !== 'subagent_invocation') || !span.tool_name || !span.children) {
      return empty;
    }
    var perm = span.children.find(function(c) { return c.canonical === 'tool_blocked_on_user'; });
    var exec = span.children.find(function(c) { return c.canonical === 'tool_execution'; });

    var permBadge = '';
    if (perm) {
      var d = perm.details || {};
      switch (d.decision_source) {
        case 'config':
        case 'hook':
          // Auto-approved — omit. Provenance survives on the tool row's
          // title tooltip and the detail panel.
          permBadge = '';
          break;
        case 'user_permanent':
        case 'user_temporary':
          permBadge = '<span class="badge badge-approve" title="User approved (' + esc(d.decision_source) + ')">\u2713 user</span>';
          break;
        case 'user_reject':
          permBadge = '<span class="badge badge-reject" title="User rejected the tool call">\u2717 blocked</span>';
          break;
        case 'user_abort':
          permBadge = '<span class="badge badge-reject" title="User aborted the turn">\u2717 aborted</span>';
          break;
        default: // unknown / empty — omit
          permBadge = '';
      }
    }

    var execBadge = '';
    if (exec && exec.success === false) {
      execBadge = '<span class="badge badge-reject" title="Tool execution reported failure">failed</span>';
    }

    return { permissionBadge: permBadge, execErrorBadge: execBadge };
  }

  // _modelBadge returns a compact color-coded model chip. Maps Claude
  // model families to the existing agent palette (opus=purple,
  // sonnet=blue, haiku=green) so trace rows share color semantics with
  // subagent badges. Unknown/third-party models fall back to the
  // generic .badge-otel style.
  _modelBadge(model, title) {
    var short = this._shortModelName(model);
    var color = this._modelColor(model);
    if (!color) {
      return '<span class="badge badge-otel" title="' + esc(title || 'Model: ' + model) + '">' + esc(short) + '</span>';
    }
    return '<span class="badge badge-model" style="background-color: ' + color + '; color: #ffffff"'
      + ' title="' + esc((title ? title + '\n' : '') + model) + '">' + esc(short) + '</span>';
  }

  // _shortModelName trims Claude's verbose ids to a human label.
  // claude-opus-4-7              → Opus 4.7
  // claude-opus-4-7-20251014     → Opus 4.7
  // claude-sonnet-4-6-20251005   → Sonnet 4.6
  // claude-haiku-4-5-20251001    → Haiku 4.5
  // gpt-5, gpt-4.1-mini          → passed through
  _shortModelName(model) {
    if (!model) return '';
    var m = model.toLowerCase();
    var match = m.match(/^claude-(opus|sonnet|haiku)-(\d+)-(\d+)/);
    if (match) {
      var family = match[1].charAt(0).toUpperCase() + match[1].slice(1);
      return family + ' ' + match[2] + '.' + match[3];
    }
    // Strip trailing -YYYYMMDD date stamps for other providers too.
    return model.replace(/-\d{8}$/, '');
  }

  _modelColor(model) {
    if (!model) return '';
    var m = model.toLowerCase();
    if (m.indexOf('opus') !== -1)   return '#a855f7'; // purple — matches badge-agent-opus
    if (m.indexOf('sonnet') !== -1) return '#3b82f6'; // blue   — matches badge-agent-sonnet
    if (m.indexOf('haiku') !== -1)  return '#22c55e'; // green  — matches badge-agent-haiku
    return ''; // unknown → fall back to neutral
  }

  // _parseMCPToolName splits "mcp__<server>__<tool>" into its parts so
  // the renderer can show the server as a separate pill and the tool
  // name unqualified. Returns null when the name doesn't match the
  // MCP convention, so callers fall through to standard rendering.
  _parseMCPToolName(name) {
    if (!name || name.indexOf('mcp__') !== 0) return null;
    var rest = name.slice(5); // drop "mcp__"
    var sep = rest.indexOf('__');
    if (sep <= 0) return { serverName: rest, toolName: '' };
    return {
      serverName: rest.slice(0, sep),
      toolName: rest.slice(sep + 2),
    };
  }

  // _mcpServerOf returns the MCP server name for an MCP span, or null
  // for non-MCP spans. Used by _groupConsecutiveMcp to identify runs.
  _mcpServerOf(span) {
    if (!span || span.synthetic) return null;
    var parsed = this._parseMCPToolName(span.tool_name);
    return parsed ? parsed.serverName : null;
  }

  // _groupConsecutiveMcp rewrites a children array so that runs of ≥2
  // consecutive sibling spans from the same MCP server are collapsed into
  // a single synthetic "mcp_group" wrapper. Single-span runs and non-MCP
  // spans pass through unchanged. Applies recursively to each child's own
  // children (real children inside a group stay ungrouped — they already
  // live inside a synthetic wrapper and rarely have their own MCP runs).
  _groupConsecutiveMcp(children) {
    var self = this;
    if (!children || children.length === 0) return children;
    var result = [];
    var i = 0;
    while (i < children.length) {
      var server = self._mcpServerOf(children[i]);
      if (!server) {
        // Non-MCP span: recurse into its own children then pass through.
        var child = children[i];
        if (child.children && child.children.length > 0) {
          child = Object.assign({}, child, { children: self._groupConsecutiveMcp(child.children) });
        }
        result.push(child);
        i++;
        continue;
      }
      // Start an MCP run — collect consecutive siblings with same server.
      var run = [children[i]];
      i++;
      while (i < children.length && self._mcpServerOf(children[i]) === server) {
        run.push(children[i]);
        i++;
      }
      if (run.length === 1) {
        // Single MCP call — no grouping; recurse into its children.
        var sole = run[0];
        if (sole.children && sole.children.length > 0) {
          sole = Object.assign({}, sole, { children: self._groupConsecutiveMcp(sole.children) });
        }
        result.push(sole);
      } else {
        // Multi-span run — synthesize a group wrapper.
        var totalMs = run.reduce(function(acc, s) { return acc + (s.duration_ms || 0); }, 0);
        result.push({
          synthetic: 'mcp_group',
          span_id: 'mcpgrp-' + run[0].span_id + '-' + run[run.length - 1].span_id,
          mcp_server: server,
          tool_name: 'mcp_group',
          canonical: 'mcp_group',
          duration_ms: totalMs,
          start_ts_micros: run[0].start_ts_micros || run[0].ts_micros,
          children: run,
          count: run.length,
        });
      }
    }
    return result;
  }

  // _mcpServerColor returns a deterministic color for a given MCP
  // server name so multiple tools from the same server share a color.
  // Hash the name to an HSL hue, keep saturation+lightness constant so
  // every server gets a distinct but consistent tint. No static map —
  // new servers get a color automatically.
  _mcpServerColor(serverName) {
    var hash = 0;
    for (var i = 0; i < serverName.length; i++) {
      hash = ((hash << 5) - hash) + serverName.charCodeAt(i);
      hash |= 0; // force int32
    }
    var hue = Math.abs(hash) % 360;
    return 'hsl(' + hue + ', 55%, 45%)';
  }

  // _rangeBadge renders a compact line-range badge for Read/Edit tool
  // spans when offset/limit or old_string/new_string context is present
  // in the span's details. Returns an empty string when no range data
  // is available (most Read calls don't pass offset/limit).
  _rangeBadge(span) {
    if (!span.tool_name) return '';
    var d = span.details || {};
    if (span.tool_name === 'Read') {
      if (d.offset || d.limit) {
        var start = d.offset || 1;
        var end = d.limit ? (start + d.limit - 1) : '?';
        return '<span class="badge badge-otel" title="Line range">L' + start + '\u2013' + end + '</span>';
      }
    }
    // Edit/Write/NotebookEdit don't currently expose a range in the
    // span's attrs; that data lives on the tool_result log's tool_input.
    // A follow-up can surface it once we join spans to logs.
    return '';
  }

  // _isSubagentSpan detects whether a span represents a subagent invocation
  // (Task or Agent tool call). Used to apply extra visual nesting depth
  // to subagent-rooted child spans.
  _isSubagentSpan(span) {
    if (!span) return false;
    if (span.canonical === 'subagent_invocation') return true;
    var name = span.tool_name || span.name || '';
    return name === 'Task' || name === 'Agent';
  }

  // _spanCanonicalLabel returns a short human label for non-tool spans.
  // Maps claude_code.interaction → "interaction", claude_code.llm_request
  // → "api_request", tool.execution → "exec", tool.blocked_on_user →
  // "permission", etc.
  _spanCanonicalLabel(span) {
    switch (span.canonical) {
      case 'interaction':           return 'interaction';
      case 'api_request':           return 'api_request';
      case 'tool_execution':        return 'exec';
      case 'tool_blocked_on_user':  return 'permission';
      default:
        // Strip the harness prefix if present (claude_code.*).
        var n = span.native_name || '';
        var i = n.indexOf('.');
        return i >= 0 ? n.slice(i + 1) : (n || 'span');
    }
  }

  // _spanSummary returns the descriptive text for a span row.
  // Priority: tool-specific detail (command, file path, etc.) > a
  // derived label for infrastructure spans > the native name as a
  // fallback so rows are never empty.
  //
  // For Bash: prefer the human "description" over the raw command —
  // "Push rollup + enrichment commit" scans faster than a 60-char shell
  // string. The full command is exposed via a hover title + detail row
  // on expand.
  _spanSummary(span) {
    var d = span.details || {};
    var tn = span.tool_name;
    if (tn) {
      var fn = TOOL_SUMMARY[tn];
      if (fn) return fn(d, this);
      // MCP tools: when tool_input carries something obviously summarizable
      // use it; otherwise leave empty (expand to see full args).
      if (tn.indexOf('mcp__') === 0) return d.url || d.query || d.pattern || '';
    }
    // tool_blocked_on_user is a misleading name — it's the PERMISSION GATE
    // span covering the time between tool emit and execution; it does NOT
    // mean the tool was blocked. The decision_source attribute (mapped via
    // BLOCKED_DECISION_TEXT) tells us how the gate resolved.
    var cfn = CANONICAL_SUMMARY[span.canonical];
    return cfn ? cfn(d) : '';
  }
}

customElements.define('hg-event-tree', HgEventTree);

// Delegate click events
document.addEventListener('click', function(e) {
  // Expand/collapse toggle takes priority
  var toggle = e.target.closest('[data-toggle]');
  if (toggle) {
    var tree = document.querySelector('hg-event-tree');
    if (tree) tree.toggle(toggle.dataset.toggle);
    return;
  }

  // Clickable event row → drill down to transcript
  var row = e.target.closest('.clickable-row[data-session]');
  if (row) {
    var sid = row.dataset.session;
    var scrollHint = {
      toolUseId: row.dataset.toolUseId || '',
      toolName: row.dataset.toolName || '',
      timestamp: row.dataset.timestamp || ''
    };
    currentView = 'sessions';
    document.querySelectorAll('.nav-btn').forEach(function(b) {
      b.classList.toggle('active', b.dataset.view === 'sessions');
    });
    document.querySelectorAll('.view').forEach(function(v) {
      v.classList.toggle('active', v.id === 'v-sessions');
    });
    openTranscript(sid, scrollHint);
    return;
  }
});

/* ── Slice-7: documented grouping contract ──────────────────────────────────
 *
 * Default grouping across the dashboard (canonical→family→session):
 *   Level 1 — canonical_project (sessions.project_dir)
 *             Exposed by /api/sessions/parallel as the top-level "groups" key.
 *   Level 2 — session_family_id
 *             Exposed by /api/sessions (session_family_id field) and
 *             /api/sessions/family-info; family members returned by
 *             /api/sessions/family (turn tree filtered to family).
 *   Level 3 — individual session drilldown (this component's default view)
 *             Turns keyed by session_id; filter by session_family_id using
 *             the /api/sessions/family endpoint.
 *
 * This component renders Level 3 (per-session turn tree). Levels 1 & 2 are
 * rendered by the sessions panel (renderSessions + renderSessionFilterRow in
 * app.js) and exposed as API for plan-c3bbb1ed Kanban consumption.
 *
 * plan-c3bbb1ed identity fields available per turn via user_query:
 *   - session_id   → join to /api/sessions for exec_root, harness, model
 *   - feature_id   → join to /api/features for claim_collision, claimants[]
 *   - agent_id     → harness discriminator
 * ──────────────────────────────────────────────────────────────────────────*/
