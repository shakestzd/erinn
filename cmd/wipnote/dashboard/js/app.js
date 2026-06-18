/* ── Application state & data fetching ─────────────────────── */

var events = [];
var sessions = [];
var resumableSessions = [];
var features = [];
var plans = [];
var stats = {};
var sessionAdherenceTrend = [];
var currentView = 'activity';
var seenEventIds = new Set();
var groupByTrack = localStorage.getItem('wipnote-kanban-group-by-track') === 'true';
var activityFeedError = '';
var resumableSessionsError = '';
var resumableSessionsLoading = false;

// Global mode state — populated by detectMode() on init. In single-project
// mode both values stay unset and buildProjectUrl() returns plain URLs.
window.wipnoteMode = 'single';
window.wipnoteProjects = [];
window.wipnoteProjectId = '';

// Terminal state — tracks the currently running ttyd sidecar pid.
var terminalPid = null;
// The last work-item ID opened in the Work detail panel; passed to the
// terminal start request so the session is pre-scoped to that item.
window.wipnoteActiveWorkItem = '';

/* ── Navigation ────────────────────────────────────────────── */
document.querySelector('.nav').addEventListener('click', function(e) {
  var btn = e.target.closest('.nav-btn');
  if (!btn) return;
  var view = btn.dataset.view;
  if (view === currentView) return;
  currentView = view;
  document.querySelectorAll('.nav-btn').forEach(function(b) { b.classList.toggle('active', b === btn); });
  document.querySelectorAll('.view').forEach(function(v) { v.classList.toggle('active', v.id === 'v-' + view); });
  if (view === 'sessions' && sessions.length === 0) fetchSessions();
  if (view === 'sessions') fetchSessionAdherenceTrend();
  if (view === 'resume') fetchResumableSessions();
  if (view === 'work' && features.length === 0) fetchFeatures();
  if (view === 'plans') fetchPlans();
  if (view === 'graph') fetchGraph();
});

/* ── Data fetching ─────────────────────────────────────────── */
function fetchStats() {
  // In global mode with no project selected, show aggregate stats.
  var url = (window.wipnoteMode === 'global' && !window.wipnoteProjectId)
    ? '/api/projects/all/stats'
    : buildProjectUrl('stats');
  return fetch(url).then(function(r) {
    if (!r.ok) return;
    return r.json().then(function(data) {
      stats = data;
      updateStatsBar();
    });
  }).catch(function() {});
}

function formatCost(val) {
  if (val >= 1000) return '$' + (val / 1000).toFixed(1) + 'k';
  if (val >= 1) return '$' + val.toFixed(0);
  return '$' + val.toFixed(2);
}

function updateStatsBar() {
  setVal('sv-live', stats.live_sessions);
  setVal('sv-feat-ip', stats.features_in_progress);
  setVal('sv-done-today', '+' + (stats.done_today || 0));
  setVal('sv-cost', formatCost(stats.cost_today || 0));
  var errPill = document.getElementById('sp-errors');
  if (errPill) {
    if (stats.errors_today > 0) {
      errPill.style.display = '';
      setVal('sv-errors', stats.errors_today);
    } else {
      errPill.style.display = 'none';
    }
  }
}

function fetchEvents() {
  return fetch(buildProjectUrl('events/recent', 'limit=100')).then(function(r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json().then(function(data) {
      activityFeedError = '';
      events = data;
      seenEventIds = new Set();
      events.forEach(function(e) { seenEventIds.add(e.event_id); });
      updateActivityFeedError();
    });
  }).catch(function(err) {
    events = [];
    seenEventIds = new Set();
    activityFeedError = 'Feed unavailable right now.';
    updateActivityFeedError();
    return err;
  });
}

function updateActivityFeedError() {
  var countEl = document.getElementById('filter-count');
  if (!countEl) return;
  if (activityFeedError) {
    countEl.textContent = activityFeedError;
    countEl.title = activityFeedError;
    return;
  }
  countEl.title = '';
}

function fetchSessions() {
  return fetch(buildProjectUrl('sessions')).then(function(r) {
    if (!r.ok) return;
    return r.json().then(function(data) {
      sessions = data;
      renderSessions();
    });
  }).catch(function() {});
}

function fetchResumableSessions() {
  resumableSessionsLoading = true;
  resumableSessionsError = '';
  renderResumableSessions();
  return fetch(buildProjectUrl('sessions/resumable')).then(function(r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json().then(function(data) {
      resumableSessions = (data && data.sessions) || [];
      resumableSessionsError = '';
      renderResumableSessions();
    });
  }).catch(function() {
    resumableSessions = [];
    resumableSessionsError = 'Resume index unavailable right now.';
    renderResumableSessions();
  }).finally(function() {
    resumableSessionsLoading = false;
    renderResumableSessions();
  });
}

function fetchSessionAdherenceTrend() {
  var panel = document.getElementById('session-trend-panel');
  if (!panel || isDoorwayLanding()) return Promise.resolve();
  panel.classList.add('loading');
  return fetch(buildProjectUrl('sessions/adherence-trend'))
    .then(function(r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    })
    .then(function(data) {
      sessionAdherenceTrend = (data && data.points) || [];
      renderSessionAdherenceTrend();
    })
    .catch(function() {
      sessionAdherenceTrend = [];
      renderSessionAdherenceTrend(true);
    })
    .finally(function() {
      panel.classList.remove('loading');
    });
}

function fetchFeatures() {
  return fetch(buildProjectUrl('features')).then(function(r) {
    if (!r.ok) return;
    return r.json().then(function(data) {
      features = data;
      renderKanban();
    });
  }).catch(function() {});
}

function fetchPlans() {
  fetch(buildProjectUrl('plans'))
    .then(function(r) { return r.json(); })
    .then(function(data) {
      plans = data || [];
      renderPlans();
      var pending = plans.filter(function(p) { return p.status !== 'finalized'; }).length;
      var pill = document.getElementById('sp-plans');
      if (pill && pending > 0) {
        pill.style.display = '';
        document.getElementById('sv-plans').textContent = pending;
      }
    })
    .catch(function() {
      plans = [];
      renderPlans();
    });
}

