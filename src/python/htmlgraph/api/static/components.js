/**
 * HtmlGraph Web Components
 *
 * Browser-native components that self-render from data attributes.
 * No server-side class computation needed — CSS :has() and data attributes
 * drive all visual state.
 */

// ============================================================
// <hg-status-badge> — Renders a status pill from data-status
// Usage: <hg-status-badge data-status="in-progress"></hg-status-badge>
// ============================================================
class HgStatusBadge extends HTMLElement {
  static get observedAttributes() {
    return ['data-status'];
  }

  connectedCallback() {
    this.render();
  }

  attributeChangedCallback() {
    this.render();
  }

  render() {
    const status = this.dataset.status || 'unknown';
    const label = status.replace(/_/g, ' ').replace(/-/g, ' ');
    this.textContent = label;
    this.setAttribute('title', `Status: ${label}`);
  }
}

// ============================================================
// <hg-work-item> — Self-rendering work item card
//
// Reads its own data attributes and renders content.
// Fetches full detail from /api/work-items/:id on click.
//
// Usage:
//   <hg-work-item
//     data-id="feat-abc123"
//     data-type="feature"
//     data-status="in-progress"
//     data-priority="high"
//     data-title="My Feature Title">
//   </hg-work-item>
// ============================================================
class HgWorkItem extends HTMLElement {
  static get observedAttributes() {
    return ['data-id', 'data-type', 'data-status', 'data-priority', 'data-title'];
  }

  connectedCallback() {
    this.render();
    this.addEventListener('click', this._handleClick.bind(this));
  }

  disconnectedCallback() {
    this.removeEventListener('click', this._handleClick.bind(this));
  }

  attributeChangedCallback() {
    if (this.isConnected) this.render();
  }

  render() {
    const id = this.dataset.id || '';
    const type = this.dataset.type || 'feature';
    const status = this.dataset.status || 'todo';
    const priority = this.dataset.priority || 'medium';
    const title = this.dataset.title || id.slice(0, 12);
    const shortId = id.slice(-8);

    this.innerHTML = `
      <div class="hg-card-header">
        <span class="hg-type-badge">${_esc(type)}</span>
        <hg-status-badge data-status="${_esc(status)}"></hg-status-badge>
      </div>
      <p class="hg-card-title" title="${_esc(title)}">${_esc(title)}</p>
      <div class="hg-card-footer">
        <span class="hg-card-id" title="${_esc(id)}">${_esc(shortId)}</span>
        <span class="hg-priority-dot" title="Priority: ${_esc(priority)}"></span>
      </div>
    `;
  }

  async _handleClick(e) {
    e.preventDefault();
    const id = this.dataset.id;
    if (!id) return;

    // Emit a custom event so the dashboard can handle navigation
    this.dispatchEvent(new CustomEvent('hg:work-item-click', {
      bubbles: true,
      detail: { id, type: this.dataset.type, status: this.dataset.status }
    }));
  }
}

// ============================================================
// <hg-live-counter> — Auto-updating counter badge
//
// Polls /api/stats and updates its own text content.
// Usage: <hg-live-counter data-metric="total_events" data-interval="5000">0</hg-live-counter>
// ============================================================
class HgLiveCounter extends HTMLElement {
  static get observedAttributes() {
    return ['data-metric', 'data-interval'];
  }

  connectedCallback() {
    this._metric = this.dataset.metric || 'total_events';
    this._interval = parseInt(this.dataset.interval || '10000', 10);
    this._startPolling();
  }

  disconnectedCallback() {
    this._stopPolling();
  }

  _startPolling() {
    this._stopPolling();
    this._timerId = setInterval(() => this._poll(), this._interval);
    // Initial fetch
    this._poll();
  }

  _stopPolling() {
    if (this._timerId) {
      clearInterval(this._timerId);
      this._timerId = null;
    }
  }

  async _poll() {
    try {
      const resp = await fetch('/api/initial-stats');
      if (!resp.ok) return;
      const data = await resp.json();
      const value = data[this._metric];
      if (value !== undefined) {
        const prev = this.textContent;
        this.textContent = value;
        if (String(prev) !== String(value)) {
          this.classList.add('hg-counter-updated');
          setTimeout(() => this.classList.remove('hg-counter-updated'), 600);
        }
      }
    } catch (_err) {
      // Silently ignore network errors — component just keeps last value
    }
  }
}

// ============================================================
// <hg-file-watcher> — Polls for dashboard data changes
//
// Dispatches 'hg:data-changed' on the document when the server
// reports new data (via ETag/Last-Modified comparison).
//
// Usage: <hg-file-watcher data-interval="5000"></hg-file-watcher>
// ============================================================
class HgFileWatcher extends HTMLElement {
  connectedCallback() {
    this._interval = parseInt(this.dataset.interval || '5000', 10);
    this._lastEtag = null;
    this._lastModified = null;
    this._startPolling();
  }

  disconnectedCallback() {
    this._stopPolling();
  }

  _startPolling() {
    this._stopPolling();
    this._timerId = setInterval(() => this._check(), this._interval);
  }

  _stopPolling() {
    if (this._timerId) {
      clearInterval(this._timerId);
      this._timerId = null;
    }
  }

  async _check() {
    try {
      const resp = await fetch('/api/change-token', { method: 'HEAD' });
      if (!resp.ok) return;

      const etag = resp.headers.get('ETag');
      const lastMod = resp.headers.get('Last-Modified');

      const changed =
        (etag && etag !== this._lastEtag) ||
        (lastMod && lastMod !== this._lastModified);

      if (changed) {
        this._lastEtag = etag;
        this._lastModified = lastMod;

        // Only fire after the first successful poll (skip initial baseline)
        if (this._seenFirst) {
          document.dispatchEvent(
            new CustomEvent('hg:data-changed', {
              detail: { etag, lastModified: lastMod }
            })
          );
        }
        this._seenFirst = true;
      }
    } catch (_err) {
      // Network error — keep polling
    }
  }
}

// ============================================================
// Helpers
// ============================================================
function _esc(str) {
  if (!str) return '';
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ============================================================
// Registration
// ============================================================
customElements.define('hg-status-badge', HgStatusBadge);
customElements.define('hg-work-item', HgWorkItem);
customElements.define('hg-live-counter', HgLiveCounter);
customElements.define('hg-file-watcher', HgFileWatcher);
