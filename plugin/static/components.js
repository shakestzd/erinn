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
      if (!events.length) {
        feed.innerHTML = '<p class="hg-feed-empty">No recent activity</p>';
        return;
      }
      feed.innerHTML = events.map(e => {
        const label = e.tool_name || e.type || 'event';
        const summary = e.summary || '';
        const durBadge = e.duration_ms > 0
          ? `<span class="hg-feed-badge hg-feed-badge-dur">${e.duration_ms}ms</span>` : '';
        const costBadge = e.cost_usd > 0
          ? `<span class="hg-feed-badge hg-feed-badge-cost">$${e.cost_usd.toFixed(3)}</span>` : '';
        return `
          <div class="hg-feed-item" data-event-type="${e.type || ''}" data-source="${e.source || ''}">
            <span class="hg-feed-time">${new Date(e.timestamp).toLocaleTimeString()}</span>
            <span class="hg-feed-tool">${label}</span>
            ${durBadge}${costBadge}
            <span class="hg-feed-summary">${summary}</span>
          </div>`;
      }).join('');
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