function renderPlans(filteredPlans) {
  var items = filteredPlans || plans;
  var body = document.getElementById('plans-body');
  var empty = document.getElementById('plans-empty');
  document.getElementById('plans-count').textContent = plans.length;

  if (items.length === 0) {
    body.innerHTML = '';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';

  body.innerHTML = '';
  items.forEach(function(p) {
    var tr = document.createElement('tr');
    tr.style.cursor = 'pointer';
    tr.addEventListener('click', function() {
      openPlanDetail(p.id, p.title);
    });

    // Title
    tr.appendChild(td(p.title));

    // ID (monospace, consistent with session/work item ID style)
    tr.appendChild(td(p.id, { className: 'mono' }));

    // Status badge
    var statusClass = p.status === 'finalized' ? 'badge-done' :
                      p.status === 'in-progress' ? 'badge-ip' : 'badge-todo';
    var statusText = p.status === 'finalized' ? 'Finalized' :
                     p.status === 'in-progress' ? 'In Progress' : 'Draft';
    tr.appendChild(tdWithChild(createBadge(statusText, statusClass)));

    // Progress bar
    var pct = p.total > 0 ? Math.round(p.approved / p.total * 100) : 0;
    var progTd = document.createElement('td');
    var progWrap = document.createElement('div');
    progWrap.style.cssText = 'display:flex;align-items:center;gap:8px;';
    var progTrack = document.createElement('div');
    progTrack.style.cssText = 'flex:1;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden;';
    var progFill = document.createElement('div');
    progFill.style.cssText = 'height:100%;border-radius:3px;background:' +
      (pct === 100 ? 'var(--status-done)' : 'var(--accent)') + ';width:' + pct + '%;';
    progTrack.appendChild(progFill);
    progWrap.appendChild(progTrack);
    var progLabel = document.createElement('span');
    progLabel.style.cssText = 'font-size:0.75rem;color:var(--text-secondary);white-space:nowrap;';
    progLabel.textContent = p.approved + '/' + p.total;
    progWrap.appendChild(progLabel);
    progTd.appendChild(progWrap);
    tr.appendChild(progTd);

    // Version
    tr.appendChild(td('v' + (p.version || 1), { className: 'mono' }));

    // Linked feature/track
    tr.appendChild(td(p.feature_id || '\u2014'));

    // Updated
    tr.appendChild(td(relTime(p.updated_at)));

    // Delete button (only for non-finalized plans)
    var delTd = document.createElement('td');
    delTd.style.cssText = 'white-space:nowrap;';
    if (p.status !== 'finalized') {
      (function(planId, planTitle, td) {
        var btnStyle = 'background:transparent;border:1px solid var(--border);color:var(--text-muted);padding:3px 10px;border-radius:4px;font-size:0.7rem;cursor:pointer;font-family:var(--font-sans,inherit);';
        var delBtn = document.createElement('button');
        delBtn.textContent = 'Delete';
        delBtn.style.cssText = btnStyle;
        delBtn.addEventListener('mouseenter', function() { this.style.borderColor='#dc2626'; this.style.color='#dc2626'; });
        delBtn.addEventListener('mouseleave', function() { this.style.borderColor=''; this.style.color='var(--text-muted)'; });
        delBtn.addEventListener('click', function(e) {
          e.stopPropagation();
          td.innerHTML = '';
          var cancelBtn = document.createElement('button');
          cancelBtn.textContent = 'Cancel';
          cancelBtn.style.cssText = btnStyle + 'margin-right:6px;';
          cancelBtn.addEventListener('click', function(e2) {
            e2.stopPropagation();
            td.innerHTML = '';
            td.appendChild(delBtn);
          });
          var confirmBtn = document.createElement('button');
          confirmBtn.textContent = 'Confirm';
          confirmBtn.style.cssText = 'background:#dc2626;border:1px solid #dc2626;color:#fff;padding:3px 10px;border-radius:4px;font-size:0.7rem;cursor:pointer;font-family:var(--font-sans,inherit);';
          confirmBtn.addEventListener('click', function(e2) {
            e2.stopPropagation();
            confirmBtn.textContent = 'Deleting...';
            confirmBtn.disabled = true;
            fetch(buildProjectUrl('plans/' + planId + '/delete'), { method: 'DELETE' })
              .then(function(r) { return r.json(); })
              .then(function() { plans = []; fetchPlans(); });
          });
          td.appendChild(cancelBtn);
          td.appendChild(confirmBtn);
        });
        td.appendChild(delBtn);
      })(p.id, p.title, delTd);
    }
    tr.appendChild(delTd);

    body.appendChild(tr);
  });
}

/* ── Rendering: Sessions ───────────────────────────────────── */
function sessionSparkline(msgCount) {
  var maxMsgs = 100;
  var w = Math.min(50, Math.max(4, (msgCount / maxMsgs) * 50));
  var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('width', '50');
  svg.setAttribute('height', '16');
  svg.setAttribute('viewBox', '0 0 50 16');
  var rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
  rect.setAttribute('x', '0');
  rect.setAttribute('y', '4');
  rect.setAttribute('width', String(w));
  rect.setAttribute('height', '8');
  rect.setAttribute('rx', '2');
  rect.setAttribute('fill', 'var(--accent)');
  rect.setAttribute('opacity', '0.5');
  svg.appendChild(rect);
  return svg;
}

/* ── Sessions inline preview disclosure ────────────────────── */

// In-flight preview fetch tracker — maps sessionId → true while pending.
var _previewInFlight = {};

// Response cache — maps sessionId → {messages: [...]} after a successful
// fetch. Survives rerenders: when renderSessions() rebuilds the DOM, an
// expanded row can re-populate itself from the cache instead of issuing a
// fresh request (which would otherwise orphan a response into a detached
// previewTd from the prior render).
var _previewCache = {};

// Per-project localStorage key for expanded session IDs.
function _sessionsExpandedKey() {
  var pid = window.wipnoteProjectId || window.location.pathname;
  return 'hg-sessions-expanded-' + pid;
}

// Load expanded session IDs from localStorage.
function _loadExpandedSessions() {
  try {
    return new Set(JSON.parse(localStorage.getItem(_sessionsExpandedKey()) || '[]'));
  } catch (e) {
    return new Set();
  }
}

// Persist expanded session IDs to localStorage.
function _saveExpandedSessions(expandedSet) {
  try {
    localStorage.setItem(_sessionsExpandedKey(), JSON.stringify(Array.from(expandedSet)));
  } catch (e) { /* non-fatal */ }
}

// Global set of expanded session IDs, restored from localStorage.
var _expandedSessions = _loadExpandedSessions();

// Render a cached preview payload into the given cell.
function _renderPreviewPayload(previewTd, msgs) {
  var bodyDiv = document.createElement('div');
  bodyDiv.className = 'session-preview-body';
  if (!msgs || msgs.length === 0) {
    var empty = document.createElement('div');
    empty.className = 'session-preview-error';
    empty.textContent = 'No messages yet.';
    bodyDiv.appendChild(empty);
  } else {
    msgs.forEach(function(msg) {
      var row = document.createElement('div');
      row.className = 'session-preview-msg';
      var roleSpan = document.createElement('span');
      roleSpan.className = 'session-preview-role role-' + (msg.role || 'unknown');
      roleSpan.textContent = msg.role || '?';
      var contentSpan = document.createElement('span');
      contentSpan.className = 'session-preview-content';
      contentSpan.textContent = msg.content_truncated || '';
      row.appendChild(roleSpan);
      row.appendChild(contentSpan);
      bodyDiv.appendChild(row);
    });
  }
  previewTd.textContent = '';
  previewTd.appendChild(bodyDiv);
}

// Fetch and render preview for a session into its preview row. If the cache
// already holds a response, render synchronously from the cache. Otherwise
// show a loading state, fetch, and populate the cache on success — so a
// later renderSessions() can re-render the same data into a fresh cell
// without re-fetching.
function _fetchSessionPreview(sessionId, previewTd) {
  // Cache hit: render and return. This is how rerenders recover their
  // preview content after the DOM row they previously populated is gone.
  if (_previewCache[sessionId]) {
    _renderPreviewPayload(previewTd, _previewCache[sessionId].messages || []);
    return;
  }

  // Already fetching for this session: leave the loading state in place.
  // The in-flight fetch will populate the cache; rerenders after it lands
  // will find the cache and render from it.
  if (_previewInFlight[sessionId]) return;
  _previewInFlight[sessionId] = true;

  var loadingDiv = document.createElement('div');
  loadingDiv.className = 'session-preview-body';
  var loadingMsg = document.createElement('div');
  loadingMsg.className = 'session-preview-loading';
  loadingMsg.textContent = 'Loading preview...';
  loadingDiv.appendChild(loadingMsg);
  previewTd.textContent = '';
  previewTd.appendChild(loadingDiv);

  fetch(buildProjectUrl('sessions/' + encodeURIComponent(sessionId) + '/preview'))
    .then(function(r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    })
    .then(function(data) {
      delete _previewInFlight[sessionId];
      _previewCache[sessionId] = { messages: data.messages || [] };
      _renderPreviewPayload(previewTd, _previewCache[sessionId].messages);
    })
    .catch(function() {
      // Do NOT cache failures: clear in-flight so a future expand can retry.
      delete _previewInFlight[sessionId];
      var bodyDiv = document.createElement('div');
      bodyDiv.className = 'session-preview-body';
      var errDiv = document.createElement('div');
      errDiv.className = 'session-preview-error';
      errDiv.textContent = 'Preview unavailable.';
      bodyDiv.appendChild(errDiv);
      previewTd.textContent = '';
      previewTd.appendChild(bodyDiv);
    });
}

// Toggle expand/collapse for a session preview row.
function _toggleSessionPreview(sessionId, chevron, previewRow, previewTd) {
  if (_expandedSessions.has(sessionId)) {
    _expandedSessions.delete(sessionId);
    _saveExpandedSessions(_expandedSessions);
    chevron.classList.remove('expanded');
    previewRow.style.display = 'none';
  } else {
    _expandedSessions.add(sessionId);
    _saveExpandedSessions(_expandedSessions);
    chevron.classList.add('expanded');
    previewRow.style.display = '';
    // _fetchSessionPreview short-circuits on cache hit and on in-flight
    // requests, so it's safe (and correct) to call unconditionally here —
    // that way a transient fetch failure can be retried by re-expanding.
    _fetchSessionPreview(sessionId, previewTd);
  }
}

function renderSessions() {
  var body = document.getElementById('sessions-body');
  var empty = document.getElementById('sessions-empty');
  document.getElementById('sessions-count').textContent = sessions.length;
  renderSessionAdherenceTrend();
  body.textContent = '';
  if (sessions.length === 0) { empty.style.display = ''; return; }
  empty.style.display = 'none';

  // Pin live sessions to top, then sort by created_at DESC
  var sorted = sessions.slice().sort(function(a, b) {
    var aLive = a.status === 'active' ? 1 : 0;
    var bLive = b.status === 'active' ? 1 : 0;
    if (bLive !== aLive) return bLive - aLive;
    return (b.created_at || '') > (a.created_at || '') ? 1 : -1;
  });

  var frag = document.createDocumentFragment();
  sorted.forEach(function(s) {
    var isExpanded = _expandedSessions.has(s.session_id);

    var tr = document.createElement('tr');
    var _knownHarnesses = {'claude-code': true, 'codex': true, 'gemini': true};
    var harnessClass = (s.agent && _knownHarnesses[s.agent]) ? ' harness-' + s.agent : '';
    tr.className = 'session-row' + (s.status === 'active' ? ' live' : '') + harnessClass;
    tr.setAttribute('data-session-id', s.session_id);
    tr.addEventListener('click', function(e) {
      // Chevron click toggles preview; row click navigates.
      if (e.target.closest('.expand-icon')) return;
      openTranscript(s.session_id);
    });

    // Title cell — chevron + title + badges
    var titleTd = document.createElement('td');

    // Chevron expand icon (mirrors event-tree pattern)
    var chevron = document.createElement('span');
    chevron.className = 'expand-icon' + (isExpanded ? ' expanded' : '');
    chevron.textContent = '\u25B6';
    chevron.title = 'Toggle preview';
    titleTd.appendChild(chevron);

    var titleSpan = document.createElement('span');
    titleSpan.className = 'session-title';
    titleSpan.textContent = sessionDisplayTitle(s);
    titleSpan.title = s.first_message || s.session_id;
    titleTd.appendChild(titleSpan);
    if (s.launch_mode === 'yolo') {
      var yoloBadge = document.createElement('span');
      yoloBadge.className = 'badge-yolo';
      yoloBadge.textContent = 'YOLO';
      yoloBadge.style.marginLeft = '6px';
      titleTd.appendChild(yoloBadge);
    }
    if (s.plan_id) {
      var planBadge = document.createElement('span');
      planBadge.className = 'badge-plan';
      planBadge.textContent = 'PLAN';
      planBadge.style.marginLeft = '6px';
      planBadge.title = s.plan_id;
      planBadge.addEventListener('click', function(e) {
        e.stopPropagation();
        navigateToPlan(s.plan_id, null);
      });
      titleTd.appendChild(planBadge);
    }
    if (s.agent) {
      var _harnessLabels = {'claude-code': 'Claude', 'codex': 'Codex', 'gemini': 'Gemini'};
      var cliBadge = document.createElement('span');
      var harnessShort = _harnessLabels[s.agent] || s.agent;
      var harnessKey = _harnessLabels[s.agent] ? s.agent : 'unknown';
      cliBadge.className = 'badge-cli badge-cli-' + harnessKey;
      cliBadge.textContent = harnessShort;
      titleTd.appendChild(cliBadge);
    }
    tr.appendChild(titleTd);

    // Model cell
    var modelTd = document.createElement('td');
    modelTd.className = 'mono';
    modelTd.textContent = s.model || '--';
    tr.appendChild(modelTd);

    // Msgs cell
    tr.appendChild(td(s.message_count ? String(s.message_count) : '--', { className: 'mono' }));

    // Activity sparkline cell
    var sparkTd = document.createElement('td');
    sparkTd.appendChild(sessionSparkline(s.message_count || 0));
    tr.appendChild(sparkTd);

    // Status cell
    var statusTd = document.createElement('td');
    if (s.status === 'active') {
      var liveBadge = document.createElement('span');
      liveBadge.className = 'badge-live';
      liveBadge.textContent = 'LIVE';
      statusTd.appendChild(liveBadge);
    } else {
      var endedBadge = document.createElement('span');
      endedBadge.className = 'badge badge-ended';
      endedBadge.textContent = s.status || 'ended';
      statusTd.appendChild(endedBadge);
    }
    tr.appendChild(statusTd);

    // Time cell
    tr.appendChild(td(relTime(s.created_at), { className: 'mono' }));

    frag.appendChild(tr);

    // Inline preview disclosure row (hidden by default unless expanded)
    var previewRow = document.createElement('tr');
    previewRow.className = 'session-preview-row' + (s.status === 'active' ? ' live' : '');
    previewRow.style.display = isExpanded ? '' : 'none';

    var previewTd = document.createElement('td');
    previewTd.setAttribute('colspan', '6');

    // Wire chevron click handler (needs reference to previewRow + previewTd)
    chevron.addEventListener('click', (function(sid, ch, pr, ptd) {
      return function(e) {
        e.stopPropagation();
        _toggleSessionPreview(sid, ch, pr, ptd);
      };
    })(s.session_id, chevron, previewRow, previewTd));

    // If already expanded on load, populate the preview. _fetchSessionPreview
    // will render from the cache synchronously if this session has been
    // fetched already (e.g. on a prior render of the same sessions panel),
    // or issue a fresh request otherwise.
    if (isExpanded) {
      _fetchSessionPreview(s.session_id, previewTd);
    }

    previewRow.appendChild(previewTd);
    frag.appendChild(previewRow);
  });
  body.appendChild(frag);
  renderActiveSessionsFilesPanel();
}

function resumeHarnessMeta(harness) {
  var raw = (harness || '').toLowerCase();
  if (raw === 'claude-code' || raw === 'claude') return { label: 'Claude', className: 'badge-cli-claude-code', rowClass: 'harness-claude-code' };
  if (raw === 'codex') return { label: 'Codex', className: 'badge-cli-codex', rowClass: 'harness-codex' };
  if (raw === 'gemini') return { label: 'Gemini', className: 'badge-cli-gemini', rowClass: 'harness-gemini' };
  if (raw === 'antigravity' || raw === 'openai') return { label: harness, className: 'badge-cli-openai', rowClass: '' };
  return { label: harness || '--', className: '', rowClass: '' };
}

function resumeTypeBadge(type) {
  var classes = {
    feature: 'badge-ip',
    bug: 'badge-blocked',
    spike: 'badge-todo',
    track: 'badge-done',
  };
  return createBadge(type || 'work', classes[type] || 'badge-todo');
}

function renderResumableSessions() {
  var body = document.getElementById('resume-body');
  var count = document.getElementById('resume-count');
  var empty = document.getElementById('resume-empty');
  var loading = document.getElementById('resume-loading');
  var error = document.getElementById('resume-error');
  var table = document.getElementById('resume-table');
  if (!body || !count || !empty || !loading || !error || !table) return;

  count.textContent = resumableSessions.length;
  body.textContent = '';
  empty.style.display = 'none';
  loading.style.display = resumableSessionsLoading ? 'block' : 'none';
  error.style.display = resumableSessionsError ? 'block' : 'none';
  table.style.display = 'none';

  if (resumableSessionsLoading) return;
  if (resumableSessionsError) {
    error.firstElementChild.textContent = resumableSessionsError;
    return;
  }
  if (resumableSessions.length === 0) {
    empty.style.display = 'block';
    return;
  }

  table.style.display = '';
  var frag = document.createDocumentFragment();
  resumableSessions.forEach(function(item) {
    var harnessMeta = resumeHarnessMeta(item.harness);
    var tr = document.createElement('tr');
    tr.className = 'resume-row' + (item.live ? ' live' : '') + (harnessMeta.rowClass ? ' ' + harnessMeta.rowClass : '');

    var workTd = document.createElement('td');
    var title = document.createElement('div');
    title.className = 'resume-item-title';
    title.textContent = item.title || item.work_item_id || 'Untitled';
    workTd.appendChild(title);

    var meta = document.createElement('div');
    meta.className = 'resume-item-meta';
    var workID = document.createElement('span');
    workID.className = 'mono';
    workID.textContent = item.work_item_id || '--';
    meta.appendChild(workID);
    meta.appendChild(resumeTypeBadge(item.type));
    workTd.appendChild(meta);
    tr.appendChild(workTd);

    tr.appendChild(td(item.branch || '--', { className: 'mono' }));
    tr.appendChild(td(item.exec_worktree_path || '--', { className: 'mono ellipsis resume-worktree', title: item.exec_worktree_path || '--' }));

    var harnessTd = document.createElement('td');
    if (harnessMeta.className) {
      harnessTd.appendChild(createBadge(harnessMeta.label, 'badge-cli ' + harnessMeta.className));
    } else {
      harnessTd.textContent = harnessMeta.label;
    }
    tr.appendChild(harnessTd);

    tr.appendChild(td(relTime(item.last_activity), { className: 'mono', title: item.last_activity || '--' }));
    tr.appendChild(td(truncId(item.last_session_id), { className: 'mono', title: item.last_session_id || '--' }));

    var stateTd = document.createElement('td');
    if (item.live) {
      var liveBadge = document.createElement('span');
      liveBadge.className = 'badge-live';
      liveBadge.textContent = 'LIVE';
      stateTd.appendChild(liveBadge);
    } else {
      stateTd.appendChild(createBadge('Idle', 'badge-ended'));
    }
    tr.appendChild(stateTd);
    frag.appendChild(tr);
  });
  body.appendChild(frag);
}

function renderActiveSessionsFilesPanel() {
  var panel = document.getElementById('active-sessions-files-panel');
  var body = document.getElementById('active-sessions-files-body');
  if (!panel || !body) return;

  var active = sessions.filter(function(s) { return s.status === 'active'; });
  if (active.length === 0) {
    panel.style.display = 'none';
    return;
  }
  panel.style.display = '';
  body.textContent = '';

  var frag = document.createDocumentFragment();
  active.forEach(function(s) {
    var block = document.createElement('div');
    block.className = 'asf-session-block';

    // Session header: id + agent + model
    var hdr = document.createElement('div');
    hdr.className = 'asf-session-header';
    var idSpan = document.createElement('span');
    idSpan.className = 'asf-session-id';
    idSpan.textContent = (s.session_id || '').substring(0, 18);
    hdr.appendChild(idSpan);
    var metaSpan = document.createElement('span');
    metaSpan.className = 'asf-session-meta';
    metaSpan.textContent = [s.agent, s.model].filter(Boolean).join(' / ');
    hdr.appendChild(metaSpan);
    block.appendChild(hdr);

    // Work item (claim)
    if (s.feature_id) {
      var wi = document.createElement('div');
      wi.className = 'asf-work-item';
      wi.textContent = 'Work item: ' + s.feature_id;
      block.appendChild(wi);
    }

    // Files touched
    var files = Array.isArray(s.files) ? s.files : [];
    if (files.length === 0) {
      var noFiles = document.createElement('div');
      noFiles.className = 'asf-no-files';
      noFiles.textContent = 'No files recorded yet.';
      block.appendChild(noFiles);
    } else {
      var ul = document.createElement('ul');
      ul.className = 'asf-files-list';
      files.slice(0, 20).forEach(function(f) {
        var li = document.createElement('li');
        li.className = 'asf-file-row';
        var opSpan = document.createElement('span');
        opSpan.className = 'asf-file-op';
        opSpan.textContent = f.operation || '-';
        li.appendChild(opSpan);
        var pathSpan = document.createElement('span');
        pathSpan.className = 'asf-file-path';
        pathSpan.textContent = f.file_path || '';
        pathSpan.title = f.file_path || '';
        li.appendChild(pathSpan);
        ul.appendChild(li);
      });
      if (files.length > 20) {
        var more = document.createElement('li');
        more.className = 'asf-file-row asf-no-files';
        more.textContent = '… and ' + (files.length - 20) + ' more';
        ul.appendChild(more);
      }
      block.appendChild(ul);
    }

    frag.appendChild(block);
  });
  body.appendChild(frag);
}

function renderSessionAdherenceTrend(forceEmpty) {
  var panel = document.getElementById('session-trend-panel');
  if (!panel) return;
  panel.textContent = '';

  var points = sessionAdherenceTrend || [];
  if (forceEmpty || points.length === 0) {
    var empty = document.createElement('div');
    empty.className = 'session-trend-empty';
    empty.textContent = 'No adherence trend available yet.';
    panel.appendChild(empty);
    return;
  }

  var header = document.createElement('div');
  header.className = 'session-trend-header';
  var title = document.createElement('div');
  title.className = 'session-trend-title';
  title.textContent = 'Adherence Trend';
  header.appendChild(title);
  var meta = document.createElement('div');
  meta.className = 'session-trend-meta';
  meta.textContent = points.length + ' completed sessions';
  header.appendChild(meta);
  panel.appendChild(header);

  var chart = document.createElement('div');
  chart.className = 'session-trend-chart';
  var maxScore = 100;
  points.forEach(function(point) {
    var row = document.createElement('div');
    row.className = 'session-trend-row';

    var label = document.createElement('span');
    label.className = 'session-trend-label';
    label.textContent = relTime(point.created_at || point.completed_at || '');
    label.title = point.session_id || '';
    row.appendChild(label);

    var track = document.createElement('div');
    track.className = 'session-trend-track';
    var fill = document.createElement('div');
    fill.className = 'session-trend-fill';
    fill.style.width = Math.max(0, Math.min(100, point.score || 0)) + '%';
    fill.title = (point.session_id || 'session') + ' - ' + (point.score || 0) + '%';
    track.appendChild(fill);
    row.appendChild(track);

    var value = document.createElement('span');
    value.className = 'session-trend-value';
    value.textContent = (point.score || 0) + '%';
    row.appendChild(value);

    chart.appendChild(row);
  });
  panel.appendChild(chart);
}

/* ── Rendering: Work (Kanban) ──────────────────────────────── */
var PRIORITY_ORDER = { critical: 0, high: 1, medium: 2, low: 3 };

var TYPE_ICONS = {
  feat:  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><polyline points="20 6 9 17 4 12"/></svg>',
  bug:   '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>',
  spk:   '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>',
  track: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="2" y="3" width="20" height="4" rx="1"/><rect x="2" y="10" width="20" height="4" rx="1"/><rect x="2" y="17" width="20" height="4" rx="1"/></svg>'
};

var COL_DEFS = [
  { status: 'todo',        label: 'Todo' },
  { status: 'in-progress', label: 'In Progress' },
  { status: 'done',        label: 'Done' }
];

function itemTypeKey(id) {
  if (!id) return 'feat';
  if (id.startsWith('bug')) return 'bug';
  if (id.startsWith('spk')) return 'spk';
  if (id.startsWith('trk')) return 'track';
  return 'feat';
}

function sortItems(items) {
  return items.slice().sort(function(a, b) {
    var pa = PRIORITY_ORDER[a.priority] != null ? PRIORITY_ORDER[a.priority] : 2;
    var pb = PRIORITY_ORDER[b.priority] != null ? PRIORITY_ORDER[b.priority] : 2;
    if (pa !== pb) return pa - pb;
    return (b.created_at || '') > (a.created_at || '') ? 1 : -1;
  });
}

function buildKanbanCard(f) {
  var card = document.createElement('div');
  card.className = 'kanban-card';
  card.dataset.itemId = f.id;
  card.style.cursor = 'pointer';
  card.addEventListener('click', function() { openWorkDetail(f.id); });

  var titleEl = document.createElement('div');
  titleEl.className = 'kanban-card-title';
  titleEl.textContent = f.title || f.id;
  titleEl.title = f.title || f.id;
  card.appendChild(titleEl);

  var meta = document.createElement('div');
  meta.className = 'kanban-card-meta';

  var typeKey = itemTypeKey(f.id);
  var iconHtml = TYPE_ICONS[typeKey] || TYPE_ICONS.feat;
  var iconWrap = document.createElement('span');
  iconWrap.className = 'type-icon';
  iconWrap.innerHTML = iconHtml;
  meta.appendChild(iconWrap);

  var idSpan = document.createElement('span');
  idSpan.textContent = f.id ? f.id.slice(0, 12) : '--';
  meta.appendChild(idSpan);

  if (f.priority) {
    var priBadge = createPriorityBadge(f.priority);
    meta.appendChild(priBadge);
  }

  // Conflict badge — SAME semantics as `wipnote who` ⚠ (claim collision or
  // a live cross-session file overlap on a file the owner touched).
  if (f.file_conflict) {
    var conf = document.createElement('span');
    conf.className = 'kanban-badge kanban-badge-conflict';
    conf.textContent = '⚠ conflict';
    conf.title = 'Live file/claim conflict — see `wipnote who`';
    meta.appendChild(conf);
  }

  card.appendChild(meta);

  // Execution-visibility row (READ-ONLY). Owner/CLI/model + liveness, step
  // progress, last-activity age, last touched file, moved/reassigned markers.
  var exec = document.createElement('div');
  exec.className = 'kanban-card-exec';

  if (f.active_session) {
    var owner = document.createElement('span');
    var harnessKey = (f.active_owner_harness || '').replace(/[^a-z0-9-]/gi, '');
    owner.className = 'kanban-badge badge-cli badge-cli-' + (harnessKey || 'claude-code') +
      (f.active_session_live ? ' is-live' : ' is-stale');
    var lbl = f.active_owner_harness || 'agent';
    if (f.active_owner_model) lbl += ' · ' + f.active_owner_model;
    owner.textContent = (f.active_session_live ? '● ' : '○ ') + lbl;
    owner.title = 'Owner session ' + f.active_session +
      (f.active_session_live ? ' (live: claim heartbeat fresh)' : ' (stale: no recent heartbeat)');
    exec.appendChild(owner);
  }

  var st = Number(f.steps_total) || 0;
  if (st > 0) {
    var steps = document.createElement('span');
    steps.className = 'kanban-badge kanban-badge-steps';
    steps.textContent = (Number(f.steps_completed) || 0) + '/' + st + ' steps';
    if (f.step_tracking_supported === false) {
      steps.classList.add('kanban-badge-unsupported');
      steps.textContent = (Number(f.steps_completed) || 0) + '/' + st + ' steps (not live)';
      steps.title = f.step_tracking_detail ||
        'Step tracking not live for this harness';
    }
    exec.appendChild(steps);
  } else if (f.step_tracking_supported === false && f.active_session) {
    var ns = document.createElement('span');
    ns.className = 'kanban-badge kanban-badge-unsupported';
    ns.textContent = 'steps n/a';
    ns.title = f.step_tracking_detail || 'Step tracking not live for this harness';
    exec.appendChild(ns);
  }

  var ageS = Number(f.last_activity_age_seconds);
  if (!isNaN(ageS) && ageS >= 0) {
    var age = document.createElement('span');
    age.className = 'kanban-badge kanban-badge-age';
    age.textContent = shortAge(ageS) + ' ago';
    if (f.last_tool) age.title = 'Last: ' + f.last_tool;
    exec.appendChild(age);
  }

  if (f.moved_recently) {
    var mv = document.createElement('span');
    mv.className = 'kanban-badge kanban-badge-moved';
    mv.textContent = 'moved';
    exec.appendChild(mv);
  }
  if (f.reassigned_recently) {
    var ra = document.createElement('span');
    ra.className = 'kanban-badge kanban-badge-moved';
    ra.textContent = 'reassigned';
    exec.appendChild(ra);
  }

  if (exec.childNodes.length > 0) card.appendChild(exec);

  if (f.last_touched_file) {
    var lf = document.createElement('div');
    lf.className = 'kanban-card-file';
    lf.textContent = f.last_touched_file;
    lf.title = f.last_touched_file;
    card.appendChild(lf);
  }

  return card;
}

// shortAge renders an age in seconds as a compact human string (no overflow).
function shortAge(s) {
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}

function buildKanbanColumns(items) {
  var buckets = { 'todo': [], 'in-progress': [], 'done': [] };
  items.forEach(function(f) {
    var s = f.status || 'todo';
    if (!buckets[s]) s = 'todo';
    buckets[s].push(f);
  });

  var frag = document.createDocumentFragment();
  COL_DEFS.forEach(function(col) {
    var sorted = sortItems(buckets[col.status] || []);
    var colEl = document.createElement('div');
    colEl.className = 'kanban-col';
    colEl.dataset.status = col.status;

    var header = document.createElement('div');
    header.className = 'kanban-col-header';
    var labelSpan = document.createElement('span');
    labelSpan.textContent = col.label;
    header.appendChild(labelSpan);
    var countBadge = document.createElement('span');
    countBadge.className = 'col-count';
    countBadge.textContent = sorted.length;
    header.appendChild(countBadge);
    colEl.appendChild(header);

    var cardsEl = document.createElement('div');
    cardsEl.className = 'kanban-cards';
    sorted.forEach(function(f) { cardsEl.appendChild(buildKanbanCard(f)); });
    colEl.appendChild(cardsEl);
    frag.appendChild(colEl);
  });
  return frag;
}

function buildTrackSection(trackId, trackTitle, items) {
  var doneCount = items.filter(function(f) { return f.status === 'done'; }).length;
  var collapseKey = 'wipnote-track-collapsed-' + trackId;
  var isCollapsed = localStorage.getItem(collapseKey) === 'true';

  var section = document.createElement('div');
  section.className = 'track-section';

  var sectionHeader = document.createElement('div');
  sectionHeader.className = 'track-section-header';

  var titleSpan = document.createElement('span');
  titleSpan.className = 'track-section-title';
  titleSpan.textContent = trackTitle || trackId;
  sectionHeader.appendChild(titleSpan);

  var activeCount = items.filter(function(f) {
    return f.active_session && f.active_session_live;
  }).length;
  var staleCount = items.filter(function(f) {
    return f.status === 'blocked' ||
      (f.status === 'in-progress' && f.active_session && !f.active_session_live);
  }).length;
  var movedCount = items.filter(function(f) {
    return f.moved_recently || f.reassigned_recently;
  }).length;

  if (activeCount > 0) {
    var aBadge = document.createElement('span');
    aBadge.className = 'track-section-stat track-stat-active';
    aBadge.textContent = activeCount + ' active';
    sectionHeader.appendChild(aBadge);
  }
  if (staleCount > 0) {
    var sBadge = document.createElement('span');
    sBadge.className = 'track-section-stat track-stat-stale';
    sBadge.textContent = staleCount + ' blocked/stale';
    sectionHeader.appendChild(sBadge);
  }
  if (movedCount > 0) {
    var mBadge = document.createElement('span');
    mBadge.className = 'track-section-stat track-stat-moved';
    mBadge.textContent = movedCount + ' moved';
    sectionHeader.appendChild(mBadge);
  }

  var progressSpan = document.createElement('span');
  progressSpan.className = 'track-section-progress';
  progressSpan.textContent = doneCount + '/' + items.length + ' done';
  sectionHeader.appendChild(progressSpan);

  var chevron = document.createElement('span');
  chevron.className = 'track-section-toggle' + (isCollapsed ? ' collapsed' : '');
  chevron.textContent = '\u25BE';
  sectionHeader.appendChild(chevron);

  var body = document.createElement('div');
  body.className = 'track-section-body kanban-board' + (isCollapsed ? ' collapsed' : '');
  body.appendChild(buildKanbanColumns(items));

  sectionHeader.addEventListener('click', function() {
    isCollapsed = !isCollapsed;
    chevron.classList.toggle('collapsed', isCollapsed);
    body.classList.toggle('collapsed', isCollapsed);
    localStorage.setItem(collapseKey, isCollapsed ? 'true' : 'false');
  });

  section.appendChild(sectionHeader);
  section.appendChild(body);
  return section;
}

function renderKanban() {
  var board = document.getElementById('kanban-board');
  var empty = document.getElementById('work-empty');
  document.getElementById('work-count').textContent = features.length;
  board.textContent = '';

  var toggleBtn = document.getElementById('track-group-toggle');
  if (toggleBtn) toggleBtn.classList.toggle('active', groupByTrack);

  if (features.length === 0) { empty.style.display = ''; return; }
  empty.style.display = 'none';

  var frag = document.createDocumentFragment();

  if (!groupByTrack) {
    frag.appendChild(buildKanbanColumns(features));
  } else {
    var trackMap = {};
    var trackOrder = [];
    var untracked = [];

    features.forEach(function(f) {
      if (!f.track_id) { untracked.push(f); return; }
      if (!trackMap[f.track_id]) {
        trackMap[f.track_id] = { title: f.track_title || f.track_id, items: [] };
        trackOrder.push(f.track_id);
      }
      trackMap[f.track_id].items.push(f);
    });

    trackOrder.forEach(function(tid) {
      var t = trackMap[tid];
      frag.appendChild(buildTrackSection(tid, t.title, t.items));
    });

    if (untracked.length > 0) {
      frag.appendChild(buildTrackSection('untracked', 'Untracked', untracked));
    }
  }

  board.appendChild(frag);
}

/* ── Rendering: Agents ─────────────────────────────────────── */
function renderAgents() {
  var body = document.getElementById('agents-body');
  var empty = document.getElementById('agents-empty');
  body.textContent = '';
  if (events.length === 0) {
    empty.style.display = '';
    document.getElementById('agents-count').textContent = '0';
    return;
  }
  empty.style.display = 'none';

  var agentMap = {};
  events.forEach(function(e) {
    var aid = e.agent_id || 'unknown';
    if (!agentMap[aid]) agentMap[aid] = { count: 0, lastTs: '', tools: {} };
    agentMap[aid].count++;
    if (e.timestamp > agentMap[aid].lastTs) agentMap[aid].lastTs = e.timestamp;
    var tool = e.tool_name || e.event_type || 'other';
    agentMap[aid].tools[tool] = (agentMap[aid].tools[tool] || 0) + 1;
  });

  var sorted = Object.keys(agentMap).map(function(k) { return [k, agentMap[k]]; })
    .sort(function(a, b) { return b[1].count - a[1].count; });
  document.getElementById('agents-count').textContent = sorted.length;

  var frag = document.createDocumentFragment();
  sorted.forEach(function(pair) {
    var aid = pair[0];
    var info = pair[1];
    var topTools = Object.keys(info.tools).map(function(t) { return [t, info.tools[t]]; })
      .sort(function(a, b) { return b[1] - a[1]; })
      .slice(0, 4)
      .map(function(pair) { return pair[0] + '(' + pair[1] + ')'; })
      .join(', ');
    var tr = document.createElement('tr');
    tr.appendChild(td(aid, { style: 'color:var(--text-primary);font-weight:500' }));
    tr.appendChild(td(String(info.count)));
    tr.appendChild(td(relTime(info.lastTs), { className: 'mono' }));
    tr.appendChild(td(topTools, { className: 'ellipsis', style: 'color:var(--text-muted)' }));
    frag.appendChild(tr);
  });
  body.appendChild(frag);
}

/* ── Rendering: Metrics ────────────────────────────────────── */
function renderMetrics() {
  var emptyEl = document.getElementById('metrics-empty');
  if (events.length === 0) { emptyEl.style.display = ''; return; }
  emptyEl.style.display = 'none';
  renderBarChart('chart-tools', bucketBy(events, function(e) { return e.tool_name || e.event_type || 'other'; }));
  renderBarChart('chart-agents', bucketBy(events, function(e) { return e.agent_id || 'unknown'; }));
  renderHoursChart();
}

function bucketBy(arr, keyFn) {
  var m = {};
  arr.forEach(function(e) { var k = keyFn(e); m[k] = (m[k] || 0) + 1; });
  return Object.keys(m).map(function(k) { return [k, m[k]]; })
    .sort(function(a, b) { return b[1] - a[1]; });
}

function renderBarChart(containerId, entries) {
  var el = document.getElementById(containerId);
  el.textContent = '';
  if (entries.length === 0) {
    var msg = document.createElement('div');
    msg.className = 'empty';
    msg.textContent = 'No data';
    el.appendChild(msg);
    return;
  }
  var maxVal = entries[0][1];
  var frag = document.createDocumentFragment();
  entries.slice(0, 15).forEach(function(pair) {
    var label = pair[0];
    var count = pair[1];
    var pct = maxVal > 0 ? (count / maxVal) * 100 : 0;
    var row = document.createElement('div');
    row.className = 'bar-row';
    var lblSpan = document.createElement('span');
    lblSpan.className = 'label';
    lblSpan.title = label;
    lblSpan.textContent = label;
    row.appendChild(lblSpan);
    var track = document.createElement('div');
    track.className = 'bar-track';
    var fill = document.createElement('div');
    fill.className = 'bar-fill';
    fill.style.width = pct + '%';
    track.appendChild(fill);
    row.appendChild(track);
    var valSpan = document.createElement('span');
    valSpan.className = 'val';
    valSpan.textContent = count;
    row.appendChild(valSpan);
    frag.appendChild(row);
  });
  el.appendChild(frag);
}

function renderHoursChart() {
  var now = Date.now();
  var keys = [];
  var buckets = {};
  for (var h = 23; h >= 0; h--) {
    var d = new Date(now - h * 3600000);
    var key = String(d.getHours()).padStart(2, '0') + ':00';
    keys.push(key);
    buckets[key] = 0;
  }
  events.forEach(function(e) {
    if (!e.timestamp) return;
    var d = new Date(e.timestamp.indexOf('T') >= 0 ? e.timestamp : e.timestamp.replace(' ', 'T') + 'Z');
    if (now - d.getTime() > 86400000) return;
    var key = String(d.getHours()).padStart(2, '0') + ':00';
    if (key in buckets) buckets[key]++;
  });
  var entries = keys.map(function(k) { return [k, buckets[k]]; });
  renderBarChart('chart-hours', entries);
}

/* ── Work item detail panel ────────────────────────────────── */
function closeWorkDetail() {
  var detail = document.getElementById('work-detail');
  var board = document.getElementById('kanban-board');
  var empty = document.getElementById('work-empty');
  var viewTitle = document.querySelector('#v-work .view-title');
  detail.classList.remove('active');
  board.style.display = '';
  if (viewTitle) viewTitle.style.display = '';
  // If features haven't been loaded yet (e.g. user navigated directly
  // from the graph view), fetch them now instead of showing an empty board.
  if (features.length === 0) {
    fetchFeatures();
  }
}

function openWorkDetail(id) {
  // Track the active work item so the terminal button can pre-scope sessions.
  window.wipnoteActiveWorkItem = id;

  var detail = document.getElementById('work-detail');
  var board = document.getElementById('kanban-board');
  var empty = document.getElementById('work-empty');
  var content = document.getElementById('work-detail-content');
  var viewTitle = document.querySelector('#v-work .view-title');

  // Hide board, show detail panel
  board.style.display = 'none';
  empty.style.display = 'none';
  if (viewTitle) viewTitle.style.display = 'none';
  detail.classList.add('active');
  content.textContent = '';

  // Loading indicator
  var loading = document.createElement('div');
  loading.className = 'empty';
  loading.textContent = 'Loading...';
  content.appendChild(loading);

  fetch(buildProjectUrl('features/detail', 'id=' + encodeURIComponent(id)))
    .then(function(r) {
      if (!r.ok) throw new Error('Not found');
      return r.json();
    })
    .then(function(node) {
      content.textContent = '';
      renderWorkDetail(content, node);
    })
    .catch(function() {
      content.textContent = '';
      var err = document.createElement('div');
      err.className = 'empty';
      err.textContent = 'Could not load item: ' + id;
      content.appendChild(err);
    });
}

function renderWorkDetail(container, node) {
  // Type badge + title
  var typeKey = itemTypeKey(node.id);
  var typeBadge = document.createElement('span');
  typeBadge.className = 'badge badge-' + (typeKey === 'feat' ? 'ip' : typeKey === 'bug' ? 'error' : 'todo');
  typeBadge.style.marginBottom = '8px';
  typeBadge.style.display = 'inline-block';
  typeBadge.textContent = typeKey.toUpperCase();
  container.appendChild(typeBadge);

  var titleEl = document.createElement('h2');
  titleEl.className = 'work-detail-title';
  titleEl.textContent = node.title || node.id;
  container.appendChild(titleEl);

  var idEl = document.createElement('div');
  idEl.className = 'work-detail-id';
  idEl.textContent = node.id;
  container.appendChild(idEl);

  // Status + priority badges
  var badges = document.createElement('div');
  badges.className = 'work-detail-badges';
  if (node.status) {
    var statusBadge = document.createElement('span');
    var statusClass = node.status === 'in-progress' ? 'ip' : node.status === 'done' ? 'done' : 'todo';
    statusBadge.className = 'badge badge-' + statusClass;
    statusBadge.textContent = node.status;
    badges.appendChild(statusBadge);
  }
  if (node.priority) {
    var priBadge = createPriorityBadge(node.priority);
    badges.appendChild(priBadge);
  }
  if (badges.childNodes.length > 0) container.appendChild(badges);

  // Content / findings
  if (node.content) {
    var contentSection = document.createElement('div');
    contentSection.className = 'work-detail-section';
    var contentTitle = document.createElement('div');
    contentTitle.className = 'work-detail-section-title';
    contentTitle.textContent = 'Findings';
    contentSection.appendChild(contentTitle);
    var contentBody = document.createElement('div');
    contentBody.className = 'work-detail-content';
    contentBody.textContent = node.content;
    contentSection.appendChild(contentBody);
    container.appendChild(contentSection);
  }

  // Track info
  if (node.track_id) {
    var trackSection = document.createElement('div');
    trackSection.className = 'work-detail-section';
    var trackLabel = document.createElement('div');
    trackLabel.className = 'work-detail-section-title';
    trackLabel.textContent = 'Track';
    trackSection.appendChild(trackLabel);
    var trackLink = document.createElement('div');
    trackLink.className = 'work-detail-track-link';
    trackLink.textContent = node.track_id;
    trackLink.addEventListener('click', function() { openWorkDetail(node.track_id); });
    trackSection.appendChild(trackLink);
    container.appendChild(trackSection);
  }

  // Steps
  if (node.steps && node.steps.length > 0) {
    var stepsSection = document.createElement('div');
    stepsSection.className = 'work-detail-section';
    var stepsLabel = document.createElement('div');
    stepsLabel.className = 'work-detail-section-title';
    stepsLabel.textContent = 'Steps (' + node.steps.filter(function(s) { return s.completed; }).length + '/' + node.steps.length + ')';
    stepsSection.appendChild(stepsLabel);
    var stepsList = document.createElement('ul');
    stepsList.className = 'work-detail-steps';
    node.steps.forEach(function(step) {
      var li = document.createElement('li');
      var icon = document.createElement('span');
      if (step.completed) {
        icon.className = 'step-done';
        icon.textContent = '\u2713';
      } else {
        icon.className = 'step-pending';
        icon.textContent = '\u25CB';
      }
      li.appendChild(icon);
      var text = document.createElement('span');
      text.textContent = step.description || step.step_id || '';
      if (step.completed) text.style.textDecoration = 'line-through';
      li.appendChild(text);

      // Step provenance (agent/CLI/session/timestamp). Surfaced for
      // task-created & task-completed (hook-driven) steps; gracefully
      // absent for hand-authored steps with no provenance.
      var provBits = [];
      if (step.agent) provBits.push(step.agent);
      if (step.created_by_model) provBits.push(step.created_by_model);
      if (step.created_by_cli_version) provBits.push('v' + step.created_by_cli_version);
      if (step.timestamp) provBits.push(relTime(step.timestamp));
      if (provBits.length > 0) {
        var prov = document.createElement('span');
        prov.className = 'step-provenance';
        prov.textContent = ' · ' + provBits.join(' / ');
        var stepIdHint = step.step_id || '';
        if (stepIdHint.indexOf('task-') === 0) prov.title = 'Hook-tracked step (' + stepIdHint + ')';
        li.appendChild(prov);
      }
      stepsList.appendChild(li);
    });
    stepsSection.appendChild(stepsList);
    container.appendChild(stepsSection);
  }

  // Edges
  if (node.edges && Object.keys(node.edges).length > 0) {
    var edgesSection = document.createElement('div');
    edgesSection.className = 'work-detail-section';
    var edgesLabel = document.createElement('div');
    edgesLabel.className = 'work-detail-section-title';
    edgesLabel.textContent = 'Relationships';
    edgesSection.appendChild(edgesLabel);
    var edgesContainer = document.createElement('div');
    edgesContainer.className = 'work-detail-edges';
    Object.keys(node.edges).forEach(function(relType) {
      var edgeList = node.edges[relType];
      if (!Array.isArray(edgeList)) return;
      edgeList.forEach(function(edge) {
        var edgeEl = document.createElement('div');
        edgeEl.className = 'work-detail-edge';
        var typeSpan = document.createElement('span');
        typeSpan.className = 'edge-type';
        typeSpan.textContent = relType.replace(/_/g, ' ');
        edgeEl.appendChild(typeSpan);
        var targetSpan = document.createElement('span');
        targetSpan.className = 'edge-target';
        targetSpan.textContent = (edge.target_id || '').slice(0, 16);
        edgeEl.appendChild(targetSpan);
        if (edge.title) {
          var titleSpan = document.createElement('span');
          titleSpan.className = 'edge-title';
          titleSpan.textContent = edge.title;
          edgeEl.appendChild(titleSpan);
        }
        edgeEl.addEventListener('click', function() {
          if (edge.target_id) openWorkDetail(edge.target_id);
        });
        edgesContainer.appendChild(edgeEl);
      });
    });
    edgesSection.appendChild(edgesContainer);
    container.appendChild(edgesSection);
  }

  // Related features (async) — section only shown when results exist
  var relatedSection = document.createElement('div');
  relatedSection.className = 'work-detail-section';
  var relatedLabel = document.createElement('div');
  relatedLabel.className = 'work-detail-section-title';
  relatedLabel.textContent = 'Related (Shared Files)';
  relatedSection.appendChild(relatedLabel);
  var relatedContainer = document.createElement('div');
  relatedContainer.className = 'work-detail-related';
  relatedSection.appendChild(relatedContainer);

  fetch(buildProjectUrl('features/related', 'feature_id=' + encodeURIComponent(node.id)))
    .then(function(r) { return r.ok ? r.json() : []; })
    .then(function(related) {
      if (!related || related.length === 0) {
        return;
      }
      container.appendChild(relatedSection);
      related.forEach(function(rel) {
        var relEl = document.createElement('div');
        relEl.className = 'work-detail-related-item';
        var idSpan = document.createElement('span');
        idSpan.className = 'rel-id';
        idSpan.textContent = (rel.feature_id || rel.id || '').slice(0, 16);
        relEl.appendChild(idSpan);
        var titleSpan = document.createElement('span');
        titleSpan.className = 'rel-title';
        titleSpan.textContent = rel.title || rel.feature_id || '';
        relEl.appendChild(titleSpan);
        var fid = rel.feature_id || rel.id;
        if (fid) relEl.addEventListener('click', function() { openWorkDetail(fid); });
        relatedContainer.appendChild(relEl);
      });
    })
    .catch(function() { /* no related section on error */ });

  // Activity data — async, three collapsible panels: Commits / Files / Activity
  fetch(buildProjectUrl('features/' + encodeURIComponent(node.id) + '/activity'))
    .then(function(r) { return r.ok ? r.json() : null; })
    .then(function(data) {
      if (!data) return;

      var hasCommits  = data.commits    && data.commits.length > 0;
      var hasFiles    = data.file_edits && data.file_edits.length > 0;
      var hasActivity = data.total_events > 0;
      if (!hasCommits && !hasFiles && !hasActivity) return;

      // helper: build a collapsible section panel
      function makePanel(headerText, count) {
        var section = document.createElement('div');
        section.className = 'work-detail-section activity-panel';

        var header = document.createElement('div');
        header.className = 'work-detail-section-title activity-panel-header';
        header.style.cssText = 'cursor:pointer;display:flex;align-items:center;justify-content:space-between;';

        var labelSpan = document.createElement('span');
        labelSpan.textContent = headerText + (count != null ? ' (' + count + ')' : '');
        header.appendChild(labelSpan);

        var chevron = document.createElement('span');
        chevron.textContent = '\u25BE';
        chevron.style.cssText = 'font-size:0.8em;margin-left:6px;transition:transform 0.15s;';
        header.appendChild(chevron);

        var body = document.createElement('div');
        body.className = 'activity-panel-body';

        var collapsed = false;
        header.addEventListener('click', function() {
          collapsed = !collapsed;
          body.style.display = collapsed ? 'none' : '';
          chevron.style.transform = collapsed ? 'rotate(-90deg)' : '';
        });

        section.appendChild(header);
        section.appendChild(body);
        return { section: section, body: body };
      }

      // Commits panel
      if (hasCommits) {
        var cp = makePanel('Commits', data.commits.length);
        data.commits.forEach(function(c) {
          var row = document.createElement('div');
          row.className = 'activity-commit-row';
          row.title = 'Click to copy SHA';
          row.style.cssText = 'display:flex;align-items:flex-start;gap:8px;padding:4px 0;cursor:pointer;';

          var shaEl = document.createElement('code');
          shaEl.className = 'activity-commit-sha';
          shaEl.textContent = (c.sha || '').slice(0, 7);
          shaEl.style.cssText = 'font-size:0.75rem;background:var(--bg-tertiary);padding:1px 5px;border-radius:3px;white-space:nowrap;flex-shrink:0;';
          row.appendChild(shaEl);

          var subjectEl = document.createElement('span');
          subjectEl.className = 'activity-commit-subject';
          subjectEl.textContent = c.subject || '';
          subjectEl.style.cssText = 'font-size:0.8rem;flex:1;word-break:break-word;';
          row.appendChild(subjectEl);

          var tsEl = document.createElement('span');
          tsEl.className = 'activity-commit-time';
          tsEl.textContent = relTime(c.timestamp);
          tsEl.style.cssText = 'font-size:0.7rem;color:var(--text-muted);white-space:nowrap;flex-shrink:0;';
          row.appendChild(tsEl);

          row.addEventListener('click', function() {
            navigator.clipboard && navigator.clipboard.writeText(c.sha || '').catch(function() {});
          });

          cp.body.appendChild(row);
        });
        container.appendChild(cp.section);
      }

      // Files panel
      if (hasFiles) {
        var fp = makePanel('Files', data.file_edits.length);
        data.file_edits.forEach(function(fe) {
          var row = document.createElement('div');
          row.className = 'activity-file-item';
          row.style.cssText = 'display:flex;align-items:center;gap:8px;padding:3px 0;';

          var pathEl = document.createElement('span');
          pathEl.className = 'activity-file-path';
          var parts = (fe.file_path || '').split('/');
          pathEl.textContent = parts.slice(-3).join('/') || fe.file_path;
          pathEl.title = fe.file_path;
          pathEl.style.cssText = 'font-size:0.8rem;flex:1;word-break:break-all;';
          row.appendChild(pathEl);

          var countBadge = document.createElement('span');
          countBadge.className = 'activity-file-count';
          countBadge.textContent = fe.edit_count + 'x';
          countBadge.style.cssText = 'font-size:0.7rem;background:var(--bg-tertiary);padding:1px 6px;border-radius:10px;white-space:nowrap;';
          row.appendChild(countBadge);

          var lastEl = document.createElement('span');
          lastEl.className = 'activity-file-last';
          lastEl.textContent = relTime(fe.last_edit);
          lastEl.style.cssText = 'font-size:0.7rem;color:var(--text-muted);white-space:nowrap;';
          row.appendChild(lastEl);

          row.addEventListener('click', function() {
            console.log('[wipnote] file trace:', fe.file_path);
          });

          fp.body.appendChild(row);
        });
        container.appendChild(fp.section);
      }

      // Activity (timeline) panel
      if (hasActivity) {
        var ap = makePanel('Activity', data.total_events);

        if (data.sessions && data.sessions.length > 0) {
          var sessDiv = document.createElement('div');
          sessDiv.className = 'activity-stat';
          sessDiv.style.marginBottom = '6px';
          sessDiv.innerHTML = 'across <strong>' + data.sessions.length + '</strong> session' + (data.sessions.length === 1 ? '' : 's');
          ap.body.appendChild(sessDiv);
        }

        if (data.events && data.events.length > 0) {
          var timeline = document.createElement('div');
          timeline.className = 'activity-timeline';

          data.events.forEach(function(ev) {
            var evRow = document.createElement('div');
            evRow.className = 'activity-event';
            evRow.title = ev.input_summary || ev.tool_name || '';

            var tsEl = document.createElement('span');
            tsEl.className = 'activity-event-time';
            var ts = ev.timestamp || '';
            var timePart = ts.indexOf('T') >= 0 ? ts.split('T')[1] : ts;
            tsEl.textContent = timePart ? timePart.slice(0, 8) : ts.slice(0, 10);
            evRow.appendChild(tsEl);

            var toolEl = document.createElement('span');
            var toolName = ev.tool_name || ev.event_type || '?';
            var toolKey = ['Edit','Read','Write','Bash','Glob','Grep'].indexOf(toolName) >= 0 ? toolName : 'default';
            toolEl.className = 'activity-event-tool activity-event-tool-' + toolKey;
            toolEl.textContent = toolName.slice(0, 12);
            evRow.appendChild(toolEl);

            var sumEl = document.createElement('span');
            sumEl.className = 'activity-event-summary';
            sumEl.textContent = (ev.input_summary || '').slice(0, 80);
            evRow.appendChild(sumEl);

            if (ev.session_id) {
              evRow.addEventListener('click', function() {
                openSessionDetail(ev.session_id);
              });
            }

            timeline.appendChild(evRow);
          });
          ap.body.appendChild(timeline);
        }
        container.appendChild(ap.section);
      }
    })
    .catch(function() { /* activity panels hidden on error */ });
}

/* ── Init ──────────────────────────────────────────────────── */
document.addEventListener('DOMContentLoaded', function() {
  var toggleBtn = document.getElementById('track-group-toggle');
  if (toggleBtn) {
    toggleBtn.classList.toggle('active', groupByTrack);
    toggleBtn.addEventListener('click', function() {
      groupByTrack = !groupByTrack;
      localStorage.setItem('wipnote-kanban-group-by-track', groupByTrack ? 'true' : 'false');
      renderKanban();
    });
  }

  var backBtn = document.getElementById('work-detail-back');
  if (backBtn) {
    backBtn.addEventListener('click', closeWorkDetail);
  }

  var planBackBtn = document.getElementById('plan-detail-back');
  if (planBackBtn) {
    planBackBtn.addEventListener('click', closePlanDetail);
  }

  var planFilter = document.getElementById('plan-status-filter');
  if (planFilter) {
    planFilter.addEventListener('change', function() {
      var val = this.value;
      renderPlans(val === 'all' ? plans : plans.filter(function(p) { return p.status === val; }));
    });
  }
});

// Theme toggle — single source of truth for dashboard theme
(function() {
  // Clean up stale theme keys from the old plan review system
  localStorage.removeItem('crispi-theme');
  localStorage.removeItem('theme');

  var btn = document.getElementById('theme-toggle');
  if (!btn) return;
  var saved = localStorage.getItem('wipnote-theme');
  if (!saved) {
    saved = (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) ? 'light' : 'dark';
    localStorage.setItem('wipnote-theme', saved);
  }
  document.documentElement.dataset.theme = saved;
  btn.textContent = saved === 'light' ? '\u263E' : '\u2600';
  btn.addEventListener('click', function() {
    var current = document.documentElement.dataset.theme || 'dark';
    var next = current === 'dark' ? 'light' : 'dark';
    window._wipnoteTheme = next;
    document.documentElement.dataset.theme = next;
    localStorage.setItem('wipnote-theme', next);
    btn.textContent = next === 'light' ? '\u263E' : '\u2600';
  });

  // Re-assert theme after any dynamic content injection (plan scripts may alter it)
  window._wipnoteTheme = saved;
  var observer = new MutationObserver(function() {
    if (document.documentElement.dataset.theme !== window._wipnoteTheme) {
      document.documentElement.dataset.theme = window._wipnoteTheme;
    }
  });
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
})();

/* ── Doorway mode: projects landing (root) vs single-project (/p/<id>/) ── */

// isDoorwayLanding returns true when the dashboard is loaded at the
// server root ("/") with no /p/<id>/ prefix. In this mode it shows the
// projects landing and clicking a card navigates to /p/<id>/ with a full
// page load — there is no SPA drill-in.
function isDoorwayLanding() {
  return window.location.pathname.indexOf('/p/') !== 0;
}

// detectMode calls /api/mode. When loaded at the doorway (root), it
// receives {"mode":"global"} and renders the projects landing. When
// loaded under /p/<id>/, it receives {"mode":"single"} (from the child)
// and proceeds with the regular single-project startup.
function detectMode() {
  // IMPORTANT: use buildProjectUrl so under /p/<id>/ this hits the child's
  // /api/mode (which carries projectName), not the parent's global mode.
  return fetch(buildProjectUrl('mode')).then(function(r) {
    if (!r.ok) return null;
    return r.json();
  }).then(function(data) {
    if (!data) return;
    window.wipnoteMode = data.mode;
    // Signal that the mode has been confirmed by the server. The event-tree
    // connectedCallback uses this flag to distinguish a real 'single' mode
    // from the initial default value (which is also 'single').
    window._wipnoteModeResolved = true;
    if (data.mode === 'global' && isDoorwayLanding()) {
      return loadAndRenderProjectsLanding();
    }
    // Inside a project (single mode served by a child under /p/<id>/)
    // — label the header with the project name returned by /api/mode.
    // Also expose projectRoot so the event-tree component can relativize
    // absolute paths (e.g. strip project root prefix from file paths).
    if (data.mode === 'single' && data.projectName) {
      window.wipnoteProjectName = data.projectName;
      window.wipnoteProjectRoot = data.projectRoot || null;
      var pe = document.getElementById('brand-project');
      if (pe) {
        pe.textContent = '/ ' + data.projectName;
        pe.style.display = '';
      }
      document.title = data.projectName + ' — wipnote';
    }
  }).catch(function() {});
}

// loadAndRenderProjectsLanding fetches /api/projects (registry JSON
// only — no DB counts) and renders one card per project. Clicking a
// card navigates the browser to /p/<id>/ with a full page load.
function loadAndRenderProjectsLanding() {
  return fetch('/api/projects').then(function(r) {
    if (!r.ok) return [];
    return r.json();
  }).then(function(projects) {
    if (!Array.isArray(projects)) projects = [];
    window.wipnoteProjects = projects;

    // Hide the per-project side nav — the landing is a level above.
    var nav = document.querySelector('.nav');
    if (nav) nav.style.display = 'none';

    // Activate the projects landing view.
    document.querySelectorAll('.view').forEach(function(v) { v.classList.remove('active'); });
    var landing = document.getElementById('v-projects');
    if (landing) landing.classList.add('active');

    renderProjectsLanding(projects);
  });
}

// renderProjectsLanding builds one card per registered project. Cards
// are simple metadata blocks (name, path, git remote, last seen) with a
// visible "Open →" affordance. Clicking or pressing Enter navigates to
// /p/<id>/ with a full page load. No SPA state management.
function renderProjectsLanding(projects) {
  var grid = document.getElementById('project-grid');
  if (!grid) return;
  grid.innerHTML = '';
  var empty = document.getElementById('projects-empty');
  var count = document.getElementById('projects-count');
  if (count) count.textContent = String(projects.length);
  if (empty) empty.style.display = projects.length === 0 ? 'block' : 'none';

  projects.forEach(function(p) {
    var card = document.createElement('div');
    card.className = 'project-card';
    card.setAttribute('role', 'button');
    card.setAttribute('tabindex', '0');
    var navigate = function() { window.location.href = '/p/' + p.id + '/'; };
    card.addEventListener('click', navigate);
    card.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); navigate(); }
    });

    var header = document.createElement('div');
    header.className = 'project-card-header';
    var name = document.createElement('div');
    name.className = 'project-card-name';
    name.textContent = p.name || '(unnamed)';
    var dir = document.createElement('div');
    dir.className = 'project-card-dir';
    dir.textContent = p.dir || '';
    dir.title = p.dir || '';
    header.appendChild(name);
    header.appendChild(dir);

    var meta = document.createElement('div');
    meta.className = 'project-card-meta';
    var last = document.createElement('span');
    last.textContent = p.lastSeen ? relTime(p.lastSeen) : 'never seen';
    meta.appendChild(last);
    if (p.gitRemoteURL) {
      var remote = document.createElement('span');
      remote.textContent = p.gitRemoteURL;
      remote.title = p.gitRemoteURL;
      meta.appendChild(remote);
    }

    var open = document.createElement('div');
    open.className = 'project-card-open';
    open.textContent = 'Open \u2192';

    card.appendChild(header);
    card.appendChild(meta);
    card.appendChild(open);
    grid.appendChild(card);
  });
}

