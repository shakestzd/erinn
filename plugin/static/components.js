// HtmlGraph Browser-Native Web Components
// These components self-render from data-* attributes, replacing server-side template logic.

class HgWorkItem extends HTMLElement {
  connectedCallback() {
    const id = this.dataset.id || '';
    const type = this.dataset.type || 'feature';
    const status = this.dataset.status || 'todo';
    const priority = this.dataset.priority || 'medium';
    const title = this.dataset.title || id;

    this.innerHTML = `
      <div class="hg-card" data-status="${status}" data-priority="${priority}" data-type="${type}">
        <div class="hg-card-header">
          <span class="hg-badge hg-badge-type">${type}</span>
          <span class="hg-badge hg-badge-status">${status}</span>
          <span class="hg-badge hg-badge-priority">${priority}</span>
        </div>
        <h3 class="hg-card-title">${title}</h3>
        <code class="hg-card-id">${id}</code>
      </div>
    `;
  }

  static get observedAttributes() { return ['data-status', 'data-priority', 'data-title']; }
  attributeChangedCallback() { if (this.isConnected) this.connectedCallback(); }
}
customElements.define('hg-work-item', HgWorkItem);

class HgProgressBar extends HTMLElement {
  connectedCallback() {
    const completed = parseInt(this.dataset.completed || '0', 10);
    const total = parseInt(this.dataset.total || '0', 10);
    const pct = total > 0 ? Math.round((completed / total) * 100) : 0;

    this.innerHTML = `
      <div class="hg-progress">
        <div class="hg-progress-info">
          <span>${pct}% Complete</span>
          <span>${completed}/${total} tasks</span>
        </div>
        <div class="hg-progress-track">
          <div class="hg-progress-fill" style="width: ${pct}%"></div>
        </div>
      </div>
    `;
  }

  static get observedAttributes() { return ['data-completed', 'data-total']; }
  attributeChangedCallback() { if (this.isConnected) this.connectedCallback(); }
}
customElements.define('hg-progress-bar', HgProgressBar);

class HgActivityFeed extends HTMLElement {
  connectedCallback() {
    this._interval = null;
    this.innerHTML = '<div class="hg-feed"><p class="hg-feed-empty">Loading activity...</p></div>';
    this.refresh();
    this._interval = setInterval(() => this.refresh(), 5000);
  }

  disconnectedCallback() {
    if (this._interval) clearInterval(this._interval);
  }

  async refresh() {
    try {
      const resp = await fetch('/api/events/feed?limit=20');
      if (!resp.ok) return;
      const data = await resp.json();
      const events = data.events || [];
      const feed = this.querySelector('.hg-feed');
      // Build DOM nodes via textContent/dataset rather than innerHTML so
      // OTel-sourced summaries — which now include raw user prompts and
      // tool content — cannot inject HTML or script into the dashboard.
      feed.replaceChildren();
      if (!events.length) {
        const empty = document.createElement('p');
        empty.className = 'hg-feed-empty';
        empty.textContent = 'No recent activity';
        feed.appendChild(empty);
        return;
      }
      for (const e of events) {
        const item = document.createElement('div');
        item.className = 'hg-feed-item';
        item.dataset.eventType = e.type || '';
        item.dataset.source = e.source || '';

        const time = document.createElement('span');
        time.className = 'hg-feed-time';
        time.textContent = new Date(e.timestamp).toLocaleTimeString();
        item.appendChild(time);

        const tool = document.createElement('span');
        tool.className = 'hg-feed-tool';
        tool.textContent = e.tool_name || e.type || 'event';
        item.appendChild(tool);

        if (e.duration_ms > 0) {
          const dur = document.createElement('span');
          dur.className = 'hg-feed-badge hg-feed-badge-dur';
          dur.textContent = `${e.duration_ms}ms`;
          item.appendChild(dur);
        }
        if (e.cost_usd > 0) {
          const cost = document.createElement('span');
          cost.className = 'hg-feed-badge hg-feed-badge-cost';
          cost.textContent = `$${e.cost_usd.toFixed(3)}`;
          item.appendChild(cost);
        }

        const summary = document.createElement('span');
        summary.className = 'hg-feed-summary';
        summary.textContent = e.summary || '';
        item.appendChild(summary);

        feed.appendChild(item);
      }
    } catch (_) { /* server not available */ }
  }
}
customElements.define('hg-activity-feed', HgActivityFeed);

class HgStatusBadge extends HTMLElement {
  connectedCallback() {
    const status = this.dataset.status || 'todo';
    this.innerHTML = `<span class="hg-badge hg-badge-status" data-status="${status}">${status}</span>`;
  }
  static get observedAttributes() { return ['data-status']; }
  attributeChangedCallback() { if (this.isConnected) this.connectedCallback(); }
}
customElements.define('hg-status-badge', HgStatusBadge);
