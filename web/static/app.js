'use strict';

document.addEventListener('DOMContentLoaded', () => {
    const MAX_ROWS = 1000; // keeps a long-open tab from growing without bound
    const RETRY_MS = 5000;
    const KNOWN_LEVELS = new Set(['INFO', 'WARNING', 'ERROR', 'DEBUG']);

    const logTable = document.getElementById('log-body');
    const levelFilter = document.getElementById('level-filter');
    const typeFilter = document.getElementById('type-filter');
    const statusEl = document.getElementById('status');
    const modal = document.getElementById('add-log-modal');
    const addLogForm = document.getElementById('add-log-form');
    const formError = document.getElementById('form-error');

    let eventSource = null;
    let loadSeq = 0;
    // While a list load is in flight, live entries wait here so that
    // rendering the list does not wipe them out.
    let pending = null;
    const shown = new Set();

    levelFilter.addEventListener('change', reload);
    typeFilter.addEventListener('change', reload);
    document.getElementById('add-log-btn').addEventListener('click', openModal);
    document.querySelector('.close').addEventListener('click', closeModal);
    addLogForm.addEventListener('submit', submitLog);
    window.addEventListener('click', (event) => {
        if (event.target === modal) closeModal();
    });

    reload();

    function query() {
        const params = new URLSearchParams();
        if (levelFilter.value) params.set('level', levelFilter.value);
        if (typeFilter.value) params.set('type', typeFilter.value);
        const qs = params.toString();
        return qs ? '?' + qs : '';
    }

    function reload() {
        connect();
        fetchLogs();
    }

    function fetchLogs() {
        const seq = ++loadSeq;
        pending = [];
        fetch('/api/logs' + query())
            .then((response) => {
                if (!response.ok) throw new Error('HTTP ' + response.status);
                return response.json();
            })
            .then((logs) => {
                if (seq !== loadSeq) return; // a newer load replaced this one
                renderLogs(logs);
                const queued = pending;
                pending = null;
                queued.forEach(prependLog);
            })
            .catch((error) => {
                if (seq !== loadSeq) return;
                const queued = pending;
                pending = null;
                queued.forEach(prependLog);
                console.error('Error fetching logs:', error);
            });
    }

    // The API returns newest first; keep that order top to bottom.
    function renderLogs(logs) {
        shown.clear();
        logTable.replaceChildren();
        logs.forEach((log) => {
            shown.add(log.id);
            logTable.appendChild(buildRow(log));
        });
        if (logs.length === 0) showEmpty();
    }

    function onLiveLog(log) {
        if (pending) {
            pending.push(log);
            return;
        }
        prependLog(log);
    }

    function prependLog(log) {
        if (shown.has(log.id)) return;
        shown.add(log.id);
        const empty = logTable.querySelector('td.empty');
        if (empty) empty.parentElement.remove();
        logTable.insertBefore(buildRow(log), logTable.firstChild);
        while (logTable.rows.length > MAX_ROWS) {
            const last = logTable.rows[logTable.rows.length - 1];
            shown.delete(Number(last.dataset.id));
            last.remove();
        }
    }

    // Every field is set with textContent: log data is untrusted input.
    function buildRow(log) {
        const row = document.createElement('tr');
        row.dataset.id = String(log.id);
        const level = cell(log.level);
        if (KNOWN_LEVELS.has(log.level)) level.classList.add('level-' + log.level);
        const message = cell(log.message);
        message.classList.add('message');
        row.append(cell(new Date(log.timestamp).toLocaleString()), level, cell(log.type), message);
        return row;
    }

    function cell(text) {
        const td = document.createElement('td');
        td.textContent = text == null ? '' : String(text);
        return td;
    }

    function showEmpty() {
        const row = document.createElement('tr');
        const td = cell('No logs found');
        td.colSpan = 4;
        td.classList.add('empty');
        row.appendChild(td);
        logTable.appendChild(row);
    }

    function setStatus(text, cls) {
        statusEl.textContent = text;
        statusEl.className = 'status ' + cls;
    }

    // Exactly one EventSource at a time; filter changes replace it.
    function connect() {
        if (eventSource) eventSource.close();
        const es = new EventSource('/api/logs/stream' + query());
        eventSource = es;
        let lostConnection = false;

        es.onopen = () => {
            if (es !== eventSource) return;
            setStatus('live', 'live');
            // Entries written while we were away were not streamed to us.
            if (lostConnection) {
                lostConnection = false;
                fetchLogs();
            }
        };
        es.onmessage = (event) => {
            if (es !== eventSource) return;
            onLiveLog(JSON.parse(event.data));
        };
        es.addEventListener('resync', () => {
            if (es === eventSource) fetchLogs();
        });
        es.onerror = () => {
            if (es !== eventSource) return;
            lostConnection = true;
            setStatus('reconnecting...', 'reconnecting');
            // The browser retries by itself unless the server refused the
            // stream (e.g. 503 during a restart); then we retry.
            if (es.readyState === EventSource.CLOSED) {
                setTimeout(() => {
                    if (es === eventSource) reload();
                }, RETRY_MS);
            }
        };
    }

    function openModal() {
        formError.hidden = true;
        modal.style.display = 'block';
    }

    function closeModal() {
        modal.style.display = 'none';
        addLogForm.reset();
        formError.hidden = true;
    }

    function submitLog(event) {
        event.preventDefault();
        const logData = {
            level: document.getElementById('log-level').value,
            type: document.getElementById('log-type').value,
            message: document.getElementById('log-message').value,
        };

        fetch('/api/logs', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(logData),
        })
            .then((response) => {
                if (response.ok) return response.json();
                return response.text().then((text) => {
                    throw new Error(text.trim() || 'HTTP ' + response.status);
                });
            })
            .then(() => closeModal())
            .catch((error) => {
                formError.textContent = 'Failed to add log: ' + error.message;
                formError.hidden = false;
            });
    }
});