// Startup: detect mode, then either render the landing (at root) or run
// the single-project startup (under /p/<id>/).
detectMode().then(function() {
  // Probe the terminal feature gate once on every page — hide the button if
  // the backend routes are not registered (WIPNOTE_TERMINAL not set to "1").
  // Runs before the doorway early return so the landing page also hides its
  // button, since the root server never registers /api/terminal/* either.
  probeTerminalFeature();
  if (isDoorwayLanding()) {
    // In single-project mode the server serves all /api/* routes from the
    // root URL. The event-tree's connectedCallback ran before detectMode()
    // resolved and saw an unknown mode, so it skipped load(). Now that we
    // know the mode is 'single', explicitly trigger the component's
    // startLoad() so the activity feed populates at the root URL too.
    if (window.wipnoteMode === 'single') {
      var treeEl = document.querySelector('hg-event-tree');
      if (treeEl && typeof treeEl.startLoad === 'function') {
        treeEl.startLoad();
      }
      // Continue below — single-project mode at root still needs stats, etc.
    } else {
      // Global doorway: hide the stats bar (no aggregate data in the doorway).
      var sb = document.getElementById('stats-bar');
      if (sb) sb.style.display = 'none';
      return;
    }
  }
  // Inside a project via /p/<id>/ — inject a back link at the top of
  // the nav so the user can return to the projects doorway. Skip in
  // single-project mode at root: there is no multi-project doorway to
  // return to, so the "All Projects" link would be a dead end.
  if (window.location.pathname.indexOf('/p/') === 0) {
    var nav = document.querySelector('.nav');
    if (nav && !document.getElementById('doorway-back')) {
      var back = document.createElement('a');
      back.id = 'doorway-back';
      back.href = '/';
      back.className = 'nav-btn';
      back.innerHTML = '<span style="font-size:13px;margin-right:4px;">&larr;</span> All Projects';
      back.style.cssText = 'margin-bottom:12px;border-bottom:1px solid var(--border);padding-bottom:12px;display:flex;align-items:center;text-decoration:none;color:var(--text-dim);font-size:.82rem;';
      nav.insertBefore(back, nav.firstChild);
    }
  }
  Promise.all([fetchStats(), fetchEvents()]);
});
setInterval(function() {
  // Refresh stats in /p/<id>/ mode or when at root in single-project mode;
  // skip for the global doorway (multi-project landing, no per-project stats).
  if (!isDoorwayLanding() || window.wipnoteMode === 'single') fetchStats();
}, 30000);

