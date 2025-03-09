document.addEventListener('DOMContentLoaded', function() {
    // DOM elements
    const logTable = document.getElementById('log-body');
    const levelFilter = document.getElementById('level-filter');
    const typeFilter = document.getElementById('type-filter');
    const addLogBtn = document.getElementById('add-log-btn');
    const modal = document.getElementById('add-log-modal');
    const closeBtn = document.querySelector('.close');
    const addLogForm = document.getElementById('add-log-form');

    // Initial load of logs
    fetchLogs();

    // Set up event listeners
    levelFilter.addEventListener('change', fetchLogs);
    typeFilter.addEventListener('change', fetchLogs);
    addLogBtn.addEventListener('click', openModal);
    closeBtn.addEventListener('click', closeModal);
    addLogForm.addEventListener('submit', submitLog);

    // Close modal when clicking outside
    window.addEventListener('click', function(event) {
        if (event.target === modal) {
            closeModal();
        }
    });

    // Start SSE connection for real-time logs
    startEventSource();

    // Functions
    function fetchLogs() {
        const level = levelFilter.value;
        const type = typeFilter.value;
        
        let url = '/api/logs';
        const params = [];
        
        if (level) params.push(`level=${level}`);
        if (type) params.push(`type=${type}`);
        
        if (params.length > 0) {
            url += '?' + params.join('&');
        }

        fetch(url)
            .then(response => response.json())
            .then(logs => {
                renderLogs(logs);
            })
            .catch(error => {
                console.error('Error fetching logs:', error);
            });
    }

    function renderLogs(logs) {
        logTable.innerHTML = '';
        
        if (logs.length === 0) {
            const row = document.createElement('tr');
            row.innerHTML = '<td colspan="4" style="text-align: center;">No logs found</td>';
            logTable.appendChild(row);
            return;
        }

        logs.forEach(log => {
            addLogToTable(log);
        });
    }

    function addLogToTable(log) {
        const row = document.createElement('tr');
        
        const timestamp = new Date(log.timestamp).toLocaleString();
        
        row.innerHTML = `
            <td>${timestamp}</td>
            <td class="level-${log.level}">${log.level}</td>
            <td>${log.type}</td>
            <td>${log.message}</td>
        `;
        
        // Add new logs at the top
        logTable.insertBefore(row, logTable.firstChild);
    }

    function startEventSource() {
        const level = levelFilter.value;
        const type = typeFilter.value;
        
        let url = '/api/logs/stream';
        const params = [];
        
        if (level) params.push(`level=${level}`);
        if (type) params.push(`type=${type}`);
        
        if (params.length > 0) {
            url += '?' + params.join('&');
        }

        const eventSource = new EventSource(url);
        
        eventSource.onmessage = function(event) {
            const log = JSON.parse(event.data);
            addLogToTable(log);
        };
        
        eventSource.onerror = function() {
            console.error('EventSource failed. Reconnecting in 5 seconds...');
            eventSource.close();
            setTimeout(startEventSource, 5000);
        };

        // Restart EventSource when filters change
        levelFilter.addEventListener('change', function() {
            eventSource.close();
            startEventSource();
        });
        
        typeFilter.addEventListener('change', function() {
            eventSource.close();
            startEventSource();
        });
    }

    function openModal() {
        modal.style.display = 'block';
    }

    function closeModal() {
        modal.style.display = 'none';
        addLogForm.reset();
    }

    function submitLog(event) {
        event.preventDefault();
        
        const logData = {
            level: document.getElementById('log-level').value,
            type: document.getElementById('log-type').value,
            message: document.getElementById('log-message').value
        };

        fetch('/api/logs', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(logData)
        })
        .then(response => {
            if (!response.ok) {
                throw new Error('Failed to add log');
            }
            return response.json();
        })
        .then(() => {
            closeModal();
        })
        .catch(error => {
            console.error('Error adding log:', error);
            alert('Failed to add log. Please try again.');
        });
    }
}); 