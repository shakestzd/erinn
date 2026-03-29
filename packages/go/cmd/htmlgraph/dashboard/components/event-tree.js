/* ── <hg-event-tree> Web Component ─────────────────────────── */

class HgEventTree extends HTMLElement {
  constructor() {
    super();
    this.turns = [];
    this.featureTitles = {};
    this.expanded = new Set(JSON.parse(localStorage.getItem('hg-expanded') || '[]'));
    this._filterDebounce = null;
  }

  connectedCallback() {
    this.load();
    this.evtSource = new EventSource('/api/events/stream');
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
    try {
      var resp = await fetch('/api/events/tree?limit=' + limit);
      if (!resp.ok) return;
      this.turns = await resp.json();
    } catch(e) {
      this.turns = [];
    }
    await this.loadFeatureTitles();
    this.updateCount();
    this._populateDropdowns();
    this.render();
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
      var resp = await fetch('/api/features');
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

  featureBadge(featureId) {
    if (!featureId) return '';
    var title = this.featureTitles[featureId];
    var label = title ? (title.length > 25 ? title.substring(0, 22) + '...' : title) : featureId;
    return '<span class="badge badge-feature" title="' + esc(featureId) + '">' + esc(label) + '</span>';
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
    } else {
      this.expanded.add(eventId);
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
    if (!this.turns || this.turns.length === 0) {
      this.innerHTML = '<div class="empty-state">No activity yet. Start a Claude Code session to see activity.</div>';
      this._updateFilterCount(0, 0);
      return;
    }

    var filters = this.getFilterValues();
    var filtered = this.turns.filter((t) => this._turnMatchesFilters(t, filters));
    this._updateFilterCount(filtered.length, this.turns.length);
    this.innerHTML = filtered.map(t => this.renderTurn(t)).join('');
  }

  _updateFilterCount(shown, total) {
    var countEl = document.getElementById('filter-count');
    if (!countEl) return;
    countEl.textContent = (shown < total) ? shown + ' of ' + total : '';
  }

  renderTurn(turn) {
    var uq = turn.user_query;
    var isExp = this.expanded.has(uq.event_id);
    var hasChildren = turn.children && turn.children.length > 0;
    var expandIcon = hasChildren
      ? '<span class="expand-icon ' + (isExp ? 'expanded' : '') + '" data-toggle="' + esc(uq.event_id) + '">\u25B6</span>'
      : '<span class="expand-icon-spacer"></span>';

    var s = turn.stats || {};
    var statsHtml = '<span class="turn-stats">' + (s.tool_count || 0) + ' tools' + (s.error_count ? ', ' + s.error_count + ' errors' : '') + '</span>';
    var featureBdg = this.featureBadge(uq.feature_id);

    var html = '<div class="turn-group">'
      + '<div class="event-row depth-0 user-query-row"'
      + ' data-event-id="' + esc(uq.event_id) + '"'
      + ' data-timestamp="' + esc(uq.timestamp || '') + '">'
      + expandIcon
      + '<span class="event-time">' + formatTime(uq.timestamp) + '</span>'
      + '<span class="event-summary">' + esc(uq.input_summary || '') + '</span>'
      + featureBdg
      + statsHtml
      + '</div>';

    if (isExp && turn.children) {
      html += turn.children.map(c => this.renderEvent(c, 1)).join('');
    }
    html += '</div>';
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

    var subagentBadge = (isTask && evt.subagent_type)
      ? '<span class="badge badge-subagent">' + esc(evt.subagent_type) + '</span>'
      : '';
    var agentId = evt.agent_id || '';
    var agentLabel = agentId.replace(/^htmlgraph:/, '');
    var agentBadge = (agentId && agentId !== 'claude-code')
      ? '<span class="agent-badge agent-' + esc(agentClass(agentId)) + '" title="' + esc(agentId) + '">' + esc(agentLabel) + '</span>'
      : '';
    var statusBdg = '<span class="badge badge-status-' + (evt.status || 'unknown') + '">' + esc(evt.status || 'unknown') + '</span>';

    var padLeft = (depth + 1) * 1.25;
    var bgAlpha = 0.05 + depth * 0.08;

    var html = '<div class="event-row depth-' + depth + ' ' + borderClass + ' clickable-row"'
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
      + agentBadge
      + '<span class="event-summary">' + esc(evt.input_summary || evt.output_summary || '') + '</span>'
      + this.featureBadge(evt.feature_id)
      + statusBdg
      + '</div>';

    if (isExp && evt.children) {
      html += evt.children.map(c => this.renderEvent(c, depth + 1)).join('');
    }
    return html;
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