// Auto-refresh the sessions list while it is the active view so an
// in-progress session's message count and LIVE badge stay current
// without manual reloads (bug-af5d048b). Cadence is 15s — half the
// stats interval, fast enough to feel live but well above the
// autoIngest sweep window so we don't waste cycles between sweeps.
// Gated on currentView so we only fetch when the user is looking,
// avoiding background work for the activity/work/graph/plans views.
var SESSIONS_REFRESH_MS = 15000;
setInterval(function() {
  if (currentView === 'sessions' && (!isDoorwayLanding() || window.wipnoteMode === 'single')) {
    fetchSessions();
  }
}, SESSIONS_REFRESH_MS);
setInterval(function() {
  if (currentView === 'resume' && (!isDoorwayLanding() || window.wipnoteMode === 'single')) {
    fetchResumableSessions();
  }
}, SESSIONS_REFRESH_MS);

/* ── Plan detail panel ────────────────────────────────────── */

// navigateToWorkDetail switches the dashboard to the Work view and opens
// the given work item's detail panel. Called from cross-view badge
// clicks (transcript stats, session list) so the behaviour matches
// clicking a work-item node in the graph view. Without this helper,
// callers would have to duplicate the nav-btn / .view class toggling
// and still end up with the detail panel rendered inside a hidden tab
// (roborev finding on job 886).
function navigateToWorkDetail(id) {
  if (!id) return;
  currentView = 'work';
  document.querySelectorAll('.nav-btn').forEach(function(b) {
    b.classList.toggle('active', b.dataset.view === 'work');
  });
  document.querySelectorAll('.view').forEach(function(v) {
    v.classList.toggle('active', v.id === 'v-work');
  });
  if (typeof openWorkDetail === 'function') {
    openWorkDetail(id);
  }
}

// navigateToPlan switches to the plans view and opens the given plan.
// Called from cross-view badge clicks (e.g. session list, transcript view).
function navigateToPlan(planId, title) {
  // Resolve title from already-loaded plans list when not provided.
  if (!title && plans.length > 0) {
    var found = plans.find(function(p) { return p.id === planId; });
    title = found ? found.title : planId;
  }

  // Switch nav to plans view (the click handler calls fetchPlans if needed).
  var planBtn = document.querySelector('.nav-btn[data-view="plans"]');
  if (planBtn && currentView !== 'plans') planBtn.click();

  // Open the plan detail immediately — openPlanDetail fetches its own content.
  openPlanDetail(planId, title || planId);
}

function closePlanDetail() {
  var detail = document.getElementById('plan-detail');
  var listView = document.getElementById('plans-list-view');
  var viewTitle = document.querySelector('#v-plans .view-title');
  detail.classList.remove('active');
  listView.style.display = '';
  if (viewTitle) viewTitle.style.display = '';
  // Clear plan subnav
  var subnav = document.getElementById('plan-subnav');
  if (subnav) { subnav.classList.remove('active'); subnav.innerHTML = ''; }
  // Reset dashboard sidebar state
  dashSidebarTeardown();
}

function openPlanDetail(planId, title) {
  var detail = document.getElementById('plan-detail');
  var listView = document.getElementById('plans-list-view');
  var body = document.getElementById('plan-detail-body');
  var titleEl = document.getElementById('plan-detail-title');
  var viewTitle = document.querySelector('#v-plans .view-title');

  listView.style.display = 'none';
  if (viewTitle) viewTitle.style.display = 'none';
  detail.classList.add('active');
  titleEl.textContent = title || planId;
  body.innerHTML = '<div class="empty">Loading...</div>';
  // Set current plan ID for sidebar
  detail.dataset.currentPlanId = planId;

  fetch(buildProjectUrl('plans/' + planId + '/render'))
    .then(function(r) {
      if (!r.ok) throw new Error('Not found');
      return r.text();
    })
    .then(function(html) {
      body.innerHTML = html;
      // Scripts via innerHTML don't execute. Load external scripts first
      // (D3, dagre-d3, hljs), then run inline scripts after they're ready.
      var scripts = Array.from(body.querySelectorAll('script'));
      var externals = scripts.filter(function(s) { return !!s.src; });
      var inlines = scripts.filter(function(s) { return !s.src && s.textContent.trim(); });

      // Remove all old script tags
      scripts.forEach(function(s) { s.remove(); });

      // Build plan subnav from section cards in the loaded content
      buildPlanSubnav(body);

      // Wire the dashboard-level sidebar for this plan
      dashSidebarSetup(planId, body);

      // Load external scripts sequentially, then run inlines
      function loadNext(i) {
        if (i >= externals.length) {
          inlines.forEach(function(oldScript) {
            var s = document.createElement('script');
            s.textContent = oldScript.textContent;
            body.appendChild(s);
          });
          // After inline scripts (renderMd, data-markdown) have run,
          // classify (N)-prefixed paragraphs for CSS hanging indent.
          body.querySelectorAll('.slice-field-value p').forEach(function(p) {
            if (/^\(\d+\)/.test(p.textContent)) p.classList.add('numbered-step');
          });
          // Re-sync the review rail now that inline scripts may have set approval state
          dashSidebarSyncRail(body);
          return;
        }
        var s = document.createElement('script');
        s.src = externals[i].src;
        s.onload = function() { loadNext(i + 1); };
        s.onerror = function() { loadNext(i + 1); };
        body.appendChild(s);
      }
      loadNext(0);
    })
    .catch(function() {
      body.innerHTML = '<div class="empty">Could not load plan: ' + planId + '</div>';
    });
}

function buildPlanSubnav(container) {
  var subnav = document.getElementById('plan-subnav');
  if (!subnav) return;
  subnav.innerHTML = '';

  var sections = [];
  var graph = container.querySelector('.dep-graph');
  if (graph) sections.push({ id: graph.id || 'dep-graph', label: 'Graph', slices: null });

  container.querySelectorAll('.section-card[id]').forEach(function(el) {
    var summary = el.querySelector('summary span:first-child');
    var label = summary ? summary.textContent.trim() : el.id;
    // Collect per-slice triage data when we hit the Slices section.
    var sliceData = null;
    if (el.id === 'slices') {
      sliceData = [];
      el.querySelectorAll('.slice-card[id]').forEach(function(card) {
        var num = card.dataset.slice || card.id.replace('slice-', '');
        var nameEl = card.querySelector('.slice-name');
        var title = nameEl ? nameEl.textContent.trim() : ('#' + num);
        var issueBadge = card.querySelector('.badge-issues');
        var questionBadge = card.querySelector('.badge-questions');
        var issues = issueBadge ? (parseInt(issueBadge.textContent, 10) || 0) : 0;
        var questions = questionBadge ? (parseInt(questionBadge.textContent, 10) || 0) : 0;
        sliceData.push({ num: num, id: card.id, title: title, issues: issues, questions: questions });
      });
    }
    sections.push({ id: el.id, label: label, slices: sliceData });
  });

  var progress = container.querySelector('.progress-zone');
  if (progress) sections.push({ id: progress.id || 'feedback-summary', label: 'Progress', slices: null });

  sections.forEach(function(sec) {
    var a = document.createElement('a');
    a.href = '#';
    a.textContent = sec.label;
    a.addEventListener('click', function(e) {
      e.preventDefault();
      var target = container.querySelector('#' + sec.id);
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      subnav.querySelectorAll('a').forEach(function(l) { l.classList.remove('active'); });
      a.classList.add('active');
    });
    subnav.appendChild(a);

    // Under the Slices section, append one nav item per slice with triage badges.
    if (sec.slices && sec.slices.length > 0) {
      var ul = document.createElement('ul');
      ul.className = 'plan-slice-nav';
      sec.slices.forEach(function(slice) {
        var li = document.createElement('li');
        var sa = document.createElement('a');
        sa.href = '#';
        sa.className = 'plan-slice-nav-item';
        // Slice label: "#N Title"
        var labelSpan = document.createElement('span');
        labelSpan.className = 'plan-slice-nav-label';
        var shortTitle = slice.title.length > 22 ? slice.title.substring(0, 22) + '…' : slice.title;
        labelSpan.textContent = '#' + slice.num + ' ' + shortTitle;
        sa.appendChild(labelSpan);
        // Issue badge (amber ⚠)
        if (slice.issues > 0) {
          var ib = document.createElement('span');
          ib.className = 'plan-slice-nav-badge plan-slice-nav-badge--issue';
          ib.title = slice.issues + ' issue(s)';
          ib.textContent = '⚠' + slice.issues;
          sa.appendChild(ib);
        }
        // Question badge (blue ?)
        if (slice.questions > 0) {
          var qb = document.createElement('span');
          qb.className = 'plan-slice-nav-badge plan-slice-nav-badge--question';
          qb.title = slice.questions + ' open question(s)';
          qb.textContent = '?' + slice.questions;
          sa.appendChild(qb);
        }
        sa.addEventListener('click', function(e) {
          e.preventDefault();
          var card = container.querySelector('#' + slice.id);
          if (card) {
            // Expand the slice card if collapsed.
            if (!card.open) card.open = true;
            card.scrollIntoView({ behavior: 'smooth', block: 'start' });
          }
          subnav.querySelectorAll('a').forEach(function(l) { l.classList.remove('active'); });
          sa.classList.add('active');
        });
        li.appendChild(sa);
        ul.appendChild(li);
      });
      subnav.appendChild(ul);
    }
  });

  // Chat link — focuses the dashboard-level chat panel
  var chatLink = document.createElement('a');
  chatLink.href = '#';
  chatLink.textContent = 'Chat';
  chatLink.style.color = 'var(--accent)';
  chatLink.addEventListener('click', function(e) {
    e.preventDefault();
    var input = document.getElementById('dash-chat-input');
    if (input) input.focus();
  });
  subnav.appendChild(chatLink);

  subnav.classList.add('active');
}

/* ── Graph View ────────────────────────────────────────────── */

var graphSimulation = null;
// Observer that watches the root <html> element for data-theme
// attribute changes so we can repaint existing graph nodes and the
// legend without tearing down the D3 simulation. Disconnected on
// every renderGraph call and re-created against the new selection.
var graphThemeObserver = null;

// GRAPH_LAYOUT centralizes every tunable constant in the graph view so
// values can be adjusted in one place instead of scattered across
// renderGraph. FNV_* drives the per-node deterministic position seeding;
// TYPE_BAND_Y is the vertical fraction of the viewport each node type
// anchors to (tracks near the top, sessions near the bottom); SIM_*
// governs the force-layout cooldown.
var GRAPH_LAYOUT = {
  // FNV-1a hash constants for deterministic (x, y) seeding.
  FNV_OFFSET_BASIS: 2166136261,
  FNV_PRIME: 16777619,
  // Hash bit ranges used to spread nodes within a band.
  HASH_X_MODULUS: 1000,          // low bits drive x position
  HASH_Y_MODULUS: 200,           // next 8 bits drive y jitter
  HASH_Y_SHIFT: 10,              // bit shift before the y modulus
  BAND_Y_JITTER_FRACTION: 0.15,  // fraction of viewport height a node can drift within its band
  // Vertical anchors per node type, expressed as a fraction of viewport
  // height. Chosen to match the eventual force-layout clusters so the
  // relaxation pass has almost no work to do.
  TYPE_BAND_Y: {
    track:   0.20,
    plan:    0.30,
    feature: 0.50,
    bug:     0.55,
    spike:   0.60,
    session: 0.80,
    file:    0.90
  },
  // Force-simulation cooldown. Lower starting alpha + faster decay
  // because nodes are pre-seeded near their final positions.
  SIM_INITIAL_ALPHA: 0.3,
  SIM_ALPHA_DECAY:   0.05,
  // Inter-node repulsion. More negative = more spread. Tuned by eye
  // on a 500+ node graph; -220 gives the dense center room to breathe
  // without blowing the whole graph outside the viewport.
  CHARGE_STRENGTH: -320,
  // Per-type fill, keyed to design-system tokens so the graph inherits
  // the active theme automatically. Resolved live via
  // getComputedStyle(document.documentElement) at every getGraphPalette()
  // call — CSS `var(...)` cannot be assigned directly to a d3 `fill`
  // attribute, and the computed value flips when the user toggles theme.
  // Track is the brand accent; feature/plan are the grayscale tier
  // (plan is differentiated with a dashed stroke in the node render);
  // bug/spike/session reuse semantic status/priority tokens.
  TYPE_TOKEN: {
    track:   '--accent',           // green
    plan:    '--graph-plan',       // teal — was grey, collided with feature
    feature: '--graph-feature',    // near-white — was grey, ambiguous
    bug:     '--status-blocked',   // red
    spike:   '--priority-high',    // amber
    session: '--status-ip',        // blue
    file:    '--graph-file',       // gold — was grey, ambiguous
    agent:   '--graph-agent'       // purple (filter UI only; no graph nodes)
  },
  // Fill opacity for non-session nodes. Sessions stay at their
  // existing 0.6 — they're secondary. 0.88 takes a little more
  // edge off the primary nodes without making them look washed out.
  NODE_FILL_OPACITY: 0.88
};

// getGraphPalette resolves GRAPH_LAYOUT.TYPE_TOKEN into a flat map of
// concrete color strings using the live computed values of the root
// element. Called on every render and on every data-theme mutation so
// the result always reflects the active theme.
function getGraphPalette() {
  var cs = getComputedStyle(document.documentElement);
  var out = {};
  var tokens = GRAPH_LAYOUT.TYPE_TOKEN;
  for (var key in tokens) {
    if (Object.prototype.hasOwnProperty.call(tokens, key)) {
      out[key] = cs.getPropertyValue(tokens[key]).trim() || '#888';
    }
  }
  return out;
}

// colorToRGB parses any CSS color string getComputedStyle might return
// (rgb, rgba, hex) into a numeric [r,g,b] triple. Named colors and hsl
// are not expected on our tokens but fall through to a neutral gray so
// YIQ still produces a reasonable pick.
function colorToRGB(c) {
  if (!c) return [128, 128, 128];
  if (c[0] === '#') {
    if (c.length === 4) c = '#' + c[1]+c[1] + c[2]+c[2] + c[3]+c[3];
    return [parseInt(c.slice(1,3), 16), parseInt(c.slice(3,5), 16), parseInt(c.slice(5,7), 16)];
  }
  var m = c.match(/\d+(\.\d+)?/g);
  if (!m || m.length < 3) return [128, 128, 128];
  return [parseFloat(m[0]) | 0, parseFloat(m[1]) | 0, parseFloat(m[2]) | 0];
}

// pickLabelColor picks a near-black or near-white ink for a given node
// fill using YIQ luminance, so labels stay legible on every palette
// entry regardless of active theme. The paint-order stroke layered on
// top of the label adds a second line of defense for edge-case fills.
function pickLabelColor(fill) {
  var rgb = colorToRGB(fill);
  var yiq = (rgb[0] * 299 + rgb[1] * 587 + rgb[2] * 114) / 1000;
  return yiq >= 140 ? '#0a0a0a' : '#f0f0f0';
}

// paintGraphLegend applies the current-theme colors to every legend
// entry carrying a data-graph-type attribute. Called on initial render
// and again whenever the theme toggles.
function paintGraphLegend() {
  var palette = getGraphPalette();
  var spans = document.querySelectorAll('[data-graph-type]');
  for (var i = 0; i < spans.length; i++) {
    var t = spans[i].getAttribute('data-graph-type');
    if (palette[t]) spans[i].style.color = palette[t];
  }
}

// Active type filter state — array means "show only these types",
// null means "show all". Default opens to the work-item skeleton only
// (track / plan / feature / bug / spike). Provenance layers (session,
// file) are one toolbar click away but start hidden so the graph
// doesn't open as a hairball. Research basis: Cambridge Intelligence
// "don't visualize everything in your underlying knowledge graph" —
// derive a workflow-focused subset.
var GRAPH_DEFAULT_TYPES = ['track', 'plan', 'feature', 'bug', 'spike'];
var graphActiveTypes = GRAPH_DEFAULT_TYPES.slice();

// Selected agent for "Filter by agent" dropdown. When set, the graph
// contracts to only the sessions/features/files the named agent
// interacted with (via agent_lineage_trace).
var graphActiveAgent = '';

// Race-proofing: track the current fetch so stale responses from a rapid
// sequence of toggles don't overwrite newer graph state. Each call bumps the
// token and aborts the previous request; the .then() checks the token before
// rendering.
var graphFetchToken = 0;
var graphFetchController = null;

