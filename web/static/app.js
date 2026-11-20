'use strict';

document.addEventListener('DOMContentLoaded', () => {
    const MAX_ROWS = 1000; // keeps a long-open tab from growing without bound
    const RETRY_MS = 5000;
    const FACETS_REFRESH_MS = 60000;
    const SEARCH_DEBOUNCE_MS = 300;
    const KNOWN_LEVELS = new Set(['INFO', 'WARNING', 'ERROR', 'DEBUG']);

    const appEl = document.getElementById('app');
    const loginEl = document.getElementById('login');
    const loginForm = document.getElementById('login-form');
    const loginError = document.getElementById('login-error');
    const logoutBtn = document.getElementById('logout-btn');
    const addLogBtn = document.getElementById('add-log-btn');
    const logTable = document.getElementById('log-body');
    const levelFilter = document.getElementById('level-filter');
    const typeFilter = document.getElementById('type-filter');
    const serviceFilter = document.getElementById('service-filter');
    const searchFilter = document.getElementById('search-filter');
    const attrFilter = document.getElementById('attr-filter');
    const filterError = document.getElementById('filter-error');
    const statusEl = document.getElementById('status');
    const modal = document.getElementById('add-log-modal');
    const addLogForm = document.getElementById('add-log-form');
    const formError = document.getElementById('form-error');

    let eventSource = null;
    let loadSeq = 0;
    // While a list load is in flight, live entries wait here so that
    // rendering the list does not wipe them out.
    let pending = null;
    let facetsTimer = null;
    let searchTimer = null;
    const shown = new Set();

    levelFilter.addEventListener('change', reload);
    typeFilter.addEventListener('change', reload);
    serviceFilter.addEventListener('change', reload);
    searchFilter.addEventListener('input', debouncedReload);
    attrFilter.addEventListener('input', debouncedReload);
    addLogBtn.addEventListener('click', openModal);
    document.querySelector('.close').addEventListener('click', closeModal);
    addLogForm.addEventListener('submit', submitLog);
    loginForm.addEventListener('submit', login);
    logoutBtn.addEventListener('click', logout);
    window.addEventListener('click', (event) => {
        if (event.target === modal) closeModal();
    });

    start();

    // start asks the server whether a login is needed, then shows the app.
    function start() {
        fetch('/api/session')
            .then((response) => response.json())
            .then((session) => {
                if (session.auth && !session.authenticated) {
                    showLogin();
                    return;
                }
                logoutBtn.hidden = !session.auth;
                // The write token never reaches the browser, so Add Log only
                // works when writes need no token.
                addLogBtn.hidden = !session.canWrite;
                loginEl.hidden = true;
                appEl.hidden = false;
                loadFacets();
                clearInterval(facetsTimer);
                facetsTimer = setInterval(loadFacets, FACETS_REFRESH_MS);
                reload();
            })
            .catch((error) => {
                setStatus('server unreachable', 'reconnecting');
                console.error('Error reading session:', error);
                setTimeout(start, RETRY_MS);
            });
    }

    function showLogin() {
        if (eventSource) eventSource.close();
        eventSource = null;
        clearInterval(facetsTimer);
        // Signed out: do not leave log data in the page, hidden or not.
        loadSeq++;
        pending = null;
        shown.clear();
        logTable.replaceChildren();
        fillSelect(typeFilter, [], 'All Types');
        fillSelect(serviceFilter, [], 'All Services');
        appEl.hidden = true;
        logoutBtn.hidden = true;
        loginEl.hidden = false;
        setStatus('signed out', 'reconnecting');
        document.getElementById('login-token').focus();
    }

    function login(event) {
        event.preventDefault();
        const input = document.getElementById('login-token');
        fetch('/api/session', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ token: input.value }),
        }).then((response) => {
            if (response.ok) {
                input.value = '';
                loginError.hidden = true;
                start();
                return;
            }
            loginError.textContent = response.status === 429
                ? 'Too many attempts. Wait a minute and try again.'
                : 'That token was not accepted.';
            loginError.hidden = false;
        }).catch(() => {
            loginError.textContent = 'Server unreachable.';
            loginError.hidden = false;
        });
    }

    function logout() {
        fetch('/api/session', { method: 'DELETE' }).finally(showLogin);
    }

    // parseAttrFilter turns "key=value" into [key, value]; null when empty.
    function parseAttrFilter() {
        const raw = attrFilter.value.trim();
        if (!raw) return null;
        const i = raw.indexOf('=');
        if (i <= 0) throw new Error('Attr filter must look like key=value.');
        return [raw.slice(0, i), raw.slice(i + 1)];
    }

    function query() {
        const params = new URLSearchParams();
        if (levelFilter.value) params.set('level', levelFilter.value);
        if (typeFilter.value) params.set('type', typeFilter.value);
        if (serviceFilter.value) params.set('service', serviceFilter.value);
        if (searchFilter.value) params.set('q', searchFilter.value);
        const attr = parseAttrFilter();
        if (attr) params.set('attr.' + attr[0], attr[1]);
        const qs = params.toString();
        return qs ? '?' + qs : '';
    }

    function debouncedReload() {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(reload, SEARCH_DEBOUNCE_MS);
    }

    function reload() {
        try {
            query();
        } catch (error) {
            filterError.textContent = error.message;
            filterError.hidden = false;
            return;
        }
        filterError.hidden = true;
        connect();
        fetchLogs();
    }

    // fillSelect keeps the current choice even if it is no longer recent.
    function fillSelect(select, values, allLabel) {
        const current = select.value;
        const options = [new Option(allLabel, '')];
        const all = new Set(values);
        if (current) all.add(current);
        Array.from(all).sort().forEach((v) => options.push(new Option(v, v)));
        select.replaceChildren(...options);
        select.value = current;
    }

    function fillDatalist(id, values) {
        document.getElementById(id).replaceChildren(...values.map((v) => new Option(v)));
    }

    function loadFacets() {
        fetch('/api/facets')
            .then((response) => {
                if (response.status === 401) throw new Error('unauthorized');
                if (!response.ok) throw new Error('HTTP ' + response.status);
                return response.json();
            })
            .then((facets) => {
                fillSelect(typeFilter, facets.types, 'All Types');
                fillSelect(serviceFilter, facets.services, 'All Services');
                fillDatalist('type-options', facets.types);
                fillDatalist('service-options', facets.services);
            })
            .catch((error) => {
                if (error.message === 'unauthorized') showLogin();
                else console.error('Error loading facets:', error);
            });
    }

    function fetchLogs() {
        const seq = ++loadSeq;
        pending = [];
        fetch('/api/logs' + query())
            .then((response) => {
                if (response.status === 401) {
                    showLogin();
                    throw new Error('signed out');
                }
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
        if (log.attrs && Object.keys(log.attrs).length > 0) {
            const attrs = document.createElement('div');
            attrs.className = 'attrs';
            attrs.textContent = JSON.stringify(log.attrs);
            message.appendChild(attrs);
        }
        row.append(cell(new Date(log.timestamp).toLocaleString()), level, cell(log.type), cell(log.service), message);
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
        td.colSpan = 5;
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
            // stream (503 during a restart, 401 after a token rotation);
            // start() tells which and either reconnects or asks to sign in.
            if (es.readyState === EventSource.CLOSED) {
                setTimeout(() => {
                    if (es === eventSource) start();
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

    function showFormError(message) {
        formError.textContent = message;
        formError.hidden = false;
    }

    function submitLog(event) {
        event.preventDefault();
        const logData = {
            level: document.getElementById('log-level').value,
            type: document.getElementById('log-type').value.trim(),
            service: document.getElementById('log-service').value.trim(),
            message: document.getElementById('log-message').value,
        };
        const rawAttrs = document.getElementById('log-attrs').value.trim();
        if (rawAttrs) {
            let attrs;
            try {
                attrs = JSON.parse(rawAttrs);
            } catch (error) {
                showFormError('Attributes are not valid JSON.');
                return;
            }
            if (attrs === null || typeof attrs !== 'object' || Array.isArray(attrs)) {
                showFormError('Attributes must be a JSON object, like {"user_id": 42}.');
                return;
            }
            logData.attrs = attrs;
        }

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
            .catch((error) => showFormError('Failed to add log: ' + error.message));
    }
});