function fetchGraph(types) {
  // If caller didn't pass an explicit filter, fall back to the module
  // state (graphActiveTypes). That way the initial page load and the
  // post-Reset fetch both pick up the current filter.
  if (types === undefined) types = graphActiveTypes;
  var url = buildProjectUrl('graph');
  if (types && types.length > 0) url += (url.indexOf('?') >= 0 ? '&' : '?') + 'types=' + types.join(',');
  if (graphActiveAgent) url += (url.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(graphActiveAgent);

  // Cancel any in-flight request before starting a new one.
  if (graphFetchController) {
    try { graphFetchController.abort(); } catch (e) {}
  }
  graphFetchController = typeof AbortController === 'function' ? new AbortController() : null;
  var myToken = ++graphFetchToken;
  var signal = graphFetchController ? graphFetchController.signal : undefined;

  fetch(url, { signal: signal })
    .then(function(r) { return r.json(); })
    .then(function(data) {
      // Drop stale responses — only the latest token is allowed to render.
      if (myToken !== graphFetchToken) return;
      document.getElementById('graph-count').textContent = data.nodes ? data.nodes.length : 0;
      var empty = document.getElementById('graph-empty');
      if (!data.nodes || data.nodes.length === 0) {
        empty.style.display = '';
        return;
      }
      empty.style.display = 'none';
      renderGraph(data);
    })
    .catch(function(err) {
      if (err && err.name === 'AbortError') return;
      if (myToken !== graphFetchToken) return;
      document.getElementById('graph-empty').style.display = '';
    });
}

function renderGraph(data) {
  var container = document.getElementById('graph-container');
  // Remove any previous SVG but keep the legend and empty overlay.
  var oldSvg = container.querySelector('svg');
  if (oldSvg) oldSvg.remove();
  var oldTip = container.querySelector('.graph-tooltip');
  if (oldTip) oldTip.remove();

  // Build or update the filter toolbar.
  var oldToolbar = container.querySelector('.graph-filter-toolbar');
  if (oldToolbar) oldToolbar.remove();
  // Agent and commit intentionally absent: agents are surfaced via the
  // "Filter by agent" dropdown (they're the actor, not a node), and
  // commits are sub-attributes of the session/feature that produced
  // them (visible in the provenance panel, not as standalone nodes).
  var allTypes = ['track', 'plan', 'feature', 'bug', 'spike', 'session', 'file'];
  var typeCounts = {};
  (data.nodes || []).forEach(function(n) { typeCounts[n.type] = (typeCounts[n.type] || 0) + 1; });
  var toolbar = document.createElement('div');
  toolbar.className = 'graph-filter-toolbar';
  allTypes.forEach(function(type) {
    if (!typeCounts[type] && !(graphActiveTypes && graphActiveTypes.indexOf(type) < 0)) return;
    var btn = document.createElement('button');
    var active = !graphActiveTypes || graphActiveTypes.indexOf(type) >= 0;
    btn.className = 'graph-filter-btn' + (active ? ' active' : '');
    btn.dataset.type = type;
    var label = type.charAt(0).toUpperCase() + type.slice(1);
    var count = typeCounts[type] || 0;
    var capText = data.caps && data.caps[type] && data.caps[type].total > data.caps[type].shown
      ? ' of ' + data.caps[type].total : '';
    // Use the type's SVG icon instead of a colored dot so the filter
     // doubles as a legend. Icon inherits currentColor via CSS so
     // active/inactive button states tint it automatically.
    btn.innerHTML = '<svg class="filter-icon" width="12" height="12" aria-hidden="true">' +
      '<use href="#icon-' + type + '"/></svg> ' + label +
      ' <span style="opacity:0.6">' + count + capText + '</span>';
    btn.onclick = function() {
      if (!graphActiveTypes) {
        // First click: drop the clicked type, keep all others.
        graphActiveTypes = allTypes.filter(function(t) { return t !== type; });
      } else {
        var idx = graphActiveTypes.indexOf(type);
        if (idx >= 0) {
          // Refuse to deselect the last remaining active type — a request
          // with an empty list would silently reload the full graph and
          // desync the toolbar from the canvas. The user can use Reset to
          // return to the default view.
          if (graphActiveTypes.length <= 1) return;
          graphActiveTypes.splice(idx, 1);
        } else {
          graphActiveTypes.push(type);
        }
        if (graphActiveTypes.length === allTypes.length) graphActiveTypes = null;
      }
      fetchGraph(graphActiveTypes);
    };
    toolbar.appendChild(btn);
  });
  // "Filter by agent" dropdown. Populated lazily from /api/graph/agents.
  // Changing it refetches the graph restricted to work that agent
  // touched (via agent_lineage_trace). The dropdown lives in the
  // toolbar (not the left nav) so the scope is always obvious.
  var agentSelect = document.createElement('select');
  agentSelect.className = 'graph-filter-btn';
  agentSelect.style.padding = '4px 6px';
  var blankOpt = document.createElement('option');
  blankOpt.value = '';
  blankOpt.textContent = 'All agents';
  agentSelect.appendChild(blankOpt);
  agentSelect.onchange = function() {
    graphActiveAgent = agentSelect.value;
    fetchGraph();
  };
  toolbar.appendChild(agentSelect);
  // Fetch the agent list once per render. Preserves current selection
  // so the user's filter doesn't reset on every re-render.
  fetch(buildProjectUrl('graph/agents'))
    .then(function(r) { return r.json(); })
    .then(function(list) {
      (list || []).forEach(function(name) {
        var opt = document.createElement('option');
        opt.value = name;
        opt.textContent = name;
        if (name === graphActiveAgent) opt.selected = true;
        agentSelect.appendChild(opt);
      });
    })
    .catch(function() { /* ignore — dropdown just stays empty */ });

  var resetBtn = document.createElement('button');
  resetBtn.className = 'graph-filter-btn';
  resetBtn.textContent = 'Reset';
  // Reset returns to the workflow default, not "show everything".
  // A user who actually wants everything can hit every toolbar button.
  resetBtn.onclick = function() {
    graphActiveTypes = GRAPH_DEFAULT_TYPES.slice();
    graphActiveAgent = '';
    fetchGraph();
  };
  toolbar.appendChild(resetBtn);
  container.insertBefore(toolbar, container.firstChild);

  if (graphSimulation) {
    graphSimulation.stop();
    graphSimulation = null;
  }
  if (graphThemeObserver) {
    graphThemeObserver.disconnect();
    graphThemeObserver = null;
  }

  var width = container.clientWidth || 800;
  var height = container.clientHeight || 600;

  var svg = d3.select('#graph-container').append('svg')
    .attr('width', width)
    .attr('height', height)
    .style('display', 'block');

  // Zoom / pan layer.
  var g = svg.append('g');
  svg.call(d3.zoom()
    .scaleExtent([0.1, 4])
    .on('zoom', function(e) { g.attr('transform', e.transform); })
  );

  // Colour by node type — theme-aware via getGraphPalette(). The
  // variable is reassigned inside the MutationObserver below so a
  // theme toggle repaints existing nodes without rebuilding the
  // simulation.
  var typeColor = getGraphPalette();
  paintGraphLegend();

  // Node size combines edges (structural weight) and activity (usage weight).
  // Log scale spreads small nodes more and compresses large ones so hubs
  // don't all blob together at max size.
  function nodeRadius(d) {
    var edges = d.edges || 0;
    var activity = d.activity || 0;
    // Weighted combination — edges matter more than raw activity.
    var weight = edges * 2 + Math.sqrt(activity);
    // Log scale with minimum floor and max cap.
    var r = 4 + Math.log(1 + weight) * 4;
    return Math.max(4, Math.min(28, r));
  }

  // visualRadius is the ACTUAL rendered radius of the circle, after any
  // type-specific scaling. This is the value to use for hit testing,
  // text wrapping, and label fit decisions so a single source of truth
  // governs "how big is this node on screen." Previously the code used
  // nodeRadius() directly for labels while the circle renderer scaled
  // session nodes to 60% — the labels thought they had more room than
  // the circle actually provided and spilled onto the background.
  function visualRadius(d) {
    if (d.type === 'session') return Math.max(3, nodeRadius(d) * 0.6);
    if (d.type === 'commit' || d.type === 'file') return Math.max(3, nodeRadius(d) * 0.5);
    if (d.type === 'agent') return Math.max(4, nodeRadius(d) * 0.8);
    return nodeRadius(d);
  }

  // Make a shallow copy so D3 can mutate positions without polluting our cache.
  var nodes = data.nodes.map(function(n) { return Object.assign({}, n); });
  var edges = data.edges.map(function(e) { return Object.assign({}, e); });

  // Seed deterministic starting positions so nodes appear in roughly stable
  // locations instead of random chaos. Without this, D3 assigns every node a
  // random (x, y) and the simulation bounces everything into place — visible
  // as a jarring "explosion and settle" on every load. With seeded positions,
  // the simulation relaxes from a near-final layout, producing a small shiver.
  // All tunables live in GRAPH_LAYOUT at the top of the Graph View section.
  function hashNodeId(s) {
    var h = GRAPH_LAYOUT.FNV_OFFSET_BASIS;
    for (var i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      // Math.imul performs true 32-bit integer multiplication; plain
      // `h * FNV_PRIME` would overflow a 64-bit float past 2^53 and
      // silently corrupt the hash distribution, causing avoidable
      // clustering in the seeded layout (roborev finding on f0b9d8aa).
      h = Math.imul(h, GRAPH_LAYOUT.FNV_PRIME) >>> 0;
    }
    return h;
  }
  var DEFAULT_BAND_Y = 0.5;
  nodes.forEach(function(n) {
    var h = hashNodeId(n.id);
    var bandFraction = GRAPH_LAYOUT.TYPE_BAND_Y[n.type];
    if (bandFraction === undefined) bandFraction = DEFAULT_BAND_Y;
    var bandY = bandFraction * height;
    var jitterRange = height * GRAPH_LAYOUT.BAND_Y_JITTER_FRACTION;
    n.x = ((h % GRAPH_LAYOUT.HASH_X_MODULUS) / GRAPH_LAYOUT.HASH_X_MODULUS) * width;
    n.y = bandY + ((((h >>> GRAPH_LAYOUT.HASH_Y_SHIFT) % GRAPH_LAYOUT.HASH_Y_MODULUS) / GRAPH_LAYOUT.HASH_Y_MODULUS) - 0.5) * jitterRange;
  });

  // Balanced forces: clusters visible but not overlapping.
  // Link strength varies by type: structural edges pull tighter than activity.
  // SIM_INITIAL_ALPHA and SIM_ALPHA_DECAY are lowered from the D3 defaults
  // (1.0 and 0.0228 respectively) because nodes are pre-seeded near their
  // final positions — the simulation only needs a short relaxation pass.
  graphSimulation = d3.forceSimulation(nodes)
    .alpha(GRAPH_LAYOUT.SIM_INITIAL_ALPHA)
    .alphaDecay(GRAPH_LAYOUT.SIM_ALPHA_DECAY)
    .force('link', d3.forceLink(edges).id(function(d) { return d.id; })
      .distance(function(d) {
        if (d.type === 'worked_on') return 90;
        if (d.type === 'part_of') return 70;     // feature -> track spacing
        return 55;
      })
      .strength(function(d) {
        // Structural edges dominate the layout; activity edges are loose.
        if (d.type === 'worked_on') return 0.2;
        if (d.type === 'part_of') return 0.9;
        return 0.6;
      }))
    .force('charge', d3.forceManyBody().strength(GRAPH_LAYOUT.CHARGE_STRENGTH).distanceMax(400))
    .force('center', d3.forceCenter(width / 2, height / 2))
    .force('x', d3.forceX(width / 2).strength(0.015))
    .force('y', d3.forceY(height / 2).strength(0.015))
    // Collision padding reserves space around each node so glyphs and
    // labels don't overlap neighbors. Track nodes use their measured
    // label half-width (set after labels render) so the effective
    // collision disc covers both the circle AND the label strip. If
    // the measurement hasn't happened yet (first tick), we fall back
    // to a generous default so the layout doesn't settle too tightly
    // before labels arrive.
    .force('collision', d3.forceCollide()
      .radius(function(d) {
        if (d.type === 'track') {
          var halfW = d._labelHalfWidth || 80;
          return Math.max(visualRadius(d) + 26, halfW + 8);
        }
        return visualRadius(d) + 5;
      })
      .strength(0.9));

  // Edges share one uniform color. Differentiation by relationship
  // type used to be encoded in the stroke; with many types and many
  // overlapping lines, the rainbow effect added noise rather than
  // signal. Structural vs provenance distinction now lives in stroke
  // opacity + width, not hue. 'spawned' edges still dashed.
  var STRUCTURAL_EDGE_TYPES = {
    part_of: 1, blocked_by: 1, caused_by: 1, implements: 1,
    contains: 1, co_session: 1
  };
  function isStructuralEdge(d) { return !!STRUCTURAL_EDGE_TYPES[d.type]; }
  var link = g.append('g').selectAll('line')
    .data(edges).enter().append('line')
    .attr('stroke', 'var(--border-strong)')
    .attr('stroke-opacity', function(d) { return isStructuralEdge(d) ? 0.45 : 0.08; })
    .attr('stroke-width', function(d) { return isStructuralEdge(d) ? 1.4 : 0.5; })
    .attr('stroke-dasharray', function(d) {
      return d.type === 'spawned' ? '6,3' : null;
    });

  // All nodes share ONE fill color (the feature slate). Type identity
  // is carried entirely by the icon glyph inside — this turns the
  // canvas into a uniform Obsidian-like field where the shape-language
  // of the icons, not hue, distinguishes types. typeColor['feature']
  // is used as the source so theme switches still work.
  var uniformFill = typeColor['feature'] || '#9ca3af';
  var node = g.append('g').selectAll('circle')
    .data(nodes).enter().append('circle')
    .attr('r', visualRadius)
    .attr('fill', uniformFill)
    .attr('fill-opacity', GRAPH_LAYOUT.NODE_FILL_OPACITY)
    .attr('stroke', 'var(--bg-primary)')
    .attr('stroke-width', 1.5)
    .attr('stroke-dasharray', function(d) { return d.type === 'plan' ? '4,2' : null; })
    .style('cursor', 'pointer')
    .call(d3.drag()
      .on('start', function(e, d) {
        if (!e.active) graphSimulation.alphaTarget(0.3).restart();
        d.fx = d.x; d.fy = d.y;
      })
      .on('drag', function(e, d) { d.fx = e.x; d.fy = e.y; })
      .on('end', function(e, d) {
        if (!e.active) graphSimulation.alphaTarget(0);
        d.fx = null; d.fy = null;
      })
    );

  // Icons sit inside the node circle, providing shape-based type
  // differentiation on top of the color. Min 10px so small nodes keep
  // some glyph readability; size scales with the node radius. Color
  // inherits from --bg-primary so they read as "reversed out" text on
  // the circle fill, adapting to dark/light theme automatically.
  var ICON_MIN_SIZE = 10;
  // All node types get an icon now — with uniform circle color (see
  // below), the icon is the sole type differentiator. Glyphs are
  // aligned with the terminal statusline metaphors: track=route,
  // feature=lightbulb, bug=bug, spike=zap — so a user moving
  // between the CLI prompt and the dashboard sees the same symbols.
  var iconTypes = { track:1, plan:1, feature:1, bug:1, spike:1, session:1, file:1 };
  function iconSize(d) {
    return Math.max(ICON_MIN_SIZE, visualRadius(d) * 1.2);
  }
  var icons = g.append('g')
    .attr('pointer-events', 'none')
    .selectAll('use')
    .data(nodes.filter(function(d) { return iconTypes[d.type] && visualRadius(d) >= 8; }))
    .enter().append('use')
    .attr('href', function(d) { return '#icon-' + d.type; })
    .attr('color', 'var(--bg-primary)')
    .attr('opacity', 0.95);

  // Repaint nodes, labels, and legend on theme toggle without tearing
  // down the simulation. The closure captures `node` / the label
  // selections and reassigns `typeColor` so subsequent fill reads stay
  // in sync (used by drag/hover handlers that reuse typeColor).
  graphThemeObserver = new MutationObserver(function() {
    typeColor = getGraphPalette();
    node.attr('fill', typeColor['feature'] || '#9ca3af');
    paintGraphLegend();
  });
  graphThemeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme']
  });

  // Tooltip.
  var tooltip = d3.select('#graph-container').append('div')
    .attr('class', 'graph-tooltip')
    .style('position', 'absolute')
    .style('background', 'rgba(15, 23, 42, 0.9)')
    .style('backdrop-filter', 'blur(4px)')
    .style('-webkit-backdrop-filter', 'blur(4px)')
    .style('border', '1px solid var(--border)')
    .style('padding', '8px 12px')
    .style('border-radius', '6px')
    .style('font-size', '12px')
    .style('pointer-events', 'none')
    .style('opacity', 0)
    .style('color', 'var(--text-primary)')
    .style('max-width', '240px')
    .style('z-index', 20)
    .style('box-shadow', '0 4px 12px rgba(0,0,0,0.4)');

  node.on('mouseover', function(e, d) {
    var rect = container.getBoundingClientRect();
    // Tooltip content is built with DOM text nodes instead of .html(...)
    // because d.title can originate from a user prompt (session nodes
    // now use sessions.title or the first user message as their label)
    // and passing that through innerHTML would let a crafted prompt
    // inject <script> into the dashboard (roborev finding on job 886).
    var tipEl = tooltip.node();
    tipEl.textContent = '';
    var titleEl = document.createElement('strong');
    titleEl.textContent = d.title || '';
    tipEl.appendChild(titleEl);
    tipEl.appendChild(document.createElement('br'));
    var meta = (d.type || '') + ' · ' + (d.status || '') +
               ' · ' + (d.edges || 0) + ' edge' + (d.edges !== 1 ? 's' : '');
    tipEl.appendChild(document.createTextNode(meta));
    tooltip.style('opacity', 1)
      .style('left', (e.clientX - rect.left + 12) + 'px')
      .style('top', (e.clientY - rect.top - 10) + 'px');
    // Highlight connected nodes.
    var connected = new Set();
    edges.forEach(function(edge) {
      var src = typeof edge.source === 'object' ? edge.source.id : edge.source;
      var tgt = typeof edge.target === 'object' ? edge.target.id : edge.target;
      if (src === d.id) connected.add(tgt);
      if (tgt === d.id) connected.add(src);
    });
    node.attr('opacity', function(n) {
      return n.id === d.id || connected.has(n.id) ? 1 : 0.25;
    });
    link.attr('stroke-opacity', function(edge) {
      var src = typeof edge.source === 'object' ? edge.source.id : edge.source;
      var tgt = typeof edge.target === 'object' ? edge.target.id : edge.target;
      return (src === d.id || tgt === d.id) ? 0.9 : 0.05;
    });
  }).on('mousemove', function(e) {
    var rect = container.getBoundingClientRect();
    tooltip.style('left', (e.clientX - rect.left + 12) + 'px').style('top', (e.clientY - rect.top - 10) + 'px');
  }).on('mouseout', function() {
    tooltip.style('opacity', 0);
    // If a focus lens is active (focusedNodeId !== null), do NOT reset
    // to the generic baseline — that would clobber the focused state.
    // Re-apply the focus instead so moving the pointer off a node
    // keeps the lens intact. When nothing is focused, fall back to
    // the pre-focus defaults.
    if (focusedNodeId !== null) {
      focusOnNode(focusedNodeId);
    } else {
      node.attr('opacity', 1);
      link.attr('stroke-opacity', function(d) {
        return isStructuralEdge(d) ? 0.45 : 0.08;
      });
    }
  }).on('click', function(e, d) {
    // Click does two things:
    //   1. Open the provenance panel (causal-chain drill-down)
    //   2. Focus the graph on this node's 1-hop neighborhood — fade
    //      everything else to 0.08. This is the "lens" pattern from
    //      classic focus+context research: keep the context visible
    //      but suppress it so the subject stands out.
    e.stopPropagation();
    openProvenancePanel(d.id);
    focusOnNode(d.id);
  });

  // Background click clears the focus lens (but doesn't touch the
  // provenance panel — that has its own × button). Attached to the SVG
  // root so any click that doesn't land on a node bubbles up here.
  svg.on('click', function(e) {
    // Only fire when the click target IS the svg/g layer (bubbled),
    // not when a node handled it (we stopPropagation above).
    if (e.target === svg.node() || e.target.tagName === 'g') {
      clearFocus();
    }
  });

  // Build an adjacency map once per render. Nodes in the same 1-hop
  // neighborhood stay at full opacity under focus; all others fade.
  // Include the focused node itself so it doesn't dim itself.
  var adjacency = {};
  edges.forEach(function(edge) {
    var s = edge.source.id || edge.source;
    var t = edge.target.id || edge.target;
    (adjacency[s] = adjacency[s] || {})[t] = 1;
    (adjacency[t] = adjacency[t] || {})[s] = 1;
  });

  var focusedNodeId = null;
  function focusOnNode(id) {
    focusedNodeId = id;
    var neighbors = adjacency[id] || {};
    neighbors[id] = 1;
    // Resolve --accent at click time so theme swaps pick up the right
    // shade (dark-mode neon #CDFF00 vs light-mode olive #4a6e00).
    var accentColor = getComputedStyle(document.documentElement)
      .getPropertyValue('--accent').trim() || '#CDFF00';
    var baseColor = typeColor['feature'] || '#9ca3af';
    icons.attr('opacity', function(d) { return neighbors[d.id] ? 0.95 : 0.08; });
    // The selected node AND its 1-hop neighborhood all get the neon
    // accent fill — they're the focused subgraph. Everyone outside
    // that neighborhood stays slate but fades to 0.08 opacity. Result:
    // the focused cluster lights up as one coherent lime-colored
    // shape, the rest recedes into context.
    node.attr('fill', function(d) {
      return neighbors[d.id] ? accentColor : baseColor;
    }).attr('fill-opacity', function(d) {
      return neighbors[d.id] ? GRAPH_LAYOUT.NODE_FILL_OPACITY : 0.08;
    });
    trackLabels.attr('opacity', function(d) { return neighbors[d.id] ? 1 : 0.15; });
    hubLabels.attr('opacity', function(d) { return neighbors[d.id] ? 1 : 0.15; });
    // Hide non-incident edges completely when focused. Previous 0.05
     // opacity was additive across many stacked lines and produced a
     // visible residual hairball behind the focus subject.
    link.attr('stroke-opacity', function(d) {
      var s = d.source.id || d.source;
      var t = d.target.id || d.target;
      return (s === id || t === id) ? 0.9 : 0;
    });
  }
  function clearFocus() {
    if (focusedNodeId === null) return;
    focusedNodeId = null;
    icons.attr('opacity', 0.95);
    // Reset all nodes back to the uniform slate fill (the previously
    // focused node was neon accent; other visible nodes were already
    // slate but no harm in resetting everyone).
    var baseColor = typeColor['feature'] || '#9ca3af';
    node.attr('fill', baseColor).attr('fill-opacity', GRAPH_LAYOUT.NODE_FILL_OPACITY);
    trackLabels.attr('opacity', 1);
    hubLabels.attr('opacity', 1);
    link.attr('stroke-opacity', function(d) { return isStructuralEdge(d) ? 0.45 : 0.08; });
  }

  // Wrap text inside a circle using real SVG measurement via getComputedTextLength.
  // Uses binary iteration: tries to fit text, shrinks font if needed, hides if too small.
  function wrapTextInCircle(textEl, title, radius) {
    textEl.text(null);
    var words = title.split(/\s+/).filter(function(w) { return w.length > 0; });
    if (words.length === 0) return;

    // Start with a font size proportional to radius, then shrink if needed.
    var minFont = 6;
    var maxFont = Math.max(minFont, Math.min(12, radius * 0.32));
    var fontSize = maxFont;

    // Try shrinking font until the text fits, or give up and truncate.
    for (var attempt = 0; attempt < 4; attempt++) {
      textEl.text(null).attr('font-size', fontSize + 'px');
      var lineHeight = fontSize * 1.15;
      // Reserve inner area — circle chord at top/bottom is narrower.
      var innerRadius = radius * 0.92;
      var maxLines = Math.max(1, Math.floor((innerRadius * 2) / lineHeight));

      // Greedy word wrap, measuring actual rendered width per line.
      var lines = [];
      var i = 0;
      var fit = true;
      while (i < words.length && lines.length < maxLines) {
        // Compute the chord width at this line's y-offset.
        var lineIdx = lines.length;
        var yOffset = (lineIdx - (maxLines - 1) / 2) * lineHeight;
        var chord = 2 * Math.sqrt(Math.max(0, innerRadius * innerRadius - yOffset * yOffset));
        if (chord <= 0) break;

        // Create a temp tspan to measure word fit.
        var tspan = textEl.append('tspan').attr('x', 0).attr('dy', 0);
        var line = words[i];
        tspan.text(line);
        // If even a single word doesn't fit, we need a smaller font.
        if (tspan.node().getComputedTextLength() > chord) {
          fit = false;
          tspan.remove();
          break;
        }
        i++;
        // Add words while they fit.
        while (i < words.length) {
          tspan.text(line + ' ' + words[i]);
          if (tspan.node().getComputedTextLength() > chord) {
            tspan.text(line);
            break;
          }
          line = line + ' ' + words[i];
          i++;
        }
        lines.push(line);
      }

      if (!fit) {
        // Single word too wide for any line — shrink font and retry.
        fontSize = Math.max(minFont, fontSize - 1);
        if (fontSize === minFont) {
          // Last resort: truncate the long word with ellipsis.
          textEl.text(null);
          textEl.append('tspan').attr('x', 0).attr('dy', 0).text(words[0].substring(0, 4) + '\u2026');
          return;
        }
        continue;
      }

      // Successfully laid out. Rebuild tspans with correct dy offsets.
      textEl.text(null);
      var startY = -((lines.length - 1) * lineHeight) / 2;
      var anyTruncated = i < words.length;
      if (anyTruncated && lines.length > 0) {
        // Append ellipsis to last line if we couldn't fit all words.
        var last = lines[lines.length - 1];
        // Try to append an ellipsis that still fits.
        var testSpan = textEl.append('tspan').attr('x', 0).attr('dy', 0);
        var yOffset2 = ((lines.length - 1) - (maxLines - 1) / 2) * lineHeight;
        var chord2 = 2 * Math.sqrt(Math.max(0, innerRadius * innerRadius - yOffset2 * yOffset2));
        testSpan.text(last + '\u2026');
        if (testSpan.node().getComputedTextLength() > chord2 && last.length > 1) {
          lines[lines.length - 1] = last.substring(0, last.length - 1) + '\u2026';
        } else {
          lines[lines.length - 1] = last + '\u2026';
        }
        textEl.text(null);
      }

      for (var k = 0; k < lines.length; k++) {
        textEl.append('tspan')
          .attr('x', 0)
          .attr('dy', k === 0 ? startY : lineHeight)
          .text(lines[k]);
      }
      return;
    }
  }

  // Labels inside track nodes using SVG text + tspan (no foreignObject).
  // Fill is contrast-aware via pickLabelColor so labels stay legible
  // regardless of which palette token the node resolved to. No
  // paint-order stroke — labels wrap inside the node radius, never
  // cross onto the background, and a dark halo would visibly thicken
  // and blur the small font sizes that fit inside sub-20px nodes.
  // Obsidian-style labels below each track and feature. Only the
  // highest-cardinality work-item types get labels — bugs/spikes/
  // sessions/files stay glyph-only to keep the canvas quiet. Labels
  // sit OUTSIDE the circle (not wrapped inside) so small nodes still
  // get a short title without squeezing unreadable 6px text.
  var trackLabelNodes = nodes.filter(function(d) { return d.type === 'track'; });
  var trackLabelGroup = g.append('g').attr('pointer-events', 'none');
  // trackLabelFontSize scales with node size so a big track's label
  // reads at a distance and a small track's label doesn't hog
  // canvas real estate. Range 9–16px matches the node radius clamp
  // of [4,28] after the 0.55 multiplier — small tracks get ~9px,
  // full-cap tracks get ~15px.
  function trackLabelFontSize(d) {
    return Math.max(9, Math.min(16, Math.round(visualRadius(d) * 0.55)));
  }
  var trackLabels = trackLabelGroup.selectAll('text.track-label')
    .data(trackLabelNodes)
    .enter().append('text')
    .attr('class', 'track-label')
    .attr('text-anchor', 'middle')
    .attr('dominant-baseline', 'hanging')
    .attr('font-size', function(d) { return trackLabelFontSize(d) + 'px'; })
    .attr('font-weight', '600')
    // Paint-order halo — the stroke draws BEHIND the fill because
    // paint-order is set to 'stroke'. A halo in the canvas background
    // color creates a readable bubble around the text so the label
    // stays legible even if it brushes a neighboring node.
    .attr('paint-order', 'stroke')
    .attr('stroke', 'var(--bg-primary)')
    .attr('stroke-width', 3)
    .attr('stroke-linejoin', 'round')
    .attr('fill', 'var(--text-primary)');

  // Two-line wrap, 15 chars per line. Greedy word-boundary break:
  // if a word would push line 1 past 15 chars, flush to line 2. If
  // line 2 also overflows, truncate with an ellipsis so the label
  // stays predictable. Lines render as <tspan dy> children.
  trackLabels.each(function(d) {
    var raw = d.title || d.id || '';
    var lines = wrapAt(raw, 15, 2);
    var sel = d3.select(this);
    sel.text(null);
    lines.forEach(function(line, i) {
      sel.append('tspan')
        .attr('x', 0)
        .attr('dy', i === 0 ? 0 : '1.15em')
        .text(line);
    });
  });

  // wrapAt splits a string into at most `maxLines` lines of up to
  // `maxChars` characters each, breaking on word boundaries. If the
  // content would exceed maxLines, the last line is truncated with an
  // ellipsis so no content silently overflows.
  function wrapAt(text, maxChars, maxLines) {
    var words = text.split(/\s+/).filter(Boolean);
    var lines = [];
    var line = '';
    for (var i = 0; i < words.length; i++) {
      var w = words[i];
      var next = line ? line + ' ' + w : w;
      if (next.length <= maxChars) {
        line = next;
      } else {
        if (line) lines.push(line);
        line = w.length <= maxChars ? w : w.slice(0, maxChars - 1) + '…';
        if (lines.length >= maxLines) break;
      }
    }
    if (line && lines.length < maxLines) lines.push(line);
    // If input had more content that didn't fit, mark the last line
    // with an ellipsis.
    if (lines.length === maxLines) {
      var used = lines.join(' ').length;
      if (used < text.length - 1) {
        var last = lines[maxLines - 1];
        if (!last.endsWith('…')) {
          if (last.length + 1 > maxChars) last = last.slice(0, maxChars - 1);
          lines[maxLines - 1] = last + '…';
        }
      }
    }
    return lines.length ? lines : [''];
  }

  // Measure each track label's width so the collision force can widen
  // the track's repulsion radius to match. Stored on the datum so the
  // d3.forceCollide radius callback can read it. This is the simplest
  // "bounding box" collision: we treat the track + label as a disc
  // whose radius covers both. Not perfect (label is rectangular, not
  // circular) but cheap and effective.
  trackLabels.each(function(d) {
    // For wrapped labels with <tspan> children, getComputedTextLength
    // on the parent returns the SUM of tspan widths (total glyph run),
    // not the widest individual line. We need the widest line for
    // collision reservation, so iterate the tspans and take the max.
    // Fall back to the parent length / line count if tspan measurement
    // isn't available (test environments etc.).
    var widest = 0;
    var tspans = this.querySelectorAll('tspan');
    if (tspans.length) {
      tspans.forEach(function(t) {
        var w = t.getComputedTextLength ? t.getComputedTextLength() : 0;
        if (w > widest) widest = w;
      });
    } else {
      widest = this.getComputedTextLength ? this.getComputedTextLength() : 80;
    }
    d._labelHalfWidth = (widest || 80) / 2;
  });
  // Now that track labels know their width, re-initialize the
  // collision force so it re-reads the radius for each track with
  // the measured _labelHalfWidth, and give the simulation a gentle
  // alpha kick so nodes re-settle without re-seeding positions.
  if (graphSimulation) {
    graphSimulation.force('collision').initialize(graphSimulation.nodes());
    graphSimulation.alpha(0.3).restart();
  }

  // Feature labels are intentionally disabled. With 250+ features in a
  // typical view, labeling even 10% of them (edges >= 2) produces
  // overlapping walls of text (verified: user feedback after previous
  // pass). Feature titles stay reachable via:
  //   1. Hover tooltip (see the mouseover handler above).
  //   2. Provenance panel (click the node).
  //   3. Focus lens — clicking the node also brings its title into
  //      the panel header which stays pinned until dismissed.
  // Track labels stay on because 42 tracks is a manageable amount of
  // static text and tracks are the top-level organizing principle.
  var hubLabels = g.append('g').attr('pointer-events', 'none').selectAll('text.hub-label')
    .data([])
    .enter().append('text');

  // truncateForNodeLabel — short helper; wraps a title at word boundary
  // if it exceeds max chars. Kept local to renderGraph so the existing
  // wrapTextInCircle (still used elsewhere) isn't touched.
  function truncateForNodeLabel(s, max) {
    if (!s || s.length <= max) return s || '';
    var cut = s.lastIndexOf(' ', max);
    if (cut < max / 2) cut = max;
    return s.slice(0, cut).replace(/[ ,.;:]+$/, '') + '…';
  }

  graphSimulation.on('tick', function() {
    link
      .attr('x1', function(d) { return d.source.x; })
      .attr('y1', function(d) { return d.source.y; })
      .attr('x2', function(d) { return d.target.x; })
      .attr('y2', function(d) { return d.target.y; });
    node
      .attr('cx', function(d) { return d.x; })
      .attr('cy', function(d) { return d.y; });
    // Icons are now the only visible node glyph, so they're centered
    // directly on (d.x, d.y) with size floored at ICON_MIN_SIZE so even
    // a degree-1 file/commit remains legible.
    icons
      .attr('width', iconSize)
      .attr('height', iconSize)
      .attr('x', function(d) { return d.x - iconSize(d) / 2; })
      .attr('y', function(d) { return d.y - iconSize(d) / 2; });
    // Labels sit just BELOW the node. Using transform so tspan x="0"
    // is relative to the label origin (lets multi-line wrapping via
    // tspan children render centered under the node without per-tick
    // x rewriting on every tspan). Gap below the circle scales up
    // slightly for bigger labels so the first line has room to
    // breathe against a thick stroke-width halo.
    trackLabels
      .attr('transform', function(d) {
        var gap = 4 + Math.round(trackLabelFontSize(d) * 0.25);
        return 'translate(' + d.x + ',' + (d.y + visualRadius(d) + gap) + ')';
      });
    hubLabels
      .attr('transform', function(d) {
        return 'translate(' + d.x + ',' + (d.y + visualRadius(d) + 3) + ')';
      });
  });
}

// openProvenancePanel fetches and displays the causal chain for a graph node
// in the fixed right-side drawer. Each upstream/downstream item is clickable
// to drill into that node's own provenance.
//
// Race-proofed: rapid clicks on a chain of nodes would otherwise let a slow
// earlier response overwrite the newer drawer. The token/abort pair mirrors
// fetchGraph above.
var provenanceFetchToken = 0;
var provenanceFetchController = null;

function openProvenancePanel(nodeId) {
  var panel = document.getElementById('provenance-panel');
  var titleEl = document.getElementById('provenance-title');
  var badge = document.getElementById('provenance-type-badge');
  var upstreamEl = document.getElementById('provenance-upstream');
  var downstreamEl = document.getElementById('provenance-downstream');

  if (provenanceFetchController) {
    try { provenanceFetchController.abort(); } catch (e) {}
  }
  provenanceFetchController = typeof AbortController === 'function' ? new AbortController() : null;
  var myToken = ++provenanceFetchToken;
  var signal = provenanceFetchController ? provenanceFetchController.signal : undefined;

  fetch(buildProjectUrl('provenance/' + encodeURIComponent(nodeId)), { signal: signal })
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if (myToken !== provenanceFetchToken) return;
      titleEl.textContent = data.node.title || data.node.id;
      badge.textContent = data.node.type;
      badge.className = 'type-badge type-' + data.node.type;

      var originEl = document.getElementById('provenance-origin');
      if (originEl) {
        var n = data.node;
        var hasProv = n.created_by_agent || n.created_by_model || n.created_by_role || n.created_by_cli_ver;
        if (hasProv) {
          var fmt = function(v) { return v || 'unknown'; };
          originEl.textContent = 'Created by: ' + fmt(n.created_by_agent) + ' / ' +
            fmt(n.created_by_model) + ' / ' + fmt(n.created_by_role) + ' / ' + fmt(n.created_by_cli_ver);
          originEl.className = 'provenance-origin';
        } else {
          originEl.className = 'provenance-origin hidden';
        }
      }

      upstreamEl.innerHTML = '';
      (data.upstream || []).forEach(function(link) {
        var li = document.createElement('li');
        var rel = document.createElement('span');
        rel.className = 'provenance-rel';
        rel.textContent = link.relationship;
        var label = document.createElement('span');
        label.textContent = link.title || link.id;
        li.appendChild(rel);
        li.appendChild(label);
        li.onclick = function() { openProvenancePanel(link.id); };
        upstreamEl.appendChild(li);
      });

      downstreamEl.innerHTML = '';
      (data.downstream || []).forEach(function(link) {
        var li = document.createElement('li');
        var rel = document.createElement('span');
        rel.className = 'provenance-rel';
        rel.textContent = link.relationship;
        var label = document.createElement('span');
        label.textContent = link.title || link.id;
        li.appendChild(rel);
        li.appendChild(label);
        li.onclick = function() { openProvenancePanel(link.id); };
        downstreamEl.appendChild(li);
      });

      panel.classList.remove('hidden');
    })
    .catch(function(err) {
      if (err && err.name === 'AbortError') return;
      if (myToken !== provenanceFetchToken) return;
      console.error('provenance fetch failed', err);
    });
}

(function() {
  var closeBtn = document.getElementById('provenance-close');
  if (closeBtn) {
    closeBtn.addEventListener('click', function() {
      document.getElementById('provenance-panel').classList.add('hidden');
    });
  }
})();

// openSessionDetail switches to the sessions view and highlights a specific session.
function openSessionDetail(sessionId) {
  currentView = 'sessions';
  document.querySelectorAll('.nav-btn').forEach(function(b) {
    b.classList.toggle('active', b.dataset.view === 'sessions');
  });
  document.querySelectorAll('.view').forEach(function(v) {
    v.classList.toggle('active', v.id === 'v-sessions');
  });
  // Open the transcript directly — don't just highlight the list row.
  if (sessions.length === 0) {
    fetchSessions().then(function() { openTranscript(sessionId); });
  } else {
    openTranscript(sessionId);
  }
}

// highlightSession scrolls to and briefly highlights a session row by ID.
function highlightSession(sessionId) {
  var el = document.querySelector('[data-session-id="' + sessionId + '"]');
  if (!el) return;
  el.scrollIntoView({ behavior: 'smooth', block: 'center' });
  el.style.outline = '2px solid var(--accent)';
  setTimeout(function() { el.style.outline = ''; }, 2000);
}

/* ── Terminal feature gate ─────────────────────────────────── */

// terminalFeatureAvailable tracks whether the backend has terminal routes.
// Set by probeTerminalFeature(); checked by the liveness poll to gate requests.
var terminalFeatureAvailable = undefined; // undefined = still probing, true = available, false = not available

// probeTerminalFeature checks once at init whether the backend has the
// terminal routes registered (WIPNOTE_TERMINAL env var). On 404 (or
// any network error) the open-terminal button is hidden so the feature
// is invisible when not enabled. Called once from the startup sequence.
function probeTerminalFeature() {
  fetch(buildProjectUrl('terminal/sessions'))
    .then(function(r) {
      if (r.status === 404) {
        terminalFeatureAvailable = false;
        var btn = document.getElementById('open-terminal-btn');
        if (btn) btn.style.display = 'none';
      } else if (r.ok) {
        terminalFeatureAvailable = true;
      } else {
        terminalFeatureAvailable = false;
        var btn = document.getElementById('open-terminal-btn');
        if (btn) btn.style.display = 'none';
      }
    })
    .catch(function() {
      terminalFeatureAvailable = false;
      var btn = document.getElementById('open-terminal-btn');
      if (btn) btn.style.display = 'none';
    });
}

/* ── Embedded terminal (ttyd sidecar) ─────────────────────── */

// openTerminal starts a ttyd sidecar and shows the overlay iframe.
function openTerminal() {
  var workItem = window.wipnoteActiveWorkItem || '';
  fetch(buildProjectUrl('terminal/start'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ work_item: workItem })
  })
  .then(function(r) { return r.json(); })
  .then(function(data) {
    if (data.error) { alert('Terminal error: ' + data.error); return; }
    terminalPid = data.pid;
    var frame = document.getElementById('terminal-frame');
    var title = document.getElementById('terminal-title');
    var overlay = document.getElementById('terminal-overlay');
    frame.src = data.url;
    title.textContent = workItem ? ('Claude Terminal — ' + workItem) : 'Claude Terminal';
    overlay.classList.remove('hidden');
  })
  .catch(function(err) { alert('Could not start terminal: ' + err); });
}

// closeTerminal stops the sidecar and hides the overlay.
function closeTerminal() {
  var overlay = document.getElementById('terminal-overlay');
  var frame = document.getElementById('terminal-frame');
  overlay.classList.add('hidden');
  frame.src = 'about:blank';
  if (terminalPid) {
    var pid = terminalPid;
    terminalPid = null;
    fetch(buildProjectUrl('terminal/stop'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pid: pid })
    }).catch(function() {});
  }
}

// Wire the Terminal nav button — opens launcher modal instead of posting directly.
var openTerminalBtn = document.getElementById('open-terminal-btn');
if (openTerminalBtn) {
  openTerminalBtn.addEventListener('click', openLauncherModal);
}

// Wire the close button inside the overlay.
var terminalCloseBtn = document.getElementById('terminal-close');
if (terminalCloseBtn) {
  terminalCloseBtn.addEventListener('click', closeTerminal);
}

// Best-effort stop on page unload — stop individual legacy pid and all panes.
window.addEventListener('beforeunload', function() {
  if (terminalPid) {
    navigator.sendBeacon(
      buildProjectUrl('terminal/stop'),
      JSON.stringify({ pid: terminalPid })
    );
  }
  // Stop all multi-pane sessions via /api/terminal/stop-all beacon.
  navigator.sendBeacon(
    buildProjectUrl('terminal/stop-all'),
    new Blob([JSON.stringify({})], { type: 'application/json' })
  );
});

/* ── Launcher modal ────────────────────────────────────────── */

// Launcher modal state
var launcherWorkItemId = '';
var launcherWorkItemTitle = '';
var launcherSearchTimer = null;
var launcherAllWorkItems = null; // cached feature list for client-side filtering

// debounce returns a function that delays invoking fn until after ms milliseconds
// have elapsed since the last call. Simple closure, no external dependencies.
function debounce(fn, ms) {
  var timer = null;
  return function() {
    var ctx = this;
    var args = arguments;
    clearTimeout(timer);
    timer = setTimeout(function() { fn.apply(ctx, args); }, ms);
  };
}

// buildLaunchPayload is a pure function that assembles the JSON body for
// POST /api/terminal/start from a plain form-state object.
// It omits work_item and cwd_kind when empty/default.
function buildLaunchPayload(formState) {
  var payload = {
    agent: formState.agent || 'claude',
    mode: formState.mode || 'dev'
  };
  if (formState.work_item) {
    payload.work_item = formState.work_item;
  }
  if (formState.cwd_kind && formState.cwd_kind !== 'main') {
    payload.cwd_kind = formState.cwd_kind;
  }
  return payload;
}
// Expose for testability
window.buildLaunchPayload = buildLaunchPayload;

// openLauncherModal shows the launcher modal and traps focus inside it.
function openLauncherModal() {
  var modal = document.getElementById('launcher-modal');
  var backdrop = document.getElementById('launcher-backdrop');
  if (!modal || !backdrop) return;

  // Pre-populate work item from the currently active work detail view.
  var activeId = window.wipnoteActiveWorkItem || '';
  launcherWorkItemId = '';
  launcherWorkItemTitle = '';

  var searchInput = document.getElementById('launcher-work-item-search');
  var hiddenInput = document.getElementById('launcher-work-item');
  var cwdSelect = document.getElementById('launcher-cwd-kind');
  var cwdHint = document.getElementById('launcher-cwd-hint');
  var errorEl = document.getElementById('launcher-error');
  var agentSelect = document.getElementById('launcher-agent');
  var modeSelect = document.getElementById('launcher-mode');

  // Reset form state
  if (agentSelect) agentSelect.value = 'claude';
  if (modeSelect) modeSelect.value = 'dev';
  if (searchInput) searchInput.value = '';
  if (hiddenInput) hiddenInput.value = '';
  if (cwdSelect) { cwdSelect.value = 'main'; cwdSelect.disabled = true; }
  if (cwdHint) cwdHint.style.display = '';
  if (errorEl) { errorEl.textContent = ''; errorEl.classList.add('hidden'); }
  hideLauncherDropdown();

  // If there's an active work item, pre-populate the search
  if (activeId) {
    launcherWorkItemId = activeId;
    if (hiddenInput) hiddenInput.value = activeId;
    if (searchInput) searchInput.value = activeId;
    if (cwdSelect) cwdSelect.disabled = false;
    if (cwdHint) cwdHint.style.display = 'none';
  }

  setLauncherSubmitSpinner(false);

  backdrop.classList.remove('hidden');
  modal.classList.remove('hidden');
  backdrop.setAttribute('aria-hidden', 'false');

  // Sync pane-cap state (Create button disabled + warning when ≥ PANE_MAX).
  // Inlined here instead of via a late-assignment wrapper so the logic fires
  // even when the modal opens via openTerminalBtn's listener that captured
  // the original function reference.
  updateLauncherCapState();

  // Focus the first focusable element (agent select)
  if (agentSelect) agentSelect.focus();
}

// closeLauncherModal hides the modal and returns focus to the trigger button.
function closeLauncherModal() {
  var modal = document.getElementById('launcher-modal');
  var backdrop = document.getElementById('launcher-backdrop');
  if (!modal || !backdrop) return;

  hideLauncherDropdown();
  modal.classList.add('hidden');
  backdrop.classList.add('hidden');
  backdrop.setAttribute('aria-hidden', 'true');

  // Return focus to the open terminal button
  var btn = document.getElementById('open-terminal-btn');
  if (btn) btn.focus();
}

// setLauncherSubmitSpinner toggles the spinner and disabled state on the submit btn.
function setLauncherSubmitSpinner(loading) {
  var btn = document.getElementById('launcher-submit');
  var label = document.getElementById('launcher-submit-label');
  var spinner = document.getElementById('launcher-submit-spinner');
  if (!btn) return;
  btn.disabled = loading;
  if (label) label.textContent = loading ? 'Launching…' : 'Launch';
  if (spinner) spinner.classList.toggle('hidden', !loading);
}

// hideLauncherDropdown hides the work-item dropdown list.
function hideLauncherDropdown() {
  var dd = document.getElementById('launcher-work-item-dropdown');
  if (dd) dd.classList.add('hidden');
}

// renderLauncherDropdown populates and shows the dropdown with items.
function renderLauncherDropdown(items) {
  var dd = document.getElementById('launcher-work-item-dropdown');
  if (!dd) return;
  dd.innerHTML = '';

  if (!items || items.length === 0) {
    var empty = document.createElement('div');
    empty.className = 'launcher-dropdown-empty';
    empty.textContent = 'No matching work items';
    dd.appendChild(empty);
    dd.classList.remove('hidden');
    return;
  }

  items.slice(0, 10).forEach(function(item) {
    var el = document.createElement('div');
    el.className = 'launcher-dropdown-item';
    el.setAttribute('role', 'option');
    el.setAttribute('data-id', item.id || '');
    el.setAttribute('data-title', item.title || '');
    el.tabIndex = 0;

    var title = document.createElement('span');
    title.className = 'item-title';
    title.textContent = item.title || item.id || '';
    el.appendChild(title);

    var meta = document.createElement('span');
    meta.className = 'item-meta';
    meta.textContent = (item.id || '') + (item.type ? ' · ' + item.type : '') + (item.status ? ' · ' + item.status : '');
    el.appendChild(meta);

    el.addEventListener('click', function() { selectLauncherWorkItem(item); });
    el.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        selectLauncherWorkItem(item);
      }
    });
    dd.appendChild(el);
  });

  dd.classList.remove('hidden');
}

// selectLauncherWorkItem handles selecting a work item from the dropdown.
function selectLauncherWorkItem(item) {
  launcherWorkItemId = item.id || '';
  launcherWorkItemTitle = item.title || '';

  var searchInput = document.getElementById('launcher-work-item-search');
  var hiddenInput = document.getElementById('launcher-work-item');
  var cwdSelect = document.getElementById('launcher-cwd-kind');
  var cwdHint = document.getElementById('launcher-cwd-hint');

  if (searchInput) searchInput.value = item.title ? (item.id + ' — ' + item.title) : item.id;
  if (hiddenInput) hiddenInput.value = launcherWorkItemId;
  if (cwdSelect) cwdSelect.disabled = false;
  if (cwdHint) cwdHint.style.display = 'none';

  hideLauncherDropdown();
  if (searchInput) searchInput.focus();
}

// searchWorkItems fetches the feature list (cached after first call) and
// filters client-side since /api/features does not support ?q=.
// Gracefully shows "search unavailable" if the endpoint fails.
function searchWorkItems(query) {
  var q = (query || '').trim().toLowerCase();

  function filterAndRender(items) {
    if (!q) { hideLauncherDropdown(); return; }
    var filtered = items.filter(function(item) {
      return (item.title && item.title.toLowerCase().indexOf(q) !== -1) ||
             (item.id && item.id.toLowerCase().indexOf(q) !== -1);
    });
    renderLauncherDropdown(filtered);
  }

  if (launcherAllWorkItems !== null) {
    filterAndRender(launcherAllWorkItems);
    return;
  }

  fetch(buildProjectUrl('features'))
    .then(function(r) {
      if (!r.ok) throw new Error('features unavailable');
      return r.json();
    })
    .then(function(data) {
      launcherAllWorkItems = data || [];
      filterAndRender(launcherAllWorkItems);
    })
    .catch(function() {
      var dd = document.getElementById('launcher-work-item-dropdown');
      if (!dd) return;
      dd.innerHTML = '';
      var msg = document.createElement('div');
      msg.className = 'launcher-dropdown-empty';
      msg.textContent = 'Work item search unavailable';
      dd.appendChild(msg);
      dd.classList.remove('hidden');
    });
}

// debouncedSearchWorkItems is the debounced version used by the search input.
var debouncedSearchWorkItems = debounce(searchWorkItems, 250);

// submitLauncher reads the form and posts to /api/terminal/start.
function submitLauncher() {
  var agentSelect = document.getElementById('launcher-agent');
  var modeSelect = document.getElementById('launcher-mode');
  var hiddenInput = document.getElementById('launcher-work-item');
  var cwdSelect = document.getElementById('launcher-cwd-kind');
  var errorEl = document.getElementById('launcher-error');

  if (errorEl) { errorEl.textContent = ''; errorEl.classList.add('hidden'); }

  var formState = {
    agent: agentSelect ? agentSelect.value : 'claude',
    mode: modeSelect ? modeSelect.value : 'dev',
    work_item: hiddenInput ? hiddenInput.value : '',
    cwd_kind: cwdSelect ? cwdSelect.value : 'main'
  };

  var payload = buildLaunchPayload(formState);

  setLauncherSubmitSpinner(true);

  fetch(buildProjectUrl('terminal/start'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })
  .then(function(r) {
    if (!r.ok) {
      return r.json().then(function(data) {
        throw new Error(data.error || ('HTTP ' + r.status));
      }).catch(function(e) {
        // JSON parse failure on a non-JSON error body produces a SyntaxError;
        // surface the HTTP status instead. Real inner throws (Error from the
        // .then above) are rethrown as-is so the user sees the server's error.
        if (e instanceof SyntaxError) {
          throw new Error('HTTP ' + r.status);
        }
        throw e;
      });
    }
    return r.json();
  })
  .then(function(data) {
    setLauncherSubmitSpinner(false);
    if (data.error) {
      showLauncherError(data.error);
      return;
    }
    // Render a floating pane if we have a session id and port.
    if (data.id && data.port) {
      renderPane(data);
      closeLauncherModal();
      updateLauncherCapState();
      return;
    }
    // Fallback: legacy overlay for old API shape (pid+url).
    if (data.pid) terminalPid = data.pid;
    var frame = document.getElementById('terminal-frame');
    var title = document.getElementById('terminal-title');
    var overlay = document.getElementById('terminal-overlay');
    if (frame && data.url) frame.src = data.url;
    if (title) {
      var wid = payload.work_item || '';
      title.textContent = wid ? ('Terminal — ' + wid) : 'Claude Terminal';
    }
    if (overlay) overlay.classList.remove('hidden');
    closeLauncherModal();
  })
  .catch(function(err) {
    setLauncherSubmitSpinner(false);
    showLauncherError(err.message || 'Could not start terminal');
  });
}

// showLauncherError displays an error message in the modal.
function showLauncherError(msg) {
  var el = document.getElementById('launcher-error');
  if (!el) return;
  el.textContent = msg;
  el.classList.remove('hidden');
}

// Focus trap: keep Tab cycling within the modal while it's open.
function launcherFocusTrap(e) {
  var modal = document.getElementById('launcher-modal');
  if (!modal || modal.classList.contains('hidden')) return;

  var focusable = modal.querySelectorAll(
    'select, input:not([type="hidden"]), button:not(:disabled), [tabindex="0"]'
  );
  if (!focusable.length) return;

  var first = focusable[0];
  var last = focusable[focusable.length - 1];

  if (e.key === 'Tab') {
    if (e.shiftKey) {
      if (document.activeElement === first) {
        e.preventDefault();
        last.focus();
      }
    } else {
      if (document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  } else if (e.key === 'Escape') {
    e.preventDefault();
    closeLauncherModal();
  }
}

// Wire keyboard events for the modal.
document.addEventListener('keydown', launcherFocusTrap);

// Wire the close, cancel, submit buttons.
var launcherCloseBtn = document.getElementById('launcher-close');
if (launcherCloseBtn) launcherCloseBtn.addEventListener('click', closeLauncherModal);

var launcherCancelBtn = document.getElementById('launcher-cancel');
if (launcherCancelBtn) launcherCancelBtn.addEventListener('click', closeLauncherModal);

var launcherSubmitBtn = document.getElementById('launcher-submit');
if (launcherSubmitBtn) {
  launcherSubmitBtn.addEventListener('click', submitLauncher);
  launcherSubmitBtn.addEventListener('keydown', function(e) {
    if (e.key === 'Enter') { e.preventDefault(); submitLauncher(); }
  });
}

// Close modal when clicking the backdrop.
var launcherBackdrop = document.getElementById('launcher-backdrop');
if (launcherBackdrop) launcherBackdrop.addEventListener('click', closeLauncherModal);

// Wire the work-item search input for debounced search.
var launcherSearchInput = document.getElementById('launcher-work-item-search');
if (launcherSearchInput) {
  launcherSearchInput.addEventListener('input', function() {
    var val = this.value.trim();
    // Clear selection if user edits the field after a pick
    var hiddenInput = document.getElementById('launcher-work-item');
    var cwdSelect = document.getElementById('launcher-cwd-kind');
    var cwdHint = document.getElementById('launcher-cwd-hint');
    launcherWorkItemId = '';
    if (hiddenInput) hiddenInput.value = '';
    if (cwdSelect) { cwdSelect.value = 'main'; cwdSelect.disabled = true; }
    if (cwdHint) cwdHint.style.display = '';
    debouncedSearchWorkItems(val);
  });
  launcherSearchInput.addEventListener('keydown', function(e) {
    var dd = document.getElementById('launcher-work-item-dropdown');
    if (!dd || dd.classList.contains('hidden')) return;
    var items = dd.querySelectorAll('.launcher-dropdown-item');
    var active = dd.querySelector('.launcher-dropdown-item[aria-selected="true"]');
    var idx = active ? Array.prototype.indexOf.call(items, active) : -1;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      var next = items[idx + 1] || items[0];
      if (active) active.removeAttribute('aria-selected');
      if (next) { next.setAttribute('aria-selected', 'true'); next.focus(); }
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      var prev = items[idx - 1] || items[items.length - 1];
      if (active) active.removeAttribute('aria-selected');
      if (prev) { prev.setAttribute('aria-selected', 'true'); prev.focus(); }
    } else if (e.key === 'Escape') {
      hideLauncherDropdown();
    }
  });
  // Hide dropdown on blur (delayed to allow click on item to fire first)
  launcherSearchInput.addEventListener('blur', function() {
    setTimeout(hideLauncherDropdown, 200);
  });
}

/* ── Multi-pane floating terminal windows ───────────────────── */

// paneRegistry maps session UUID → {el, sessionId, iframe, ended}
var paneRegistry = new Map();

// closedByUser tracks session IDs that were explicitly closed by the user.
// The backend keeps exited sessions in Sessions() for ~10s after stop; without
// this guard, the 4s liveness poll re-renders them as "Ended" panes for ~6s.
// IDs are removed from the set once the backend drops them from the inventory.
var closedByUser = new Set();

// PANE_MAX caps the number of simultaneous live panes.
var PANE_MAX = 6;

// buildPaneElement creates a draggable floating pane DOM element for session.
function buildPaneElement(session) {
  // Cascade position so panes don't perfectly overlap.
  var idx = paneRegistry.size;
  var left = 48 + idx * 32;
  var top = 48 + idx * 32;

  var pane = document.createElement('div');
  pane.className = 'terminal-pane';
  pane.style.left = left + 'px';
  pane.style.top = top + 'px';
  pane.setAttribute('data-session-id', session.id);

  // Titlebar
  var titlebar = document.createElement('div');
  titlebar.className = 'pane-titlebar';

  var label = document.createElement('span');
  label.className = 'pane-titlebar-label';
  var agentText = session.agent || 'terminal';
  var workText = session.work_item ? ' — ' + session.work_item : '';
  label.innerHTML = '<strong>' + escapeHtml(agentText) + '</strong>' + escapeHtml(workText);
  titlebar.appendChild(label);

  var closeBtn = document.createElement('button');
  closeBtn.className = 'pane-close';
  closeBtn.setAttribute('aria-label', 'Close terminal pane');
  closeBtn.textContent = '×'; // ×
  closeBtn.addEventListener('click', function() { closePane(session.id); });
  titlebar.appendChild(closeBtn);

  pane.appendChild(titlebar);

  // Body + iframe
  var body = document.createElement('div');
  body.className = 'pane-body';

  var iframe = document.createElement('iframe');
  // Defer setting iframe.src until the session reaches state="live". ttyd
  // binds its port asynchronously; pointing the iframe at an un-bound port
  // causes the browser to cache ERR_CONNECTION_REFUSED for that URL, which
  // prevents later reloads of the same port from working.
  if (session.state === 'live') {
    iframe.src = 'http://127.0.0.1:' + session.port;
  }
  iframe.setAttribute('allowfullscreen', '');
  body.appendChild(iframe);

  // Placeholder shown while the session is still pending.
  var placeholder = null;
  if (session.state !== 'live') {
    placeholder = document.createElement('div');
    placeholder.className = 'pane-placeholder';
    placeholder.textContent = 'Starting…';
    body.appendChild(placeholder);
  }

  pane.appendChild(body);

  // Drag via titlebar mouse events
  attachPaneDragHandlers(pane, titlebar);

  return { el: pane, sessionId: session.id, iframe: iframe, ended: false, port: session.port };
}

// syncPaneIframes flips iframe.src to the live ttyd URL once the session
// reports state="live". Called after renderAllPanes on every inventory poll.
function syncPaneIframes(sessionsArray) {
  var arr = sessionsArray || [];
  arr.forEach(function(session) {
    if (session.state !== 'live') return;
    var record = paneRegistry.get(session.id);
    if (!record || record.iframe.src) return;
    record.iframe.src = 'http://127.0.0.1:' + (session.port || record.port);
    var placeholder = record.el.querySelector('.pane-placeholder');
    if (placeholder) placeholder.remove();
  });
}

// attachPaneDragHandlers wires mousedown on titlebar to drag the pane.
function attachPaneDragHandlers(pane, titlebar) {
  var dragging = false;
  var startX = 0;
  var startY = 0;
  var origLeft = 0;
  var origTop = 0;

  titlebar.addEventListener('mousedown', function(e) {
    // Only drag on left-button; ignore clicks on the close button.
    if (e.button !== 0 || e.target.closest('.pane-close')) return;
    dragging = true;
    startX = e.clientX;
    startY = e.clientY;
    origLeft = parseInt(pane.style.left, 10) || 0;
    origTop = parseInt(pane.style.top, 10) || 0;
    e.preventDefault();
  });

  document.addEventListener('mousemove', function(e) {
    if (!dragging) return;
    pane.style.left = (origLeft + e.clientX - startX) + 'px';
    pane.style.top = (origTop + e.clientY - startY) + 'px';
  });

  document.addEventListener('mouseup', function() {
    dragging = false;
  });
}

// escapeHtml escapes < > & " characters for safe insertion into innerHTML.
function escapeHtml(str) {
  return (str || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// renderPane adds a new pane for session if not already in registry.
function renderPane(sessionData) {
  if (!sessionData || !sessionData.id) return;
  if (paneRegistry.has(sessionData.id)) return;

  var layer = document.getElementById('pane-layer');
  if (!layer) return;

  var record = buildPaneElement(sessionData);
  paneRegistry.set(sessionData.id, record);
  layer.appendChild(record.el);
}

// renderAllPanes syncs the registry with a sessions array from the API.
// Adds new panes (up to PANE_MAX), removes panes for sessions that are gone.
function renderAllPanes(sessionsArray) {
  var arr = sessionsArray || [];
  var liveIds = new Set(arr.map(function(s) { return s.id; }));

  // GC closedByUser: once the backend has dropped the id, stop suppressing it.
  closedByUser.forEach(function(id) {
    if (!liveIds.has(id)) closedByUser.delete(id);
  });

  // Add new panes for sessions not yet rendered, respecting PANE_MAX.
  arr.forEach(function(session) {
    if (closedByUser.has(session.id)) return;
    if (paneRegistry.has(session.id)) return;
    if (paneRegistry.size >= PANE_MAX) return;
    renderPane(session);
  });

  // Remove panes whose session has disappeared from the API response.
  paneRegistry.forEach(function(record, id) {
    if (!liveIds.has(id)) {
      removePaneFromDOM(id);
    }
  });
}

// markExitedPanes adds the 'ended' visual state to panes whose session is exited.
function markExitedPanes(sessionsArray) {
  var arr = sessionsArray || [];
  var stateById = {};
  arr.forEach(function(s) { stateById[s.id] = s.state; });

  paneRegistry.forEach(function(record, id) {
    var state = stateById[id];
    // Note: renderAllPanes already dropped panes whose id is absent from the
    // response, so here we only need to handle the exited state.
    if (state === 'exited' && !record.ended) {
      record.ended = true;
      record.el.classList.add('ended');

      // Add ended badge to titlebar if not already there.
      var titlebar = record.el.querySelector('.pane-titlebar');
      if (titlebar && !titlebar.querySelector('.pane-ended-badge')) {
        var badge = document.createElement('span');
        badge.className = 'pane-ended-badge';
        badge.textContent = 'Ended';
        titlebar.insertBefore(badge, titlebar.querySelector('.pane-close'));
      }

      // Add ended overlay over the iframe body.
      var body = record.el.querySelector('.pane-body');
      if (body && !body.querySelector('.pane-ended-overlay')) {
        var overlay = document.createElement('div');
        overlay.className = 'pane-ended-overlay';
        overlay.textContent = 'Session ended';
        body.appendChild(overlay);
      }
    }
  });
}

// closePane stops the session and removes the pane from DOM and registry.
function closePane(sessionId) {
  closedByUser.add(sessionId);
  fetch(buildProjectUrl('terminal/stop'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id: sessionId })
  }).catch(function() {});
  removePaneFromDOM(sessionId);
  updateLauncherCapState();
}

// removePaneFromDOM removes the pane element from the DOM and the registry.
function removePaneFromDOM(sessionId) {
  var record = paneRegistry.get(sessionId);
  if (!record) return;
  if (record.el && record.el.parentNode) {
    record.el.parentNode.removeChild(record.el);
  }
  paneRegistry.delete(sessionId);
}

// updateLauncherCapState disables the launcher submit button when at pane cap.
function updateLauncherCapState() {
  var btn = document.getElementById('launcher-submit');
  var label = document.getElementById('launcher-submit-label');
  var errorEl = document.getElementById('launcher-error');
  if (!btn) return;

  var atCap = paneRegistry.size >= PANE_MAX;
  btn.disabled = atCap;
  if (label) label.textContent = atCap ? 'Max panes open' : 'Launch';
  if (errorEl) {
    if (atCap) {
      errorEl.textContent = 'Maximum of ' + PANE_MAX + ' terminal panes are already open. Close one to launch a new session.';
      errorEl.classList.remove('hidden');
    } else {
      errorEl.textContent = '';
      errorEl.classList.add('hidden');
    }
  }
}

// Liveness poll: fetch /api/terminal/sessions every 4s, sync pane state.
// Skip at the doorway landing (/) — the terminal routes only exist under
// /p/<id>/ in multi-project serve, and polling the doorway would 404-spam.
// Also skip if terminal feature is not available (terminalFeatureAvailable === false).
setInterval(function() {
  if (!terminalFeatureAvailable || isDoorwayLanding()) return;
  fetch(buildProjectUrl('terminal/sessions'))
    .then(function(r) { return r.json(); })
    .then(function(data) {
      renderAllPanes(data);
      syncPaneIframes(data);
      markExitedPanes(data);
    })
    .catch(function() {});
}, 4000);

// Reload restore: on DOMContentLoaded fetch sessions and render all existing panes.
document.addEventListener('DOMContentLoaded', function() {
  if (isDoorwayLanding()) return;
  fetch(buildProjectUrl('terminal/sessions'))
    .then(function(r) { return r.json(); })
    .then(function(data) {
      renderAllPanes(data);
      syncPaneIframes(data);
      markExitedPanes(data);
    })
    .catch(function() {});
});

/* ── Dashboard-level Plan Review Sidebar ───────────────────────
   Hoisted from plan_page.gohtml so it renders for every plan the
   user opens — both YAML-backed plans with slice cards and legacy
   static-HTML plans that have no slices at all. */

var DASH_SIDEBAR_KEY = 'wipnote-dashboard-rail-collapsed';
var _dashApprovalListener = null; // event listener handle for teardown

(function initDashSidebarCollapse() {
  var detail = document.getElementById('plan-detail');
  var collapseBtn = document.getElementById('dash-sidebar-collapse');
  var reopenBtn = document.getElementById('dash-sidebar-reopen');
  if (!detail || !collapseBtn || !reopenBtn) return;

  function applyCollapsed(collapsed) {
    detail.classList.toggle('sidebar-collapsed', collapsed);
    collapseBtn.textContent = collapsed ? '‹' : '›';
    collapseBtn.title = collapsed ? 'Expand sidebar' : 'Collapse sidebar';
  }

  // Restore from localStorage
  applyCollapsed(localStorage.getItem(DASH_SIDEBAR_KEY) === '1');

  collapseBtn.addEventListener('click', function() {
    var collapsed = !detail.classList.contains('sidebar-collapsed');
    applyCollapsed(collapsed);
    localStorage.setItem(DASH_SIDEBAR_KEY, collapsed ? '1' : '0');
  });

  reopenBtn.addEventListener('click', function() {
    applyCollapsed(false);
    localStorage.setItem(DASH_SIDEBAR_KEY, '0');
  });
}());

function dashSidebarSyncRail(body) {
  var rail = document.getElementById('dash-review-rail');
  var dotsEl = document.getElementById('dash-rail-dots');
  var approvedEl = document.getElementById('dash-rail-approved');
  var totalEl = document.getElementById('dash-rail-total');
  var fillEl = document.getElementById('dash-rail-fill');
  var pendingList = document.getElementById('dash-rail-pending');
  var finalizeBtn = document.getElementById('dash-finalize-btn');
  if (!rail || !body) return;

  var sliceCards = body.querySelectorAll('.slice-card[data-slice]');
  if (!sliceCards.length) {
    // Also try slice-card elements with data-approval (YAML plans)
    sliceCards = body.querySelectorAll('.slice-card[data-approval]');
  }
  if (!sliceCards.length) {
    rail.style.display = 'none';
    if (finalizeBtn) finalizeBtn.style.display = 'none';
    return;
  }

  rail.style.display = '';
  if (finalizeBtn) finalizeBtn.style.display = '';

  var dots = dotsEl ? Array.from(dotsEl.querySelectorAll('.dash-rail-dot')) : [];
  var approved = 0;
  var pending = [];

  Array.from(sliceCards).forEach(function(card, i) {
    var approval = card.dataset.approval || 'pending';
    var sliceNum = card.id ? card.id.replace('slice-', '') : (i + 1);
    var nameEl = card.querySelector('.slice-name');
    var label = 'Slice ' + sliceNum + (nameEl ? ' (' + nameEl.textContent.trim().substring(0, 20) + ')' : '');

    if (dots[i]) dots[i].dataset.approval = approval;

    if (approval === 'approved') {
      approved++;
    } else {
      pending.push(label);
    }
  });

  var total = sliceCards.length;
  var pct = total > 0 ? Math.round(approved / total * 100) : 0;

  if (approvedEl) approvedEl.textContent = approved;
  if (totalEl) totalEl.textContent = total;
  if (fillEl) fillEl.style.width = pct + '%';
  if (finalizeBtn) finalizeBtn.disabled = (approved < total);

  if (pendingList) {
    if (pending.length === 0) {
      pendingList.innerHTML = '<li style="color:var(--text-muted);font-size:.8rem">All approved</li>';
    } else {
      pendingList.innerHTML = pending.map(function(p) {
        return '<li>' + p + '</li>';
      }).join('');
    }
  }
}

function dashSidebarBuildRail(planId, body) {
  var rail = document.getElementById('dash-review-rail');
  var dotsEl = document.getElementById('dash-rail-dots');
  if (!rail || !dotsEl) return;

  var sliceCards = body.querySelectorAll('.slice-card[data-slice]');
  if (!sliceCards.length) sliceCards = body.querySelectorAll('.slice-card[data-approval]');

  if (!sliceCards.length) {
    rail.style.display = 'none';
    var fb = document.getElementById('dash-finalize-btn');
    if (fb) fb.style.display = 'none';
    return;
  }

  // Build dot elements
  dotsEl.innerHTML = '';
  Array.from(sliceCards).forEach(function(card, i) {
    var sliceNum = card.id ? card.id.replace('slice-', '') : (i + 1);
    var nameEl = card.querySelector('.slice-name');
    var label = 'Slice ' + sliceNum + (nameEl ? ': ' + nameEl.textContent.trim() : '');
    var dot = document.createElement('a');
    dot.href = '#';
    dot.className = 'dash-rail-dot';
    dot.dataset.approval = card.dataset.approval || 'pending';
    dot.title = label;
    dot.setAttribute('aria-label', 'Jump to ' + label);
    dot.addEventListener('click', function(e) {
      e.preventDefault();
      // Scroll the body container to the slice card
      var bodyEl = document.getElementById('plan-detail-body');
      var target = card;
      if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
    dotsEl.appendChild(dot);
  });

  dashSidebarSyncRail(body);

  // Wire finalize button
  var finalizeBtn = document.getElementById('dash-finalize-btn');
  if (finalizeBtn) {
    finalizeBtn.addEventListener('click', function() {
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Finalizing...';
      fetch(buildProjectUrl('plans/' + planId + '/finalize'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' }
      }).then(function(r) {
        if (r.ok) {
          btn.textContent = 'Plan Finalized';
          btn.style.background = 'var(--approved, #22c55e)';
          return r.json().then(function(data) {
            renderFinalizeResult(data);
            plans = [];
            fetchPlans();
          });
        } else {
          return r.text().then(function(body) {
            btn.textContent = 'Finalize Plan';
            btn.disabled = false;
            alert(body || 'Not all sections approved.');
          });
        }
      }).catch(function() {
        btn.textContent = 'Finalize Plan';
        btn.disabled = false;
      });
    });
  }
}

// renderFinalizeResult renders the finalize success panel showing created
// features and agentic next-step commands. Uses the result div injected by
// the finalize click handler into the sidebar below the finalize button.
function renderFinalizeResult(data) {
  var panel = document.getElementById('dash-finalize-result');
  if (!panel) return;
  panel.style.display = '';

  var html = '<p style="color:var(--status-done,#22c55e);font-weight:600;margin:8px 0 4px">Plan finalized.</p>';

  var features = data.created_features || [];
  if (features.length > 0) {
    html += '<p style="font-size:0.78rem;color:var(--text-secondary);margin:4px 0 2px">Features created (' + features.length + '):</p>';
    html += '<ul style="margin:0 0 8px;padding-left:16px;font-size:0.78rem;">';
    features.forEach(function(fid) {
      html += '<li style="font-family:var(--font-mono,monospace);color:var(--text-muted)">' + fid + '</li>';
    });
    html += '</ul>';
  }

  if (data.next_command) {
    html += '<p style="font-size:0.75rem;color:var(--text-secondary);margin:8px 0 2px">Next — run the agentic generation (integrates review feedback):</p>';
    html += '<pre style="margin:0 0 4px;padding:6px 8px;background:var(--bg-tertiary);border-radius:4px;font-family:var(--font-mono,monospace);font-size:0.75rem;white-space:pre-wrap;word-break:break-all;user-select:text">' + data.next_command + '</pre>';
  }

  if (data.yolo_command) {
    html += '<p style="font-size:0.75rem;color:var(--text-muted);margin:4px 0 2px">Or autonomous mode:</p>';
    html += '<pre style="margin:0;padding:6px 8px;background:var(--bg-tertiary);border-radius:4px;font-family:var(--font-mono,monospace);font-size:0.75rem;white-space:pre-wrap;word-break:break-all;user-select:text">' + data.yolo_command + '</pre>';
  }

  panel.innerHTML = html;
}

// dashCollectBlockAnchors scans the loaded plan body for block-anchored
// elements and returns an ordered list of {anchor, label} the reviewer can pin
// a comment to (slice-8). Anchors come from elements carrying an explicit
// data-block-anchor attribute, or whose id matches the tightly-bounded
// slice-<n>-block-<name>-<idx> contract enforced server-side by validSectionRe.
// Returns [] when the plan renders no blocks, so the affordance hides cleanly.
function dashCollectBlockAnchors(body) {
  if (!body) return [];
  var seen = {};
  var out = [];
  var re = /^slice-\d+-block-[a-z0-9-]+-\d+$/;
  var els = body.querySelectorAll('[data-block-anchor], [id^="slice-"]');
  els.forEach(function(el) {
    var anchor = el.getAttribute('data-block-anchor') || el.id || '';
    if (!anchor || !re.test(anchor) || seen[anchor]) return;
    seen[anchor] = true;
    var label = el.getAttribute('data-block-label') ||
      (el.querySelector('h2,h3,h4,.block-title') ? el.querySelector('h2,h3,h4,.block-title').textContent.trim() : '') ||
      anchor;
    out.push({ anchor: anchor, label: label });
  });
  return out;
}

function dashSidebarSetupChat(planId, body) {
  var messagesEl = document.getElementById('dash-chat-messages');
  var inputEl = document.getElementById('dash-chat-input');
  var sendBtn = document.getElementById('dash-chat-send');
  var emptyEl = document.getElementById('dash-chat-empty');
  if (!messagesEl || !inputEl || !sendBtn) return;

  // Anchor affordance (slice-8): a select that lets the reviewer pin the next
  // comment to a specific plan block. When an anchor is chosen, sending posts a
  // block-level annotation (action='annotation') to /feedback instead of
  // chatting. The select is injected dynamically so index.html stays untouched,
  // and is hidden entirely when the plan renders no blocks.
  var anchorSelect = document.getElementById('dash-chat-anchor');
  var inputArea = inputEl.parentElement;
  if (!anchorSelect && inputArea) {
    anchorSelect = document.createElement('select');
    anchorSelect.id = 'dash-chat-anchor';
    anchorSelect.className = 'dash-chat-anchor';
    anchorSelect.title = 'Pin this comment to a plan block (leave blank to chat)';
    inputArea.insertBefore(anchorSelect, inputArea.firstChild);
  }
  if (anchorSelect) {
    var anchors = dashCollectBlockAnchors(body);
    anchorSelect.innerHTML = '';
    var opt0 = document.createElement('option');
    opt0.value = '';
    opt0.textContent = anchors.length ? 'Chat (no anchor)' : 'No plan blocks to annotate';
    anchorSelect.appendChild(opt0);
    anchors.forEach(function(a) {
      var o = document.createElement('option');
      o.value = a.anchor;
      o.textContent = 'Annotate: ' + a.label;
      anchorSelect.appendChild(o);
    });
    anchorSelect.style.display = anchors.length ? '' : 'none';
  }

  // Clear previous messages
  messagesEl.innerHTML = '';
  if (emptyEl) {
    var notice = document.createElement('p');
    notice.className = 'dash-chat-notice';
    notice.id = 'dash-chat-empty';
    notice.textContent = 'Ask about the plan design, slices, risks, or tradeoffs.';
    messagesEl.appendChild(notice);
  }

  function escHtmlDash(s) { var d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

  function renderMdDash(text) {
    var s = escHtmlDash(text);
    s = s.replace(/```(\w*)\n([\s\S]*?)```/g, function(_, lang, code) {
      return '<pre><code' + (lang ? ' class="language-' + lang + '"' : '') + '>' + code.trim() + '</code></pre>';
    });
    s = s.replace(/`([^`]+)`/g, '<code>$1</code>');
    s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/\*([^*]+)\*/g, '<em>$1</em>');
    s = s.replace(/^### (.+)$/gm, '<h4>$1</h4>');
    s = s.replace(/^## (.+)$/gm, '<h3>$1</h3>');
    s = s.replace(/^[*-] (.+)$/gm, '<li>$1</li>');
    s = s.replace(/(<li>.*<\/li>\n?)+/g, '<ul>$&</ul>');
    var blocks = s.split(/\n\n/);
    var html = blocks.map(function(block) {
      var trimmed = block.trim();
      if (!trimmed) return '';
      return '<p>' + trimmed.replace(/\n/g, '<br>') + '</p>';
    }).filter(Boolean).join('');
    return html || '<p></p>';
  }

  function addBubble(role, content) {
    if (!content || !content.trim()) return null;
    var emptyNotice = messagesEl.querySelector('.dash-chat-notice');
    if (emptyNotice) emptyNotice.style.display = 'none';
    var div = document.createElement('div');
    div.className = 'dash-chat-bubble';
    div.dataset.role = role;
    if (role === 'assistant') {
      div.innerHTML = renderMdDash(content);
    } else {
      div.textContent = content;
    }
    messagesEl.appendChild(div);
    messagesEl.scrollTop = messagesEl.scrollHeight;
    return div;
  }

  function setEnabled(enabled) {
    inputEl.disabled = !enabled;
    sendBtn.disabled = !enabled;
    if (enabled) inputEl.focus();
  }

  // Load previous messages
  fetch(buildProjectUrl('plans/' + planId + '/feedback'))
    .then(function(r) { return r.ok ? r.json() : null; })
    .then(function(data) {
      if (!data) return;
      var hasChat = data.chat_messages && data.chat_messages.length;
      var hasAnn = data.annotations && data.annotations.length;
      if (!hasChat && !hasAnn) return;
      var emptyNotice = messagesEl.querySelector('.dash-chat-notice');
      if (emptyNotice) emptyNotice.style.display = 'none';
      if (hasChat) data.chat_messages.forEach(function(m) { addBubble(m.role, m.content); });
      // Replay block-level annotations with their two-axis state so the loop is
      // visible on reload (slice-8).
      if (hasAnn) data.annotations.forEach(function(a) {
        var state = (a.consumed ? 'consumed' : 'new') + (a.resolved ? ' · resolved' : ' · open');
        addBubble('user', '@' + (a.anchor || a.section) + ' [' + state + ' → ' + (a.resolution_target || 'agent') + ']: ' + (a.comment || ''));
      });
    })
    .catch(function() {});

  // sendAnnotation pins the reviewer's comment to a specific plan block and
  // posts it as a block-level annotation (slice-8). consumed/resolved start
  // false (a freshly-authored note is unaddressed) and resolution_target
  // defaults to 'agent' — the loop's first reader is the executing agent.
  function sendAnnotation(anchor, text) {
    addBubble('user', '@' + anchor + ': ' + text);
    fetch(buildProjectUrl('plans/' + planId + '/feedback'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        section: anchor,
        action: 'annotation',
        value: text,
        anchor: anchor,
        consumed: false,
        resolved: false,
        resolution_target: 'agent'
      })
    }).then(function(r) {
      if (!r.ok) { addBubble('assistant', 'Could not save annotation (' + r.status + ').'); }
      setEnabled(true);
    }).catch(function(err) {
      addBubble('assistant', 'Network error saving annotation: ' + err.message);
      setEnabled(true);
    });
  }

  function sendMessage() {
    var text = inputEl.value.trim();
    if (!text || !planId) return;
    inputEl.value = '';
    setEnabled(false);

    var anchorVal = anchorSelect ? anchorSelect.value : '';
    if (anchorVal) {
      sendAnnotation(anchorVal, text);
      return;
    }
    addBubble('user', text);

    var emptyNotice = messagesEl.querySelector('.dash-chat-notice');
    if (emptyNotice) emptyNotice.style.display = 'none';
    var assistantBubble = document.createElement('div');
    assistantBubble.className = 'dash-chat-bubble';
    assistantBubble.dataset.role = 'assistant';
    messagesEl.appendChild(assistantBubble);
    var rawResponse = '';

    fetch(buildProjectUrl('plans/' + planId + '/chat'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: text })
    }).then(function(response) {
      if (response.status === 503) {
        assistantBubble.textContent = 'Chat unavailable — Claude CLI not found and no API key configured.';
        assistantBubble.style.color = 'var(--text-muted)';
        assistantBubble.style.fontStyle = 'italic';
        setEnabled(true);
        return;
      }
      if (!response.ok) {
        assistantBubble.textContent = 'Error: ' + response.statusText;
        setEnabled(true);
        return;
      }
      var reader = response.body.getReader();
      var decoder = new TextDecoder();
      var buffer = '';

      function processStream() {
        reader.read().then(function(result) {
          if (result.done) {
            if (rawResponse) assistantBubble.innerHTML = renderMdDash(rawResponse);
            setEnabled(true);
            return;
          }
          buffer += decoder.decode(result.value, { stream: true });
          var lines = buffer.split('\n');
          buffer = lines.pop() || '';
          lines.forEach(function(line) {
            line = line.trim();
            if (!line.startsWith('data: ')) return;
            var jsonStr = line.substring(6);
            try {
              var evt = JSON.parse(jsonStr);
              if (evt.type === 'chunk' && evt.text) {
                rawResponse += evt.text;
                assistantBubble.textContent = rawResponse;
                messagesEl.scrollTop = messagesEl.scrollHeight;
              } else if (evt.type === 'error') {
                assistantBubble.textContent += ' [Error: ' + evt.error + ']';
              } else if (evt.type === 'done') {
                if (rawResponse) assistantBubble.innerHTML = renderMdDash(rawResponse);
                setEnabled(true);
              }
            } catch (e) {}
          });
          processStream();
        }).catch(function() { setEnabled(true); });
      }
      processStream();
    }).catch(function(err) {
      assistantBubble.textContent = 'Network error: ' + err.message;
      setEnabled(true);
    });
  }

  sendBtn.addEventListener('click', sendMessage);
  inputEl.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMessage(); }
  });
}

function dashSidebarSetup(planId, body) {
  // Build the review rail from slice cards in the loaded content
  dashSidebarBuildRail(planId, body);

  // Set up chat panel (body enables the block-anchor annotation affordance).
  dashSidebarSetupChat(planId, body);

  // Listen for approval changes inside the plan content and sync the rail
  if (_dashApprovalListener) {
    body.removeEventListener('change', _dashApprovalListener);
  }
  _dashApprovalListener = function(e) {
    var el = e.target;
    if (el && el.dataset && (el.dataset.action === 'approve' || el.name && el.name.indexOf('slice-') === 0)) {
      // Small delay to let the in-template approval handler run first
      setTimeout(function() { dashSidebarSyncRail(body); }, 50);
    }
  };
  body.addEventListener('change', _dashApprovalListener);
}

function dashSidebarTeardown() {
  var body = document.getElementById('plan-detail-body');
  if (body && _dashApprovalListener) {
    body.removeEventListener('change', _dashApprovalListener);
    _dashApprovalListener = null;
  }
  // Reset chat panel
  var messagesEl = document.getElementById('dash-chat-messages');
  if (messagesEl) {
    messagesEl.innerHTML = '<p class="dash-chat-notice" id="dash-chat-empty">Ask about the plan design, slices, risks, or tradeoffs.</p>';
  }
  // Reset rail
  var rail = document.getElementById('dash-review-rail');
  if (rail) rail.style.display = 'none';
  var dotsEl = document.getElementById('dash-rail-dots');
  if (dotsEl) dotsEl.innerHTML = '';
  var fb = document.getElementById('dash-finalize-btn');
  if (fb) { fb.textContent = 'Finalize Plan'; fb.disabled = true; fb.style.background = ''; fb.style.display = ''; }
  var fr = document.getElementById('dash-finalize-result');
  if (fr) { fr.style.display = 'none'; fr.innerHTML = ''; }
}

/* ── Launcher Doctor hint ───────────────────────────────────── */
// renderDoctorHint returns a small inline element linking to the launcher
// doctor command. Call this from any stale-session or isolation-warning
// context to surface actionable guidance without navigating away.
// The link is informational — it copies the command to a terminal hint;
// it does NOT auto-invoke any CLI command.
// slice-9 (feat-dbe359c1): minimal additive hook; dashboard visual QA deferred.
function renderDoctorHint() {
  var hint = document.createElement('span');
  hint.className = 'doctor-hint';
  hint.title = 'Run this command in your terminal to diagnose launcher/worktree health';
  hint.textContent = 'Run: wipnote launcher doctor';
  hint.style.cssText = 'font-size:.78rem;color:var(--text-dim,#888);margin-left:8px;cursor:help;';
  return hint;
}

/* ── Slice-7: isolation-visibility extensions ─────────────────────────────── */

// _sessionFamilyFilter is the currently-active family filter value, or '' for none.
var _sessionFamilyFilter = '';
// _sessionHarnessFilter is the currently-active harness filter value, or '' for none.
var _sessionHarnessFilter = '';
// _sessionCollisionFilter is true when showing only sessions with claim collisions.
var _sessionCollisionFilter = false;

// renderSessionFilterRow injects a filter-pill row above the sessions table.
// Filters: harness pills + "Collision only" toggle. Additive — no existing
// table markup changed; the row is inserted before the sessions-body.
function renderSessionFilterRow() {
  var existing = document.getElementById('session-filter-row');
  if (existing) existing.remove();

  var harnesses = {};
  sessions.forEach(function(s) {
    if (s.harness || s.agent) harnesses[s.harness || s.agent] = true;
  });
  var harnessKeys = Object.keys(harnesses);
  // Compute hasAnyCollision BEFORE the early return so the Collision pill is
  // shown even when there is only one harness (single-harness collision case).
  var hasAnyCollision = sessions.some(function(s) { return s.claim_collision; });
  if (harnessKeys.length < 2 && !hasAnyCollision && !_sessionCollisionFilter) return; // no harness choice and no collision: filters not needed

  var row = document.createElement('div');
  row.id = 'session-filter-row';
  row.className = 'session-filter-row';

  // "All" pill
  var allPill = document.createElement('button');
  allPill.className = 'session-filter-pill' + (_sessionHarnessFilter === '' && !_sessionCollisionFilter ? ' active' : '');
  allPill.textContent = 'All';
  allPill.addEventListener('click', function() {
    _sessionHarnessFilter = '';
    _sessionCollisionFilter = false;
    renderSessions();
  });
  row.appendChild(allPill);

  // One pill per harness
  var _harnessLabels = {'claude-code': 'Claude', 'codex': 'Codex', 'gemini': 'Gemini'};
  harnessKeys.forEach(function(h) {
    var pill = document.createElement('button');
    pill.className = 'session-filter-pill' + (_sessionHarnessFilter === h ? ' active' : '');
    pill.textContent = _harnessLabels[h] || h;
    pill.addEventListener('click', function() {
      _sessionHarnessFilter = (_sessionHarnessFilter === h) ? '' : h;
      _sessionCollisionFilter = false;
      renderSessions();
    });
    row.appendChild(pill);
  });

  // Collision filter pill (only show if any collision exists)
  if (hasAnyCollision) {
    var colPill = document.createElement('button');
    colPill.className = 'session-filter-pill' + (_sessionCollisionFilter ? ' active' : '');
    colPill.textContent = 'Collision';
    colPill.addEventListener('click', function() {
      _sessionCollisionFilter = !_sessionCollisionFilter;
      _sessionHarnessFilter = '';
      renderSessions();
    });
    row.appendChild(colPill);
  }

  // Insert before the sessions table
  var tbl = document.querySelector('#v-sessions table');
  if (tbl && tbl.parentNode) tbl.parentNode.insertBefore(row, tbl);
}

// applySessionFilters returns the filtered session list for renderSessions.
function applySessionFilters(list) {
  return list.filter(function(s) {
    if (_sessionHarnessFilter && (s.harness || s.agent) !== _sessionHarnessFilter) return false;
    if (_sessionCollisionFilter && !s.claim_collision) return false;
    return true;
  });
}

// renderCollectorWarningBanner fetches /api/collector-status for the given
// sessionID and renders a stale warning when the collector is dead and the
// session is still active. Additive — shown above the session table or in
// a parent container; never replaces existing content.
function renderCollectorWarningBanner(sessionID, container) {
  if (!sessionID || !container) return;
  fetch(buildProjectUrl('otel/status', 'session=' + sessionID))
    .then(function(r) { return r.ok ? r.json() : null; })
    .then(function(data) {
      if (!data) return;
      if (data.alive) return; // healthy — no banner
      var banner = document.createElement('div');
      banner.className = 'collector-stale-banner';
      banner.textContent = 'OTel collector offline for session ' + sessionID.slice(0, 8) + ' — telemetry paused';
      banner.appendChild(renderDoctorHint());
      container.insertBefore(banner, container.firstChild);
    })
    .catch(function() {});
}

// Patch renderSessions to:
//   1. Apply harness/collision filters.
//   2. Append session-family and collision badges after the harness badge.
//   3. Insert the filter row above the table.
//
// This wraps the original renderSessions rather than replacing it so the
// existing single-session view behaviour is unchanged.
(function() {
  var _originalRenderSessions = renderSessions;
  renderSessions = function() {
    _originalRenderSessions();
    // Post-render pass: inject filter row and slice-7 badges.
    renderSessionFilterRow();
    _injectSlice7Badges();
    // Wire stale-collector warning banners for active sessions (slice-7 deliverable).
    // renderCollectorWarningBanner is best-effort/async: it fetches /api/collector-status
    // and inserts a banner above the sessions table when the collector is offline.
    // One banner per active session; duplicates are avoided by the banner's container-insert
    // (each call targets a fresh container derived from the session row).
    var body = document.getElementById('sessions-body');
    if (body) {
      var activeRows = body.querySelectorAll('tr.session-row.live');
      activeRows.forEach(function(tr) {
        var sid = tr.getAttribute('data-session-id');
        if (sid) renderCollectorWarningBanner(sid, tr.parentNode || body);
      });
    }
  };
})();

// _injectSlice7Badges walks the rendered session rows and adds family and
// collision badges where the API fields are set. Operates on the live DOM
// after renderSessions() has built it; never rebuilds the table.
function _injectSlice7Badges() {
  var body = document.getElementById('sessions-body');
  if (!body) return;

  // Build a lookup from session_id to session data.
  var lookup = {};
  sessions.forEach(function(s) { lookup[s.session_id] = s; });

  var rows = body.querySelectorAll('tr.session-row');
  rows.forEach(function(tr) {
    var sid = tr.getAttribute('data-session-id');
    var s = sid && lookup[sid];
    if (!s) return;

    var titleTd = tr.querySelector('td:first-child');
    if (!titleTd) return;

    // Remove any previously-injected slice-7 badges to avoid doubling.
    titleTd.querySelectorAll('.badge-family,.badge-collision').forEach(function(b) { b.remove(); });

    // Family badge — show short suffix of family ID when it differs from session_id.
    var fid = s.session_family_id;
    if (fid && fid !== s.session_id) {
      var famBadge = document.createElement('span');
      famBadge.className = 'badge-family';
      famBadge.textContent = 'fam:' + fid.slice(-6);
      famBadge.title = 'Session family: ' + fid;
      titleTd.appendChild(famBadge);
    }

    // Collision badge — shown when claim_collision is true.
    if (s.claim_collision) {
      var colBadge = document.createElement('span');
      colBadge.className = 'badge-collision';
      colBadge.textContent = 'COLLISION';
      colBadge.title = 'Claim collision: multiple sessions own this work item (' + (s.feature_id || '') + ')';
      titleTd.appendChild(colBadge);
    }
  });

  // Apply harness/collision filters: hide non-matching rows.
  rows.forEach(function(tr) {
    var sid = tr.getAttribute('data-session-id');
    var s = sid && lookup[sid];
    if (!s) return;
    var visible = true;
    if (_sessionHarnessFilter && (s.harness || s.agent) !== _sessionHarnessFilter) visible = false;
    if (_sessionCollisionFilter && !s.claim_collision) visible = false;
    tr.style.display = visible ? '' : 'none';
    // Also hide the preview row below this row.
    var next = tr.nextElementSibling;
    if (next && next.classList.contains('session-preview-row')) {
      next.style.display = (visible && next.style.display !== 'none') ? next.style.display : 'none';
    }
  });
}
