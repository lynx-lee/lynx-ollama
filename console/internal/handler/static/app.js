// ── Auth ─────────────────────────────────────────────────────────
const AUTH_KEY = 'ollama_web_api_key';

function getApiKey() {
    return localStorage.getItem(AUTH_KEY) || '';
}

function setApiKey(key) {
    localStorage.setItem(AUTH_KEY, key);
}

function clearApiKey() {
    localStorage.removeItem(AUTH_KEY);
}

async function verifyAuth() {
    const input = document.getElementById('authKeyInput');
    const errorEl = document.getElementById('authError');
    const btn = document.getElementById('authBtn');
    const key = input.value.trim();

    if (!key) {
        errorEl.textContent = '请输入 API Key';
        return;
    }

    btn.disabled = true;
    errorEl.textContent = '';

    try {
        const resp = await fetch('/api/auth/verify', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ key }),
        });
        const data = await resp.json();
        if (data.success) {
            setApiKey(key);
            showApp();
        } else {
            errorEl.textContent = data.error || 'API Key 无效';
            input.select();
        }
    } catch (err) {
        errorEl.textContent = '连接失败: ' + err.message;
    } finally {
        btn.disabled = false;
    }
}

function showApp() {
    document.getElementById('authScreen').classList.add('hidden');
    document.getElementById('app').style.display = 'flex';
    startAutoRefresh();
}

function logout() {
    clearApiKey();
    stopLogStream();
    disconnectStatusWs();
    document.getElementById('app').style.display = 'none';
    document.getElementById('authScreen').classList.remove('hidden');
    document.getElementById('authKeyInput').value = '';
    document.getElementById('authError').textContent = '';
}

// Handle Enter key on auth input
document.addEventListener('DOMContentLoaded', () => {
    const authInput = document.getElementById('authKeyInput');
    if (authInput) {
        authInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') verifyAuth();
        });
    }
});

// ── Theme Management ────────────────────────────────────────────
const THEME_KEY = 'ollama_web_theme';

// getThemePreference returns the stored preference: 'light', 'dark', or 'system'.
function getThemePreference() {
    return localStorage.getItem(THEME_KEY) || 'system';
}

// resolveTheme translates a preference into the actual theme ('light' or 'dark').
function resolveTheme(pref) {
    if (pref === 'light' || pref === 'dark') return pref;
    // 'system' — detect from OS
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

// applyTheme sets the data-theme attribute on <html> and updates all theme UIs.
function applyTheme(pref) {
    const actual = resolveTheme(pref);
    document.documentElement.setAttribute('data-theme', actual);

    // Update topbar theme buttons active state
    document.querySelectorAll('#topbarThemeBtns .topbar-theme-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.themeValue === pref);
    });
}

// setTheme is called by the user clicking a theme option.
function setTheme(pref) {
    localStorage.setItem(THEME_KEY, pref);
    applyTheme(pref);
}

// Listen for OS theme changes (relevant when preference is 'system').
window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
    if (getThemePreference() === 'system') {
        applyTheme('system');
    }
});

// Apply theme immediately on script load (before DOMContentLoaded) to avoid FOUC.
applyTheme(getThemePreference());

// ── State ────────────────────────────────────────────────────────
let logWs = null;
let logStreaming = false;
let statusWs = null;                   // WebSocket for status streaming
let statusWsReconnectTimer = null;     // reconnect timer
let statusWsReconnectDelay = 1000;     // initial reconnect delay (ms)
let currentStatus = null;
let currentPage = 'dashboard';         // track which page is active
let pageVisible = true;                // track browser tab visibility

// ── Navigation ──────────────────────────────────────────────────
document.querySelectorAll('.nav-item').forEach(item => {
    item.addEventListener('click', () => {
        const page = item.dataset.page;
        switchPage(page);
    });
});

function switchPage(page) {
    document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
    document.querySelector(`.nav-item[data-page="${page}"]`).classList.add('active');
    document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
    document.getElementById(`page-${page}`).classList.add('active');

    // Chat/Compare pages need #content to not scroll (they manage their own scroll)
    const content = document.getElementById('content');
    if (page === 'chat' || page === 'compare') {
        content.style.overflow = 'hidden';
        content.style.padding = '0';
    } else {
        content.style.overflow = '';
        content.style.padding = '';
    }

    currentPage = page;

    // Notify WebSocket to switch between full/lite mode
    sendStatusWsCommand({
        type: 'subscribe',
        mode: page === 'dashboard' ? 'full' : 'lite',
    });

    // Load data for the page
    switch (page) {
        case 'dashboard': refreshStatus(); onDashboardEnter(); break;
        case 'models': loadModels(); onDashboardLeave(); break;
        case 'chat': initChat(); onDashboardLeave(); break;
        case 'compare': initCompare(); onDashboardLeave(); break;
        case 'benchmark': initBenchmark(); onDashboardLeave(); break;
        case 'health': onDashboardLeave(); break; // Manual trigger
        case 'logs': loadLogs(); break;
        case 'config': loadConfig(); break;
        case 'gpu': renderGpuCards(currentStatus ? currentStatus.gpu : null); break;
    }
}

// ── Browser visibility detection ────────────────────────────────
document.addEventListener('visibilitychange', () => {
    pageVisible = !document.hidden;
    if (pageVisible) {
        // Tab became visible — resume WebSocket data stream
        sendStatusWsCommand({ type: 'resume' });
        // Also request a fresh snapshot in correct mode
        sendStatusWsCommand({
            type: 'subscribe',
            mode: currentPage === 'dashboard' ? 'full' : 'lite',
        });
    } else {
        // Tab hidden — pause WebSocket data stream to save resources
        sendStatusWsCommand({ type: 'pause' });
    }
});

// ── API Helpers ──────────────────────────────────────────────────
async function api(url, options = {}) {
    try {
        const headers = {
            'Content-Type': 'application/json',
            'X-API-Key': getApiKey(),
            ...(options.headers || {}),
        };
        const resp = await fetch(url, { ...options, headers });

        // Handle auth errors — redirect to login
        if (resp.status === 401 || resp.status === 403) {
            logout();
            throw new Error('认证失败，请重新登录');
        }

        const data = await resp.json();
        if (!data.success) throw new Error(data.error || 'Unknown error');
        return data.data;
    } catch (err) {
        console.error(`API Error [${url}]:`, err);
        throw err;
    }
}

function showToast(message, type = 'info') {
    const toast = document.getElementById('toast');
    toast.textContent = message;
    toast.className = `toast ${type} show`;
    setTimeout(() => toast.classList.remove('show'), 4000);
}

// ── Dashboard ───────────────────────────────────────────────────

// refreshStatus: HTTP fallback — used for initial load or when WebSocket is not available.
// In normal operation, status updates arrive via WebSocket push.
async function refreshStatus() {
    try {
        if (currentPage === 'dashboard') {
            const status = await api('/api/status');
            currentStatus = status;
            updateDashboard(status);
        } else {
            const lite = await api('/api/status/lite');
            if (currentStatus) {
                currentStatus.container = lite.container;
                currentStatus.running_models = lite.running_models;
                currentStatus.ollama_version = lite.ollama_version;
                currentStatus.api_reachable = lite.api_reachable;
                currentStatus.project_version = lite.project_version;
                if (lite.gpu) {
                    currentStatus.gpu = lite.gpu;
                }
            } else {
                currentStatus = lite;
            }
            updateTopbarBadges(currentStatus);
            if (currentPage === 'gpu') {
                renderGpuCards(currentStatus.gpu);
            }
        }
    } catch (err) {
        if (currentPage === 'dashboard') {
            showToast('无法获取服务状态: ' + err.message, 'error');
        }
    }
}

// handleStatusWSMessage processes a status message from the WebSocket.
function handleStatusWSMessage(msg) {
    const status = msg.data;
    if (!status) return;

    if (msg.mode === 'full') {
        currentStatus = status;
        if (currentPage === 'dashboard') {
            updateDashboard(status);
        } else {
            updateTopbarBadges(status);
        }
        // GPU page also benefits from full data
        if (currentPage === 'gpu') {
            renderGpuCards(status.gpu);
        }
    } else {
        // lite mode — merge into currentStatus (now includes GPU data)
        if (currentStatus) {
            currentStatus.container = status.container;
            currentStatus.running_models = status.running_models;
            currentStatus.ollama_version = status.ollama_version;
            currentStatus.api_reachable = status.api_reachable;
            currentStatus.project_version = status.project_version;
            if (status.gpu) {
                currentStatus.gpu = status.gpu;
            }
        } else {
            currentStatus = status;
        }
        updateTopbarBadges(currentStatus);
        // Auto-refresh GPU page when on it
        if (currentPage === 'gpu') {
            renderGpuCards(currentStatus.gpu);
        }
    }
}

// updateTopbarBadges updates the version info in the topbar (used by lite polling and WS).
function updateTopbarBadges(s) {
    if (s.project_version) {
        document.getElementById('topbarProjectVersion').textContent = s.project_version;
    }
    if (s.ollama_version) {
        document.getElementById('topbarOllamaVersion').textContent = s.ollama_version;
    }
}

function updateDashboard(s) {
    // Project version (topbar badge)
    document.getElementById('topbarProjectVersion').textContent = s.project_version || '--';
    // Ollama engine version (topbar)
    document.getElementById('topbarOllamaVersion').textContent = s.ollama_version || '--';

    // API Status
    const apiStatus = document.getElementById('apiStatus');
    apiStatus.className = `status-indicator ${s.api_reachable ? 'online' : 'offline'}`;
    apiStatus.querySelector('span:last-child').textContent = s.api_reachable ? 'API 在线' : 'API 离线';

    // Service Status
    const svcStatus = document.getElementById('serviceStatus');
    const health = s.container.health || s.container.status || 'unknown';
    const statusMap = {
        healthy:   { cls: 'healthy',   text: '运行中 (healthy)' },
        starting:  { cls: 'starting',  text: '启动中...' },
        unhealthy: { cls: 'unhealthy', text: '异常 (unhealthy)' },
        running:   { cls: 'healthy',   text: '运行中' },
        exited:    { cls: 'stopped',   text: '已停止' },
        not_found: { cls: 'stopped',   text: '未创建' },
        unknown:   { cls: 'stopped',   text: '未知' },
        created:   { cls: 'starting',  text: '已创建' },
        paused:    { cls: 'starting',  text: '已暂停' },
        restarting:{ cls: 'starting',  text: '重启中...' },
        removing:  { cls: 'stopped',   text: '移除中...' },
        dead:      { cls: 'stopped',   text: '已终止' },
    };
    const st = statusMap[health] || { cls: 'stopped', text: health || '未知' };
    svcStatus.className = `service-status ${st.cls}`;
    svcStatus.querySelector('span:last-child').textContent = st.text;

    // Stats
    document.getElementById('modelCount').textContent = s.models ? s.models.length : 0;
    document.getElementById('runningCount').textContent = s.running_models ? s.running_models.length : 0;
    document.getElementById('diskUsage').textContent = s.disk.use_percent || '--';

    if (s.gpu && s.gpu.length > 0) {
        const gpu = s.gpu[0];
        if (gpu.is_unified_mem && gpu.unified_mem_total) {
            document.getElementById('gpuUsage').textContent = `${gpu.unified_mem_total}`;
            document.querySelector('#gpuUsage').closest('.stat-card').querySelector('.stat-label').textContent = '统一内存';
        } else if (gpu.mem_used && !gpu.mem_used.includes('[N/A]')) {
            document.getElementById('gpuUsage').textContent = `${gpu.mem_used} / ${gpu.mem_total}`;
        } else {
            document.getElementById('gpuUsage').textContent = gpu.name || 'N/A';
        }
    }

    // Container Info
    const containerEl = document.getElementById('containerInfo');
    containerEl.innerHTML = buildInfoList({
        '状态': `<span style="color:${s.api_reachable ? 'var(--accent-green)' : 'var(--accent-red)'}">● ${st.text}</span>`,
        '镜像': s.container.image || '--',
        '运行时间': s.container.uptime || '--',
        '端口': s.container.ports || '--',
        '容器 ID': s.container.id || '--',
    });

    // Resources
    const resEl = document.getElementById('resourceInfo');
    resEl.innerHTML = buildInfoList({
        'CPU': s.resources.cpu_percent || '--',
        '内存': `${s.resources.mem_usage || '--'} (${s.resources.mem_percent || '--'})`,
        '网络 IO': s.resources.net_io || '--',
        '磁盘 IO': s.resources.block_io || '--',
        '模型数据': s.disk.model_data_size || '--',
        '可用空间': `${s.disk.avail_space || '--'} / ${s.disk.total_space || '--'}`,
    });

    // Running Models
    const tbody = document.querySelector('#runningModelsTable tbody');
    if (s.running_models && s.running_models.length > 0) {
        tbody.innerHTML = s.running_models.map(m => `
            <tr>
                <td><strong>${escapeHtml(m.name)}</strong></td>
                <td>${m.vram_human}</td>
                <td>${formatTime(m.expires_at)}</td>
            </tr>
        `).join('');
    } else {
        tbody.innerHTML = '<tr><td colspan="3" class="empty-state">无运行中的模型</td></tr>';
    }
}

function buildInfoList(items) {
    return Object.entries(items).map(([k, v]) =>
        `<div class="info-item"><span class="info-label">${k}</span><span class="info-value">${v}</span></div>`
    ).join('');
}

// ── Service Control ─────────────────────────────────────────────
async function controlService(action) {
    const actionNames = { start: '启动', stop: '停止', restart: '重启', update: '更新' };
    const name = actionNames[action] || action;

    if (action === 'update') {
        return startStreamUpdate();
    }

    if (action === 'stop' && !confirm('确定要停止 Ollama 服务吗？')) return;

    return startStreamServiceControl(action, name);
}

// ── Stream Service Control via WebSocket ────────────────────────
function startStreamServiceControl(action, actionName) {
    const progressEl = document.getElementById('serviceProgress');
    const progressFill = document.getElementById('serviceProgressFill');
    const progressText = document.getElementById('serviceProgressText');

    // Show progress area & disable buttons
    progressEl.style.display = 'block';
    progressFill.style.width = '0%';
    progressFill.style.background = ''; // reset to default
    progressText.textContent = `正在${actionName} Ollama 服务...`;
    document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = true);

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${location.host}/api/ws/service?action=${action}&key=${encodeURIComponent(getApiKey())}`);

    ws.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);

            if (data.error) {
                progressFill.style.width = '100%';
                progressFill.style.background = 'var(--accent-red, #ef4444)';
                progressText.textContent = `❌ ${actionName}失败: ${data.error}`;
                showToast(`${actionName}失败: ${data.error}`, 'error');
                finishServiceControl(ws);
                return;
            }

            switch (data.phase) {
                case 'operating':
                    progressFill.style.width = '40%';
                    progressText.textContent = '⚙️ ' + data.status;
                    break;

                case 'waiting':
                    progressFill.style.width = '75%';
                    progressText.textContent = '⏳ ' + data.status;
                    break;

                case 'done':
                    progressFill.style.width = '100%';
                    progressFill.style.background = 'var(--accent-green, #22c55e)';
                    progressText.textContent = `✅ ${data.message}`;
                    showToast(data.message, 'success');
                    setTimeout(refreshStatus, 2000);
                    finishServiceControl(ws);
                    return;
            }
        } catch (e) {
            progressText.textContent = event.data;
        }
    };

    ws.onerror = () => {
        showToast('WebSocket 连接失败，请重试', 'error');
        progressText.textContent = '❌ 连接失败';
        finishServiceControl(ws);
    };

    ws.onclose = () => {
        document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = false);
    };
}

function finishServiceControl(ws) {
    document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = false);
    // Auto-hide progress bar after 5 seconds
    setTimeout(() => {
        const el = document.getElementById('serviceProgress');
        if (el) el.style.display = 'none';
    }, 5000);
    if (ws && ws.readyState === WebSocket.OPEN) ws.close();
}

// ── Stream Update via WebSocket ─────────────────────────────────
// 记录待更新提示信息（取消更新时显示在顶栏）
let _pendingUpdateInfo = null;

function showUpdateHint(currentVersion, latestVersion) {
    _pendingUpdateInfo = { current: currentVersion, latest: latestVersion };
    const el = document.getElementById('topbarOllamaVersion');
    if (el) {
        el.textContent = currentVersion + ' → ' + latestVersion;
        el.title = `当前版本 ${currentVersion}，最新版本 ${latestVersion}，点击「更新版本」按钮升级`;
        el.style.color = 'var(--accent-yellow)';
    }
}

function clearUpdateHint() {
    _pendingUpdateInfo = null;
    const el = document.getElementById('topbarOllamaVersion');
    if (el) {
        el.style.color = '';
        el.title = 'Ollama 引擎版本';
    }
}

function startStreamUpdate() {
    const progressEl = document.getElementById('serviceProgress');
    const progressFill = document.getElementById('serviceProgressFill');
    const progressText = document.getElementById('serviceProgressText');

    // Show progress area & disable buttons
    progressEl.style.display = 'block';
    progressFill.style.width = '0%';
    progressFill.style.background = '';
    progressText.textContent = '正在获取版本信息...';
    document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = true);

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${location.host}/api/ws/update?key=${encodeURIComponent(getApiKey())}`);

    let pullLines = 0;

    ws.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);

            if (data.error) {
                progressFill.style.width = '0%';
                progressText.textContent = `❌ 错误: ${data.error}`;
                showToast(`更新失败: ${data.error}`, 'error');
                finishUpdate(ws);
                return;
            }

            switch (data.phase) {
                case 'checking':
                    progressFill.style.width = '5%';
                    progressText.textContent = '🔍 ' + data.status;
                    break;

                case 'up_to_date': {
                    progressFill.style.width = '100%';
                    progressFill.style.background = 'var(--accent-green, #22c55e)';
                    clearUpdateHint();
                    const cv = data.current_version || '--';
                    progressText.textContent = `✅ 当前已是最新版本 (${cv})`;
                    showToast(`当前已是最新版本 (${cv})`, 'success');
                    finishUpdate(ws);
                    return;
                }

                case 'update_available': {
                    progressFill.style.width = '10%';
                    progressFill.style.background = 'var(--accent-yellow)';
                    const cv = data.current_version || '--';
                    const lv = data.latest_version || '未知';
                    progressText.textContent = `🆕 发现新版本 ${lv}（当前: ${cv}），等待确认...`;
                    const doUpdate = confirm(`发现 Ollama 新版本！\n\n当前版本: ${cv}\n最新版本: ${lv}\n\n确定要立即更新并重启 Ollama 服务吗？`);
                    if (doUpdate) {
                        ws.send('confirm');
                        progressFill.style.background = '';
                        progressText.textContent = `⏳ 正在更新 ${cv} → ${lv}...`;
                    } else {
                        ws.send('cancel');
                        showUpdateHint(cv, lv);
                        progressFill.style.width = '0%';
                        progressText.textContent = `ℹ️ 已取消更新（当前: ${cv}，最新: ${lv}）`;
                        showToast(`已跳过更新 (${cv} → ${lv})`, 'info');
                        finishUpdate(ws);
                    }
                    break;
                }

                case 'cancelled':
                    finishUpdate(ws);
                    return;

                case 'pulling':
                    pullLines++;
                    const pullPercent = Math.min(10 + pullLines * 2, 85);
                    progressFill.style.width = pullPercent + '%';
                    progressFill.style.background = '';
                    const statusLine = data.status || '拉取中...';
                    progressText.textContent = '📦 ' + statusLine;
                    break;

                case 'waiting':
                    progressFill.style.width = '90%';
                    progressText.textContent = '⏳ ' + data.status;
                    break;

                case 'done':
                    progressFill.style.width = '100%';
                    progressFill.style.background = 'var(--accent-green, #22c55e)';
                    clearUpdateHint();
                    if (data.old_version && data.new_version && data.old_version !== data.new_version) {
                        progressText.textContent = `✅ 更新完成: ${data.old_version} → ${data.new_version}`;
                        showToast(`版本更新: ${data.old_version} → ${data.new_version}`, 'success');
                    } else {
                        progressText.textContent = `✅ ${data.message || '更新完成'}`;
                        showToast('更新完成', 'success');
                    }
                    setTimeout(refreshStatus, 2000);
                    finishUpdate(ws);
                    return;
            }
        } catch (e) {
            progressText.textContent = event.data;
        }
    };

    ws.onerror = () => {
        showToast('WebSocket 连接失败，请重试', 'error');
        progressText.textContent = '❌ 连接失败';
        finishUpdate(ws);
    };

    ws.onclose = () => {
        document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = false);
    };
}

function finishUpdate(ws) {
    document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = false);
    setTimeout(() => {
        const el = document.getElementById('serviceProgress');
        if (el) el.style.display = 'none';
    }, 8000);
    if (ws && ws.readyState === WebSocket.OPEN) ws.close();
}

// ── Models Page ─────────────────────────────────────────────────
// 模型列表状态
let _allModels = [];
let _modelSearch = '';
let _modelSort = { key: 'name', dir: 'asc' };

function filterAndSortModels(models) {
    let filtered = models;
    if (_modelSearch) {
        const q = _modelSearch.toLowerCase();
        filtered = models.filter(m => (m.name || '').toLowerCase().includes(q) || (m.family || '').toLowerCase().includes(q));
    }
    const { key, dir } = _modelSort;
    filtered.sort((a, b) => {
        let va, vb;
        if (key === 'name') { va = (a.name || '').toLowerCase(); vb = (b.name || '').toLowerCase(); }
        else if (key === 'size') { va = a.size || 0; vb = b.size || 0; }
        else if (key === 'modified_at') { va = new Date(a.modified_at || 0).getTime(); vb = new Date(b.modified_at || 0).getTime(); }
        else { va = a[key] || ''; vb = b[key] || ''; }
        if (va < vb) return dir === 'asc' ? -1 : 1;
        if (va > vb) return dir === 'asc' ? 1 : -1;
        return 0;
    });
    return filtered;
}

function sortIcon(key) {
    if (_modelSort.key !== key) return ' ⇅';
    return _modelSort.dir === 'asc' ? ' ↑' : ' ↓';
}

function toggleModelSort(key) {
    if (_modelSort.key === key) {
        _modelSort.dir = _modelSort.dir === 'asc' ? 'desc' : 'asc';
    } else {
        _modelSort.key = key;
        _modelSort.dir = 'asc';
    }
    renderModels();
}

function onModelSearchInput(e) {
    _modelSearch = e.target.value;
    renderModels();
}

function renderModelCaps(caps) {
    if (!caps || !caps.length) return '<span style="color:var(--text-muted)">--</span>';
    const emoji = { vision: '👁', tools: '🛠', thinking: '🧠', embedding: '📐', code: '💻', cloud: '☁️' };
    return caps.map(c => `<span class="model-cap-tag">${emoji[c.toLowerCase()] || '🏷'} ${escapeHtml(c)}</span>`).join(' ');
}

function renderModelType(type) {
    const map = { chat: '💬 对话', vision: '👁 视觉', embedding: '📐 嵌入', code: '💻 代码' };
    return map[type] || type || '--';
}

function renderModels() {
    const container = document.getElementById('modelsContainer');
    if (!_allModels || _allModels.length === 0) {
        container.innerHTML = '<div class="card"><div class="empty-state">暂无模型，点击"拉取模型"下载</div></div>';
        return;
    }

    const cloudModels = filterAndSortModels(_allModels.filter(m => m.name && m.name.includes(':cloud')));
    const localModels = filterAndSortModels(_allModels.filter(m => !m.name || !m.name.includes(':cloud')));
    const sortableHdr = (label, key) => `<th class="sortable-th" onclick="toggleModelSort('${key}')">${label}${sortIcon(key)}</th>`;

    let html = `<div class="model-search-bar"><input type="text" placeholder="搜索模型名称..." value="${escapeAttr(_modelSearch)}" oninput="onModelSearchInput(event)" /></div>`;

    if (cloudModels.length > 0) {
        html += `
            <div class="card">
                <div class="card-header"><h3>☁️ 云端模型 (${cloudModels.length})</h3></div>
                <div class="table-container">
                    <table>
                        <thead><tr>${sortableHdr('名称','name')}<th>类型</th><th>能力</th>${sortableHdr('大小','size')}${sortableHdr('修改时间','modified_at')}<th>操作</th></tr></thead>
                        <tbody>${cloudModels.map(m => `
                            <tr>
                                <td><strong>${escapeHtml(m.name)}</strong><br><span style="color:var(--text-muted);font-size:11px">${m.family || '云端推理'}</span></td>
                                <td>${renderModelType(m.model_type || 'chat')}</td>
                                <td>${renderModelCaps(m.capabilities || ['cloud'])}</td>
                                <td>${m.size_human}</td>
                                <td>${formatTime(m.modified_at)}</td>
                                <td>
                                    <button class="btn btn-sm" onclick="testModel('${escapeAttr(m.name)}')" title="测试">💬</button>
                                    <button class="btn btn-sm" onclick="showModelInfo('${escapeAttr(m.name)}')" title="详情">📋</button>
                                    <button class="btn btn-sm btn-danger" onclick="deleteModel('${escapeAttr(m.name)}')" title="删除">🗑</button>
                                </td>
                            </tr>
                        `).join('')}</tbody>
                    </table>
                </div>
            </div>`;
    }

    if (localModels.length > 0) {
        html += `
            <div class="card">
                <div class="card-header"><h3>💻 本地模型 (${localModels.length})</h3></div>
                <div class="table-container">
                    <table>
                        <thead><tr>${sortableHdr('名称','name')}<th>类型</th><th>能力</th>${sortableHdr('大小','size')}<th>参数</th><th>量化</th>${sortableHdr('修改时间','modified_at')}<th>操作</th></tr></thead>
                        <tbody>${localModels.map(m => `
                            <tr>
                                <td><strong>${escapeHtml(m.name)}</strong><br><span style="color:var(--text-muted);font-size:11px">${m.family || ''}</span></td>
                                <td>${renderModelType(m.model_type)}</td>
                                <td>${renderModelCaps(m.capabilities)}</td>
                                <td>${m.size_human}</td>
                                <td>${m.parameters || '--'}</td>
                                <td>${m.quantization || '--'}</td>
                                <td>${formatTime(m.modified_at)}</td>
                                <td>
                                    <button class="btn btn-sm" onclick="testModel('${escapeAttr(m.name)}')" title="测试">💬</button>
                                    <button class="btn btn-sm" onclick="showModelInfo('${escapeAttr(m.name)}')" title="详情">📋</button>
                                    <button class="btn btn-sm btn-danger" onclick="deleteModel('${escapeAttr(m.name)}')" title="删除">🗑</button>
                                </td>
                            </tr>
                        `).join('')}</tbody>
                    </table>
                </div>
            </div>`;
    }

    if (cloudModels.length === 0 && localModels.length === 0 && _modelSearch) {
        html += '<div class="card"><div class="empty-state">未找到匹配的模型</div></div>';
    }

    const totalSize = _allModels.reduce((sum, m) => sum + (m.size || 0), 0);
    const totalSizeHuman = totalSize >= 1073741824 ? `${(totalSize / 1073741824).toFixed(1)} GiB` : `${(totalSize / 1048576).toFixed(1)} MiB`;
    html += `<div style="text-align:right;color:var(--text-muted);font-size:12px;margin-top:4px">共 ${_allModels.length} 个模型，总计 ${totalSizeHuman}</div>`;

    container.innerHTML = html;
}

async function loadModels() {
    try {
        _allModels = await api('/api/models') || [];
        renderModels();
        // Async: check model compatibility
        checkModelCompatibility();
    } catch (err) {
        showToast('加载模型列表失败: ' + err.message, 'error');
    }
}

async function checkModelCompatibility() {
    try {
        const incompatible = await api('/api/models/check');
        if (!incompatible || incompatible.length === 0) return;

        // Store incompatible model names for inline display
        window._incompatibleModels = {};
        incompatible.forEach(m => { window._incompatibleModels[m.name] = m.error; });

        // Mark rows in the model table
        document.querySelectorAll('#modelsContainer table tbody tr').forEach(row => {
            const nameCell = row.querySelector('td:first-child strong');
            if (!nameCell) return;
            const name = nameCell.textContent.trim();
            if (window._incompatibleModels[name]) {
                row.style.background = 'rgba(239,68,68,0.06)';
                // Add warning badge after the name
                if (!row.querySelector('.compat-badge')) {
                    const badge = document.createElement('span');
                    badge.className = 'compat-badge';
                    badge.title = window._incompatibleModels[name];
                    badge.innerHTML = ' <span style="display:inline-block;background:var(--accent-red);color:#fff;font-size:10px;padding:1px 6px;border-radius:3px;cursor:help">⚠ 需重新下载</span>';
                    nameCell.parentElement.appendChild(badge);
                    // Add repull button in the actions cell
                    const actionsCell = row.querySelector('td:last-child');
                    if (actionsCell && !actionsCell.querySelector('.compat-repull')) {
                        const repullBtn = document.createElement('button');
                        repullBtn.className = 'btn btn-sm btn-warning compat-repull';
                        repullBtn.title = '重新拉取';
                        repullBtn.textContent = '🔄';
                        repullBtn.onclick = () => repullModel(name);
                        actionsCell.prepend(repullBtn);
                    }
                }
            }
        });
    } catch { /* ignore */ }
}

function repullModel(name) {
    document.getElementById('pullModelName').value = name;
    showPullDialog();
}

async function repullAllIncompatible() {
    const rows = document.querySelectorAll('#modelCompatAlert tbody tr');
    for (const row of rows) {
        const name = row.querySelector('strong').textContent;
        showToast(`正在重新拉取 ${name}...`, 'info');
        try {
            await api('/api/models/pull', { method: 'POST', body: JSON.stringify({ name }) });
        } catch { /* ignore, use WS pull for progress */ }
    }
    showToast('已提交全部重新拉取请求', 'success');
}

async function showModelInfo(name) {
    try {
        const info = await api(`/api/models/${encodeURIComponent(name)}/info`);
        const details = info.details || {};
        const modelInfo = info.model_info || {};
        const params = info.parameters || '';
        const template = info.template || '';
        const system = info.system || '';
        const license = info.license || '';

        // Infer capabilities from details
        const families = (details.families || []).join(', ') || '--';
        const paramSize = details.parameter_size || '--';
        const quantLevel = details.quantization_level || '--';
        const format = details.format || '--';

        // Context length from model_info
        let ctxLen = '--';
        for (const [k, v] of Object.entries(modelInfo)) {
            if (k.toLowerCase().includes('context_length')) { ctxLen = String(v); break; }
        }

        let html = `<div class="modal-overlay" id="modelInfoModal" onclick="if(event.target===this)this.remove()">
            <div class="modal" style="width:640px;max-height:85vh;display:flex;flex-direction:column">
                <div class="modal-header">
                    <h3>📋 ${escapeHtml(name)}</h3>
                    <button class="btn-close" onclick="document.getElementById('modelInfoModal').remove()">✕</button>
                </div>
                <div class="modal-body" style="overflow-y:auto;flex:1">
                    <div class="model-info-grid">
                        <div class="model-info-row"><span class="model-info-label">参数规模</span><span>${escapeHtml(paramSize)}</span></div>
                        <div class="model-info-row"><span class="model-info-label">量化级别</span><span>${escapeHtml(quantLevel)}</span></div>
                        <div class="model-info-row"><span class="model-info-label">格式</span><span>${escapeHtml(format)}</span></div>
                        <div class="model-info-row"><span class="model-info-label">模型族</span><span>${escapeHtml(families)}</span></div>
                        <div class="model-info-row"><span class="model-info-label">上下文长度</span><span>${escapeHtml(ctxLen)}</span></div>
                    </div>
                    ${params ? `<details class="model-info-section"><summary>📝 模型参数 (Modelfile)</summary><pre class="model-info-pre">${escapeHtml(params)}</pre></details>` : ''}
                    ${system ? `<details class="model-info-section"><summary>🤖 System Prompt</summary><pre class="model-info-pre">${escapeHtml(system)}</pre></details>` : ''}
                    ${template ? `<details class="model-info-section"><summary>📄 模板</summary><pre class="model-info-pre">${escapeHtml(template)}</pre></details>` : ''}
                    ${license ? `<details class="model-info-section"><summary>📜 许可证</summary><pre class="model-info-pre">${escapeHtml(license.slice(0, 2000))}</pre></details>` : ''}
                </div>
            </div>
        </div>`;
        document.body.insertAdjacentHTML('beforeend', html);
    } catch (err) {
        showToast('获取模型信息失败: ' + err.message, 'error');
    }
}

async function deleteModel(name) {
    if (!confirm(`确定要删除模型 "${name}" 吗？此操作不可撤销。`)) return;

    try {
        await api(`/api/models/${encodeURIComponent(name)}`, { method: 'DELETE' });
        showToast(`模型 ${name} 已删除`, 'success');
        loadModels();
    } catch (err) {
        showToast('删除失败: ' + err.message, 'error');
    }
}

// testModel navigates to chat page and pre-selects the given model.
function testModel(name) {
    switchPage('chat');
    // Wait a tick for initChat to populate the select options
    setTimeout(() => {
        const sel = document.getElementById('chatModelSelect');
        sel.value = name;
        if (sel.value !== name) {
            // Model might not be in options yet; add it
            const opt = document.createElement('option');
            opt.value = name;
            opt.textContent = name;
            sel.appendChild(opt);
            sel.value = name;
        }
        // Trigger model change to load presets
        if (typeof onChatModelChange === 'function') onChatModelChange();
    }, 100);
}

// ── Model Tab Switching ─────────────────────────────────────────
function switchModelTab(tab) {
    // Update tab buttons
    document.querySelectorAll('#modelsTabBar .tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    // Show/hide tab panels
    document.querySelectorAll('.model-tab').forEach(panel => {
        panel.classList.toggle('active', panel.id === `tab-${tab}`);
    });
    // Auto-load market on first switch
    if (tab === 'market') {
        const results = document.getElementById('marketResults');
        if (results && results.querySelector('.empty-state')) {
            searchMarketModels();
        }
    }
}

// ── Model Market Search ─────────────────────────────────────────
let marketSearching = false;

async function searchMarketModels() {
    if (marketSearching) return;
    marketSearching = true;

    const resultsEl = document.getElementById('marketResults');
    const query = document.getElementById('marketSearchInput').value.trim();
    const category = document.getElementById('marketCategory').value;
    const sort = document.getElementById('marketSort').value;

    resultsEl.innerHTML = '<div class="market-loading"><span class="spinner"></span>正在从 Ollama 官网获取全部模型（遍历所有分页中）...</div>';

    try {
        const params = new URLSearchParams();
        if (query) params.set('q', query);
        if (category) params.set('c', category);
        if (sort) params.set('sort', sort);

        const data = await api(`/api/models/search?${params.toString()}`);

        if (!data.models || data.models.length === 0) {
            resultsEl.innerHTML = '<div class="empty-state">未找到匹配的模型，请尝试其他关键词</div>';
            return;
        }

        // Phase 1: Render results immediately with English descriptions
        resultsEl.innerHTML = renderMarketResults(data);

        // Phase 2: Asynchronously translate descriptions in batches
        translateMarketDescriptions(data.models);
    } catch (err) {
        resultsEl.innerHTML = `<div class="empty-state" style="color:var(--accent-red)">搜索失败: ${escapeHtml(err.message)}<br><small>请检查服务器网络是否可访问 ollama.com</small></div>`;
    } finally {
        marketSearching = false;
    }
}

// translateMarketDescriptions translates model descriptions in batches (100 per request).
// The backend handles caching (cached items return instantly) and batch LLM translation.
// If there are more than 100 items, we loop through batches so ALL models get translated.
async function translateMarketDescriptions(models) {
    // Filter models that need translation (non-empty, non-Chinese descriptions)
    const toTranslate = models.filter(m => m.description && m.description.length >= 10 && !containsChineseChar(m.description));
    if (toTranslate.length === 0) return;

    // Show translation progress indicator
    const progressEl = document.createElement('div');
    progressEl.id = 'translateProgress';
    progressEl.style.cssText = 'color:var(--text-muted);font-size:12px;margin-bottom:8px;display:flex;align-items:center;gap:6px;';
    const marketGrid = document.querySelector('.market-grid');
    if (marketGrid && marketGrid.parentNode) {
        marketGrid.parentNode.insertBefore(progressEl, marketGrid);
    }

    const BATCH_SIZE = 100;
    const totalBatches = Math.ceil(toTranslate.length / BATCH_SIZE);
    let translatedCount = 0;
    let failedBatches = 0;

    try {
        for (let batchIdx = 0; batchIdx < totalBatches; batchIdx++) {
            const start = batchIdx * BATCH_SIZE;
            const end = Math.min(start + BATCH_SIZE, toTranslate.length);
            const batch = toTranslate.slice(start, end);

            // Update progress
            if (progressEl) {
                if (totalBatches > 1) {
                    progressEl.innerHTML = `<span class="spinner" style="width:14px;height:14px;border-width:2px;"></span><span>正在翻译模型描述（第 ${batchIdx + 1}/${totalBatches} 批，共 ${toTranslate.length} 条）...</span>`;
                } else {
                    progressEl.innerHTML = `<span class="spinner" style="width:14px;height:14px;border-width:2px;"></span><span>正在翻译 ${toTranslate.length} 条模型描述（批量翻译中）...</span>`;
                }
            }

            try {
                const items = batch.map(m => ({ name: m.name, description: m.description }));
                const results = await api('/api/models/search/translate', {
                    method: 'POST',
                    body: JSON.stringify({ items }),
                });

                if (!results || !Array.isArray(results)) {
                    failedBatches++;
                    continue;
                }

                // Update the DOM for each translated description
                for (const r of results) {
                    if (!r.description) continue;
                    const original = batch.find(m => m.name === r.name);
                    if (original && r.description === original.description) continue;

                    const descEl = document.querySelector(`.market-card[data-model="${CSS.escape(r.name)}"] .market-card-desc`);
                    if (descEl) {
                        descEl.textContent = r.description;
                        descEl.classList.add('translated');
                        translatedCount++;
                    }
                }
            } catch (batchErr) {
                console.warn(`Translation batch ${batchIdx + 1} failed:`, batchErr);
                failedBatches++;
                // Continue with next batch instead of aborting entirely
            }
        }

        // Show completion summary
        if (progressEl) {
            if (translatedCount > 0) {
                const summary = failedBatches > 0
                    ? `✅ 已翻译 ${translatedCount}/${toTranslate.length} 条模型描述（${failedBatches} 批失败）`
                    : `✅ 已翻译 ${translatedCount}/${toTranslate.length} 条模型描述`;
                progressEl.innerHTML = `<span>${summary}</span>`;
                setTimeout(() => progressEl.remove(), 5000);
            } else {
                progressEl.innerHTML = `<span>⚠️ 翻译未成功，请确保有可用的本地大模型</span>`;
                setTimeout(() => progressEl.remove(), 8000);
            }
        }
    } catch (err) {
        // Translation failure is non-critical — just keep English descriptions
        console.warn('Translation failed:', err);
        if (progressEl) {
            progressEl.innerHTML = `<span>⚠️ 翻译失败: ${escapeHtml(err.message || '请求超时')}</span>`;
            setTimeout(() => progressEl.remove(), 8000);
        }
    }
}

// containsChineseChar checks if a string contains Chinese characters.
function containsChineseChar(str) {
    return /[\u4E00-\u9FFF]/.test(str);
}

function renderMarketResults(data) {
    const tagEmoji = {
        'vision': '👁', 'tools': '🛠', 'thinking': '🧠',
        'embedding': '📐', 'code': '💻', 'cloud': '☁️',
    };

    let html = `<div style="color:var(--text-muted);font-size:12px;margin-bottom:10px">共找到 ${data.total} 个模型${data.query ? '（关键词: ' + escapeHtml(data.query) + '）' : ''}</div>`;
    html += '<div class="market-grid">';

    for (const m of data.models) {
        // Tags
        const tagsHtml = (m.tags || []).map(t =>
            `<span class="market-tag">${tagEmoji[t.toLowerCase()] || '🏷'} ${escapeHtml(t)}</span>`
        ).join('');

        // Sizes
        const sizesHtml = (m.sizes || []).map(s =>
            `<span class="market-size">${escapeHtml(s)}</span>`
        ).join('');

        html += `
            <div class="market-card" data-model="${escapeAttr(m.name)}">
                <div class="market-card-header">
                    <span class="market-card-name">${escapeHtml(m.name)}</span>
                    ${m.pulls ? `<span class="market-card-pulls">⬇ ${escapeHtml(m.pulls)}</span>` : ''}
                </div>
                ${m.description ? `<div class="market-card-desc">${escapeHtml(m.description)}</div>` : ''}
                <div class="market-card-meta">
                    ${tagsHtml}${sizesHtml}
                </div>
                <div class="market-card-footer">
                    <span class="market-card-updated">${m.updated ? '🕐 ' + escapeHtml(m.updated) : ''}</span>
                    <div class="market-card-actions">
                        <button class="btn btn-sm btn-primary" onclick="pullFromMarket('${escapeAttr(m.name)}')" title="拉取模型">📥 拉取</button>
                        <a class="btn btn-sm" href="https://ollama.com/library/${encodeURIComponent(m.name)}" target="_blank" title="查看详情">🔗</a>
                    </div>
                </div>
            </div>`;
    }

    html += '</div>';
    return html;
}

function pullFromMarket(name) {
    document.getElementById('pullModelName').value = name;
    showPullDialog();
}

// ── Pull Model ──────────────────────────────────────────────────
function showPullDialog() {
    document.getElementById('pullDialog').style.display = 'flex';
    document.getElementById('pullModelName').focus();
    document.getElementById('pullProgress').style.display = 'none';
    const pullBtn = document.getElementById('pullBtn');
    pullBtn.disabled = false;
    pullBtn.textContent = '开始拉取';
    pullBtn.classList.remove('btn-success');
    pullBtn.classList.add('btn-primary');
    pullBtn.onclick = startPull;
}

function closePullDialog() {
    document.getElementById('pullDialog').style.display = 'none';
}

function setPullModel(name) {
    document.getElementById('pullModelName').value = name;
}

async function startPull() {
    const name = document.getElementById('pullModelName').value.trim();
    if (!name) { showToast('请输入模型名称', 'error'); return; }

    document.getElementById('pullProgress').style.display = 'block';
    document.getElementById('pullBtn').disabled = true;

    const progressFill = document.getElementById('pullProgressFill');
    const progressText = document.getElementById('pullProgressText');

    try {
        // Use WebSocket for streaming
        const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const ws = new WebSocket(`${wsProtocol}//${location.host}/api/ws/pull?key=${encodeURIComponent(getApiKey())}`);

        ws.onopen = () => {
            ws.send(JSON.stringify({ name }));
        };

        ws.onmessage = (event) => {
            try {
                const data = JSON.parse(event.data);

                if (data.error) {
                    progressText.textContent = `错误: ${data.error}`;
                    progressFill.style.width = '0%';
                    document.getElementById('pullBtn').disabled = false;
                    return;
                }

                if (data.status === 'success') {
                    progressFill.style.width = '100%';
                    progressText.textContent = '✅ ' + (data.message || '完成');
                    showToast(`模型 ${name} 下载完成`, 'success');
                    const pullBtn = document.getElementById('pullBtn');
                    pullBtn.disabled = false;
                    pullBtn.textContent = '✅ 完成';
                    pullBtn.classList.remove('btn-primary');
                    pullBtn.classList.add('btn-success');
                    pullBtn.onclick = closePullDialog;
                    loadModels();
                    return;
                }

                // Update progress
                if (data.percent !== undefined) {
                    progressFill.style.width = `${Math.min(data.percent, 100)}%`;
                    progressText.textContent = `${data.status || '下载中'} ${data.percent.toFixed(1)}%`;
                } else {
                    progressText.textContent = data.status || '处理中...';
                }
            } catch (e) {
                progressText.textContent = event.data;
            }
        };

        ws.onerror = () => {
            showToast('WebSocket 连接失败，使用 HTTP 模式', 'error');
            document.getElementById('pullBtn').disabled = false;
        };

        ws.onclose = () => {
            document.getElementById('pullBtn').disabled = false;
        };
    } catch (err) {
        showToast('拉取失败: ' + err.message, 'error');
        document.getElementById('pullBtn').disabled = false;
    }
}

// ── Health Check ────────────────────────────────────────────────
async function runHealthCheck() {
    const checksEl = document.getElementById('healthChecks');
    const scoreEl = document.getElementById('healthScore');
    checksEl.innerHTML = '<div class="empty-state">检查中...</div>';

    try {
        const report = await api('/api/health');

        // Score
        const pct = report.total > 0 ? Math.round((report.passed / report.total) * 100) : 0;
        const scoreColor = pct >= 80 ? 'var(--accent-green)' : pct >= 50 ? 'var(--accent-yellow)' : 'var(--accent-red)';
        scoreEl.innerHTML = `
            <span class="score-value" style="color:${scoreColor}">${report.score}</span>
            <span class="score-label">通过 ${report.passed} / ${report.total} 项检查</span>
        `;

        // Checks
        const iconMap = { pass: '✅', fail: '❌', warn: '⚠️', skip: '⏭️' };
        checksEl.innerHTML = report.checks.map(c => `
            <div class="health-check-item">
                <span class="check-icon">${iconMap[c.status] || '❓'}</span>
                <span class="check-name">${escapeHtml(c.name)}</span>
                <span class="check-msg">${escapeHtml(c.message)}</span>
                ${c.detail ? `<span class="check-detail">${escapeHtml(c.detail)}</span>` : ''}
            </div>
        `).join('');
    } catch (err) {
        checksEl.innerHTML = `<div class="empty-state" style="color:var(--accent-red)">检查失败: ${escapeHtml(err.message)}</div>`;
    }
}

// ── Logs ────────────────────────────────────────────────────────
async function loadLogs() {
    try {
        const entries = await api('/api/logs?lines=300');
        const logEl = document.getElementById('logOutput');
        logEl.textContent = entries.map(e => e.raw).join('\n');
        logEl.scrollTop = logEl.scrollHeight;
    } catch (err) {
        document.getElementById('logOutput').textContent = '加载日志失败: ' + err.message;
    }
}

function toggleLogStream() {
    const btn = document.getElementById('logStreamBtn');
    if (logStreaming) {
        stopLogStream();
        btn.textContent = '▶ 实时日志';
    } else {
        startLogStream();
        btn.textContent = '⏹ 停止';
    }
}

function startLogStream() {
    const logEl = document.getElementById('logOutput');
    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    logWs = new WebSocket(`${wsProtocol}//${location.host}/api/ws/logs?key=${encodeURIComponent(getApiKey())}`);

    logWs.onopen = () => {
        logStreaming = true;
        logEl.textContent = '--- 实时日志流 ---\n';
    };

    logWs.onmessage = (event) => {
        logEl.textContent += event.data + '\n';
        // Auto-scroll to bottom
        logEl.scrollTop = logEl.scrollHeight;
        // Limit lines in DOM
        const lines = logEl.textContent.split('\n');
        if (lines.length > 2000) {
            logEl.textContent = lines.slice(-1500).join('\n');
        }
    };

    logWs.onclose = () => {
        logStreaming = false;
        document.getElementById('logStreamBtn').textContent = '▶ 实时日志';
    };

    logWs.onerror = () => {
        showToast('日志 WebSocket 连接失败', 'error');
    };
}

function stopLogStream() {
    if (logWs) {
        logWs.close();
        logWs = null;
    }
    logStreaming = false;
}

// ── Config ──────────────────────────────────────────────────────
async function loadConfig() {
    try {
        const vars = await api('/api/config');
        const form = document.getElementById('configForm');

        if (!vars || vars.length === 0) {
            form.innerHTML = '<div class="empty-state">.env 文件为空或不存在</div>';
            return;
        }

        form.innerHTML = vars.map(v => `
            <div class="config-item">
                <div>
                    <div class="config-key">${escapeHtml(v.key)}</div>
                    <div class="config-desc">${escapeHtml(v.description || '')}</div>
                </div>
                <input type="text" data-key="${escapeAttr(v.key)}" value="${escapeAttr(v.value)}" placeholder="${escapeAttr(v.default || '')}">
                <span style="font-size:11px;color:var(--text-muted)">${v.default ? `默认: ${v.default}` : ''}</span>
            </div>
        `).join('');
    } catch (err) {
        showToast('加载配置失败: ' + err.message, 'error');
    }
}

async function saveConfig() {
    const inputs = document.querySelectorAll('#configForm input[data-key]');
    const variables = [];
    inputs.forEach(input => {
        variables.push({ key: input.dataset.key, value: input.value });
    });

    try {
        await api('/api/config', {
            method: 'PUT',
            body: JSON.stringify({ variables }),
        });
        showToast('配置已保存，需要重启服务生效', 'success');
    } catch (err) {
        showToast('保存失败: ' + err.message, 'error');
    }
}

async function runOptimize() {
    if (!confirm('将根据硬件自动优化配置并应用，是否继续？')) return;
    showToast('正在执行自动优化...', 'info');

    try {
        await api('/api/optimize', { method: 'POST' });
        showToast('优化完成，请查看配置页面', 'success');
        loadConfig();
    } catch (err) {
        showToast('优化失败: ' + err.message, 'error');
    }
}

// ── Clean ───────────────────────────────────────────────────────
async function runClean(mode) {
    const modeNames = { soft: '轻度清理', hard: '深度清理' };
    if (!confirm(`确定执行${modeNames[mode]}吗？`)) return;

    showToast(`正在执行${modeNames[mode]}...`, 'info');
    try {
        await api('/api/clean', {
            method: 'POST',
            body: JSON.stringify({ mode }),
        });
        showToast(`${modeNames[mode]}完成`, 'success');
        setTimeout(refreshStatus, 2000);
    } catch (err) {
        showToast(`清理失败: ${err.message}`, 'error');
    }
}

// ── GPU ─────────────────────────────────────────────────────────

// renderGpuCards renders the GPU cards from a data array (used by WebSocket push).
function renderGpuCards(gpus) {
    const container = document.getElementById('gpuCards');
    if (!container) return;

    if (!gpus || gpus.length === 0) {
        container.innerHTML = '<div class="card"><div class="empty-state">未检测到 GPU</div></div>';
        return;
    }

    container.innerHTML = gpus.map((gpu) => {
        const isUnified = gpu.is_unified_mem;
        const memTotal = parseFloat(gpu.mem_total) || 1;
        const memUsed = parseFloat(gpu.mem_used) || 0;
        const memPct = isUnified ? 0 : ((memUsed / memTotal) * 100).toFixed(1);
        const utilPct = parseFloat(gpu.utilization) || 0;

        // Memory display section
        let memSection;
        if (isUnified) {
            memSection = `
                <div class="gpu-meter">
                    <div class="gpu-meter-label">
                        <span>🔗 统一内存 (CPU/GPU 共享)</span>
                        <span>${gpu.unified_mem_total || gpu.mem_total}</span>
                    </div>
                    ${gpu.mem_used && !gpu.mem_used.includes('N/A') ? `
                    <div class="gpu-meter-label" style="margin-top:4px">
                        <span>已使用</span>
                        <span>${gpu.mem_used}</span>
                    </div>` : ''}
                </div>`;
        } else {
            memSection = `
                <div class="gpu-meter">
                    <div class="gpu-meter-label">
                        <span>显存使用</span>
                        <span>${gpu.mem_used} / ${gpu.mem_total} (${memPct}%)</span>
                    </div>
                    <div class="meter-bar">
                        <div class="meter-fill ${memPct > 90 ? 'warn' : ''}" style="width:${memPct}%"></div>
                    </div>
                </div>`;
        }

        // GPU 进程列表
        let processesSection = '';
        if (gpu.processes && gpu.processes.length > 0) {
            processesSection = `
                <div style="margin-top:12px">
                    <div style="font-size:12px;color:var(--text-secondary);margin-bottom:8px">活跃进程 (${gpu.processes.length})</div>
                    <div style="font-size:11px;max-height:200px;overflow-y:auto">
                        ${gpu.processes.map(p => `
                            <div style="display:flex;justify-content:space-between;padding:4px 0;border-bottom:1px solid var(--border-color)">
                                <span style="color:var(--text-secondary)">[${p.pid}] ${escapeHtml(p.name)}</span>
                                <span style="color:var(--accent-blue)">${p.mem_usage}</span>
                            </div>
                        `).join('')}
                    </div>
                </div>`;
        }

        return `
            <div class="gpu-card">
                <h3>🎮 GPU ${gpu.index}: ${escapeHtml(gpu.name)}${isUnified ? ' <span style="color:var(--accent-purple);font-size:12px">统一内存架构</span>' : ''}</h3>
                ${memSection}
                <div class="gpu-meter">
                    <div class="gpu-meter-label">
                        <span>GPU 利用率</span>
                        <span>${gpu.utilization}</span>
                    </div>
                    <div class="meter-bar">
                        <div class="meter-fill ${utilPct > 90 ? 'warn' : ''}" style="width:${utilPct}%"></div>
                    </div>
                </div>
                <div class="info-list">
                    ${buildInfoList({
                        '温度': gpu.temperature,
                        '功耗': `${gpu.power} / ${gpu.power_limit}`,
                        '风扇': gpu.fan_speed || 'N/A',
                        '性能状态': gpu.perf_state || 'N/A',
                        '持久化模式': gpu.persistence_mode || 'N/A',
                        '计算模式': gpu.compute_mode || 'N/A',
                        '总线 ID': gpu.bus_id || 'N/A',
                        '显示活跃': gpu.disp_active || 'N/A',
                        'ECC 错误': gpu.volatile_ecc || '0',
                        'MIG 模式': gpu.mig_mode || 'N/A',
                        '驱动': gpu.driver,
                        'CUDA': gpu.cuda || '--',
                        ...(isUnified ? {'内存架构': '统一内存 (CPU/GPU 共享)'} : {'空闲显存': gpu.mem_free}),
                    })}
                </div>
                ${processesSection}
            </div>
        `;
    }).join('');
}

// loadGPUInfo is the HTTP fallback for manual refresh or initial load.
async function loadGPUInfo() {
    try {
        const gpus = await api('/api/gpu');
        if (currentStatus) {
            currentStatus.gpu = gpus;
        }
        renderGpuCards(gpus);
    } catch (err) {
        document.getElementById('gpuCards').innerHTML = `<div class="card"><div class="empty-state">加载 GPU 信息失败: ${escapeHtml(err.message)}</div></div>`;
    }
}

// ── Utilities ───────────────────────────────────────────────────
function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function escapeAttr(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function formatTime(isoStr) {
    if (!isoStr) return '--';
    try {
        const d = new Date(isoStr);
        if (isNaN(d.getTime())) return isoStr;
        const now = new Date();
        const diff = now - d;

        if (diff < 0) {
            // Future time (e.g. expires_at)
            const mins = Math.round(-diff / 60000);
            if (mins < 60) return `${mins} 分钟后`;
            const hrs = Math.round(mins / 60);
            return `${hrs} 小时后`;
        }

        if (diff < 60000) return '刚刚';
        if (diff < 3600000) return `${Math.round(diff / 60000)} 分钟前`;
        if (diff < 86400000) return `${Math.round(diff / 3600000)} 小时前`;
        return d.toLocaleDateString('zh-CN');
    } catch (e) {
        return isoStr;
    }
}

// ── Status WebSocket (replaces polling) ─────────────────────────
// A single WebSocket connection replaces all setInterval-based HTTP polling.
// The server pushes status data every 5s; the client controls full/lite mode.

const STATUS_WS_RECONNECT_MIN = 1000;   // 1s  initial delay
const STATUS_WS_RECONNECT_MAX = 30000;  // 30s max delay

// updateWsStatusIndicator updates the topbar WebSocket connection status.
function updateWsStatusIndicator(state) {
    const el = document.getElementById('topbarWsStatus');
    if (!el) return;
    const textEl = el.querySelector('.topbar-ws-text');
    el.className = 'topbar-ws-status ' + state;
    switch (state) {
        case 'connected':
            textEl.textContent = '已连接';
            break;
        case 'disconnected':
            textEl.textContent = '未连接';
            break;
        case 'connecting':
            textEl.textContent = '连接中...';
            break;
    }
}

function connectStatusWs() {
    if (statusWs && (statusWs.readyState === WebSocket.OPEN || statusWs.readyState === WebSocket.CONNECTING)) {
        return; // already connected or connecting
    }

    updateWsStatusIndicator('connecting');

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${wsProtocol}//${location.host}/api/ws/status?key=${encodeURIComponent(getApiKey())}`;

    statusWs = new WebSocket(url);

    statusWs.onopen = () => {
        statusWsReconnectDelay = STATUS_WS_RECONNECT_MIN; // reset backoff
        updateWsStatusIndicator('connected');
        // Tell server which mode we need
        sendStatusWsCommand({
            type: 'subscribe',
            mode: currentPage === 'dashboard' ? 'full' : 'lite',
        });
        // If tab is hidden, immediately pause
        if (!pageVisible) {
            sendStatusWsCommand({ type: 'pause' });
        }
    };

    statusWs.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            if (msg.type === 'status') {
                handleStatusWSMessage(msg);
            }
        } catch (e) {
            console.warn('Status WS parse error:', e);
        }
    };

    statusWs.onclose = () => {
        statusWs = null;
        updateWsStatusIndicator('disconnected');
        scheduleStatusWsReconnect();
    };

    statusWs.onerror = () => {
        // onclose will fire after onerror; reconnect is handled there.
    };
}

function disconnectStatusWs() {
    if (statusWsReconnectTimer) {
        clearTimeout(statusWsReconnectTimer);
        statusWsReconnectTimer = null;
    }
    if (statusWs) {
        statusWs.close();
        statusWs = null;
    }
    updateWsStatusIndicator('disconnected');
}

function scheduleStatusWsReconnect() {
    if (statusWsReconnectTimer) return;
    statusWsReconnectTimer = setTimeout(() => {
        statusWsReconnectTimer = null;
        // Only reconnect if user is still logged in (api key exists)
        if (getApiKey()) {
            connectStatusWs();
        }
    }, statusWsReconnectDelay);
    // Exponential backoff
    statusWsReconnectDelay = Math.min(statusWsReconnectDelay * 2, STATUS_WS_RECONNECT_MAX);
}

// sendStatusWsCommand sends a JSON command to the status WebSocket.
function sendStatusWsCommand(cmd) {
    if (statusWs && statusWs.readyState === WebSocket.OPEN) {
        statusWs.send(JSON.stringify(cmd));
    }
}

function startAutoRefresh() {
    refreshStatus();         // immediate HTTP fetch for first paint
    connectStatusWs();       // then switch to WebSocket for subsequent updates
}

// ── Chat Module ─────────────────────────────────────────────────
let chatWs = null;
let chatMessages = [];          // conversation history [{role, content, files}]
let chatUploadedFiles = [];     // [{id, name, size, preview}]
let chatStreaming = false;
let chatCurrentResponse = '';   // accumulates streaming tokens
let chatThinkingContent = '';   // accumulates thinking/reasoning tokens
let chatInitialized = false;
let chatSettingsOpen = false;
let chatHistoryOpen = false;
let currentSessionId = null;    // current active session ID

// ── Chat History (multi-session) ────────────────────────────────

function toggleChatHistory() {
    chatHistoryOpen = !chatHistoryOpen;
    const panel = document.getElementById('chatHistoryPanel');
    panel.classList.toggle('open', chatHistoryOpen);
    if (chatHistoryOpen) loadChatHistory();
}

async function loadChatHistory() {
    try {
        const sessions = await api('/api/chat/sessions');
        const list = document.getElementById('chatHistoryList');
        if (!sessions || sessions.length === 0) {
            list.innerHTML = '<div class="chat-history-empty">暂无对话记录</div>';
            return;
        }
        list.innerHTML = sessions.map(s => `
            <div class="chat-history-item ${s.id === currentSessionId ? 'active' : ''}" onclick="loadChatSession('${s.id}')">
                <div class="chat-history-item-title">${escapeHtml(s.title)}</div>
                <div class="chat-history-item-meta">${escapeHtml(s.model || '未知模型')} · ${formatTime(s.updated_at)}</div>
                <div class="chat-history-item-actions">
                    <button class="btn-icon" onclick="event.stopPropagation();renameChatSession('${s.id}','${escapeAttr(s.title)}')" title="重命名">✏️</button>
                    <button class="btn-icon" onclick="event.stopPropagation();exportChatSession('${s.id}','md')" title="导出 Markdown">📥</button>
                    <button class="btn-icon" onclick="event.stopPropagation();deleteChatSession('${s.id}')" title="删除">🗑</button>
                </div>
            </div>
        `).join('');
    } catch { /* ignore */ }
}

async function newChatSession() {
    // Save current session if has messages
    if (currentSessionId && chatMessages.length > 0) {
        await autoSaveSession();
    }
    currentSessionId = null;
    chatMessages = [];
    chatUploadedFiles = [];
    chatCurrentResponse = '';
    chatStreaming = false;
    document.getElementById('chatMessages').innerHTML = `
        <div class="chat-empty-state">
            <div class="chat-empty-icon">💬</div>
            <div class="chat-empty-text">选择模型，开始对话</div>
            <div class="chat-empty-hint">支持 Markdown、代码高亮、表格渲染</div>
        </div>`;
    renderChatUploadTags();
    document.getElementById('chatSendBtn').style.display = '';
    document.getElementById('chatStopBtn').style.display = 'none';
    if (chatWs) { chatWs.close(); chatWs = null; }
}

async function autoSaveSession() {
    if (!currentSessionId) {
        // Create a new session
        const model = document.getElementById('chatModelSelect').value || '';
        const title = generateSessionTitle();
        try {
            const resp = await api('/api/chat/sessions', {
                method: 'POST', body: JSON.stringify({ title, model }),
            });
            if (resp && resp.id) currentSessionId = resp.id;
        } catch { return; }
    }
    // Save all messages
    for (const msg of chatMessages) {
        if (!msg._saved) {
            try {
                await api(`/api/chat/sessions/${currentSessionId}/messages`, {
                    method: 'POST',
                    body: JSON.stringify({ role: msg.role, content: msg.content, files: msg.files || [] }),
                });
                msg._saved = true;
            } catch { /* ignore */ }
        }
    }
}

function generateSessionTitle() {
    const first = chatMessages.find(m => m.role === 'user');
    if (first && first.content) {
        return first.content.slice(0, 30) + (first.content.length > 30 ? '...' : '');
    }
    return '新对话';
}

async function loadChatSession(sessionId) {
    // Save current first
    if (currentSessionId && chatMessages.length > 0) {
        await autoSaveSession();
    }
    try {
        const msgs = await api(`/api/chat/sessions/${sessionId}`);
        currentSessionId = sessionId;
        chatMessages = (msgs || []).map(m => ({ role: m.role, content: m.content, files: m.files || [], _saved: true }));

        // Rebuild UI
        const container = document.getElementById('chatMessages');
        container.innerHTML = '';
        for (const msg of chatMessages) {
            appendChatBubble(msg.role, msg.content, []);
        }
        if (chatMessages.length === 0) {
            container.innerHTML = `<div class="chat-empty-state"><div class="chat-empty-icon">💬</div><div class="chat-empty-text">空对话</div></div>`;
        }
        toggleChatHistory(); // close history panel
    } catch (err) {
        showToast('加载对话失败: ' + err.message, 'error');
    }
}

async function deleteChatSession(id) {
    if (!confirm('确定删除该对话记录？')) return;
    try {
        await api(`/api/chat/sessions/${id}`, { method: 'DELETE' });
        if (currentSessionId === id) {
            currentSessionId = null;
            chatMessages = [];
            document.getElementById('chatMessages').innerHTML = `<div class="chat-empty-state"><div class="chat-empty-icon">💬</div><div class="chat-empty-text">选择模型，开始对话</div></div>`;
        }
        loadChatHistory();
        showToast('对话已删除', 'success');
    } catch (err) {
        showToast('删除失败: ' + err.message, 'error');
    }
}

async function renameChatSession(id, currentTitle) {
    const title = prompt('输入新标题', currentTitle);
    if (!title || title === currentTitle) return;
    try {
        await api(`/api/chat/sessions/${id}`, { method: 'PUT', body: JSON.stringify({ title }) });
        loadChatHistory();
    } catch { /* ignore */ }
}

function exportChatSession(id, format) {
    window.open(`/api/chat/sessions/${id}/export?format=${format}&key=${encodeURIComponent(getApiKey())}`, '_blank');
}

async function exportCurrentChat() {
    if (chatMessages.length === 0) { showToast('当前无对话内容', 'info'); return; }
    // Show format picker
    showExportMenu();
}

function showExportMenu() {
    // Remove existing menu
    const old = document.getElementById('chatExportMenu');
    if (old) { old.remove(); return; }

    const btn = document.getElementById('chatExportBtn');
    const menu = document.createElement('div');
    menu.id = 'chatExportMenu';
    menu.className = 'chat-export-menu';
    menu.innerHTML = `
        <button onclick="doChatExport('text')">📋 复制为文本</button>
        <button onclick="doChatExport('md')">📝 复制为 Markdown</button>
        <button onclick="doChatExport('md-file')">💾 下载 Markdown</button>
        <button onclick="doChatExport('json-file')">💾 下载 JSON</button>
        <button onclick="doChatExport('image')">🖼 截图为图片</button>
    `;
    btn.parentElement.style.position = 'relative';
    btn.parentElement.appendChild(menu);
    // Close on outside click
    setTimeout(() => {
        document.addEventListener('click', function closeMenu(e) {
            if (!menu.contains(e.target) && e.target !== btn) {
                menu.remove();
                document.removeEventListener('click', closeMenu);
            }
        });
    }, 10);
}

async function doChatExport(format) {
    const menu = document.getElementById('chatExportMenu');
    if (menu) menu.remove();

    if (format === 'text') {
        const text = chatMessages.map(m => {
            const role = m.role === 'user' ? '用户' : m.role === 'assistant' ? '助手' : '系统';
            return `【${role}】\n${m.content}`;
        }).join('\n\n');
        try { await navigator.clipboard.writeText(text); } catch { fallbackCopy(text); }
        showToast('已复制为文本', 'success');
    } else if (format === 'md') {
        const md = chatMessages.map(m => {
            const role = m.role === 'user' ? '用户' : m.role === 'assistant' ? '助手' : '系统';
            return `## ${role}\n\n${m.content}`;
        }).join('\n\n---\n\n');
        const fullMd = '# 对话记录\n\n' + md;
        try { await navigator.clipboard.writeText(fullMd); } catch { fallbackCopy(fullMd); }
        showToast('已复制为 Markdown', 'success');
    } else if (format === 'md-file') {
        await autoSaveSession();
        if (currentSessionId) {
            exportChatSession(currentSessionId, 'md');
        } else {
            let md = '# 对话导出\n\n';
            for (const m of chatMessages) {
                const role = m.role === 'user' ? '用户' : m.role === 'assistant' ? '助手' : '系统';
                md += `## ${role}\n\n${m.content}\n\n---\n\n`;
            }
            downloadBlob(md, 'chat_export.md', 'text/markdown');
        }
    } else if (format === 'json-file') {
        await autoSaveSession();
        if (currentSessionId) {
            exportChatSession(currentSessionId, 'json');
        } else {
            const json = JSON.stringify(chatMessages, null, 2);
            downloadBlob(json, 'chat_export.json', 'application/json');
        }
    } else if (format === 'image') {
        captureChat();
    }
}

function downloadBlob(content, filename, type) {
    const blob = new Blob([content], { type });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = filename;
    a.click();
    URL.revokeObjectURL(a.href);
}

// Capture chat area as image using canvas
function captureChat() {
    const el = document.getElementById('chatMessages');
    if (!el || el.children.length === 0) { showToast('无内容可截图', 'info'); return; }
    showToast('正在生成截图...', 'info');

    // Use a simple approach: serialize to canvas via html2canvas-like technique
    // Since we can't import external libs, we'll create a styled HTML and convert via SVG foreignObject
    const clone = el.cloneNode(true);
    clone.style.position = 'absolute';
    clone.style.left = '-9999px';
    clone.style.width = el.offsetWidth + 'px';
    clone.style.height = 'auto';
    clone.style.maxHeight = 'none';
    clone.style.overflow = 'visible';
    document.body.appendChild(clone);

    // Get computed styles
    const styles = document.querySelectorAll('style, link[rel="stylesheet"]');
    let cssText = '';
    styles.forEach(s => {
        if (s.tagName === 'STYLE') cssText += s.textContent;
    });

    const width = el.offsetWidth;
    const height = clone.scrollHeight;

    const svgData = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}">
        <foreignObject width="100%" height="100%">
            <div xmlns="http://www.w3.org/1999/xhtml">
                <style>${cssText}</style>
                ${clone.outerHTML}
            </div>
        </foreignObject>
    </svg>`;

    const canvas = document.createElement('canvas');
    canvas.width = width * 2;
    canvas.height = height * 2;
    const ctx = canvas.getContext('2d');
    ctx.scale(2, 2);

    const img = new Image();
    img.onload = () => {
        ctx.drawImage(img, 0, 0);
        canvas.toBlob(blob => {
            if (blob) {
                // Copy to clipboard if supported
                if (navigator.clipboard && navigator.clipboard.write) {
                    navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]).then(
                        () => showToast('截图已复制到剪贴板', 'success'),
                        () => {
                            // Fallback: download
                            const a = document.createElement('a');
                            a.href = URL.createObjectURL(blob);
                            a.download = 'chat_screenshot.png';
                            a.click();
                            showToast('截图已下载', 'success');
                        }
                    );
                } else {
                    const a = document.createElement('a');
                    a.href = URL.createObjectURL(blob);
                    a.download = 'chat_screenshot.png';
                    a.click();
                    showToast('截图已下载', 'success');
                }
            }
            clone.remove();
        }, 'image/png');
    };
    img.onerror = () => {
        showToast('截图生成失败，已切换为下载 Markdown', 'warning');
        clone.remove();
        doChatExport('md-file');
    };
    img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svgData);
}

// Copy a single chat bubble content
function copyChatBubble(btn, format) {
    const bubble = btn.closest('.chat-bubble');
    const textEl = bubble.querySelector('.chat-bubble-text');
    if (!textEl) return;

    // Find the message index — count only .chat-message elements (skip stats/empty)
    const msgEl = btn.closest('.chat-message');
    const container = document.getElementById('chatMessages');
    const allMsgs = Array.from(container.querySelectorAll('.chat-message'));
    const idx = allMsgs.indexOf(msgEl);
    const msg = idx >= 0 && idx < chatMessages.length ? chatMessages[idx] : null;

    let text;
    if (msg) {
        text = msg.content;
    } else {
        // Fallback: extract visible text from DOM
        text = textEl.innerText || textEl.textContent || '';
    }

    if (!text) { showToast('无内容可复制', 'info'); return; }

    navigator.clipboard.writeText(text).then(
        () => {
            const orig = btn.textContent;
            btn.textContent = '✅';
            setTimeout(() => btn.textContent = orig, 1500);
        },
        () => {
            // Fallback for non-HTTPS
            fallbackCopy(text);
            const orig = btn.textContent;
            btn.textContent = '✅';
            setTimeout(() => btn.textContent = orig, 1500);
        }
    );
}

// Copy code block content
function copyCodeBlock(btn) {
    const codeEl = btn.parentElement.querySelector('code');
    if (!codeEl) return;
    const text = codeEl.textContent || '';
    navigator.clipboard.writeText(text).then(
        () => {
            btn.textContent = '已复制';
            setTimeout(() => btn.textContent = '复制', 2000);
        },
        () => {
            fallbackCopy(text);
            btn.textContent = '已复制';
            setTimeout(() => btn.textContent = '复制', 2000);
        }
    );
}

// Fallback copy for non-HTTPS environments using textarea
function fallbackCopy(text) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch {}
    document.body.removeChild(ta);
}

function toggleChatSettings() {
    chatSettingsOpen = !chatSettingsOpen;
    const panel = document.getElementById('chatSettingsPanel');
    panel.classList.toggle('open', chatSettingsOpen);
}

function getChatOptions() {
    const opts = {};
    const temp = parseFloat(document.getElementById('chatTemperature').value);
    if (!isNaN(temp)) opts.temperature = temp;
    const topP = parseFloat(document.getElementById('chatTopP').value);
    if (!isNaN(topP)) opts.top_p = topP;
    const numCtx = document.getElementById('chatNumCtx').value;
    if (numCtx) opts.num_ctx = parseInt(numCtx);
    const numPredict = document.getElementById('chatNumPredict').value;
    if (numPredict) opts.num_predict = parseInt(numPredict);
    return opts;
}

function getChatFormat() {
    return document.getElementById('chatJsonMode').checked ? 'json' : '';
}

function getChatKeepAlive() {
    return document.getElementById('chatKeepAlive').value || '';
}

function getChatSystemPrompt() {
    return (document.getElementById('chatSystemPrompt').value || '').trim();
}

// ── Model parameter presets ─────────────────────────────────────
// Built-in recommended presets for common model families
const BUILTIN_PRESETS = {
    'qwen':      { temperature: 0.7, top_p: 0.8, num_ctx: 32768, label: 'Qwen 推荐' },
    'llama':     { temperature: 0.7, top_p: 0.9, num_ctx: 8192, label: 'Llama 推荐' },
    'codellama': { temperature: 0.2, top_p: 0.9, num_ctx: 16384, label: 'CodeLlama 推荐（低温度）' },
    'deepseek':  { temperature: 0.6, top_p: 0.9, num_ctx: 65536, label: 'DeepSeek 推荐' },
    'coder':     { temperature: 0.2, top_p: 0.95, num_ctx: 16384, label: '代码模型推荐' },
    'gemma':     { temperature: 0.7, top_p: 0.9, num_ctx: 8192, label: 'Gemma 推荐' },
    'phi':       { temperature: 0.7, top_p: 0.9, num_ctx: 4096, label: 'Phi 推荐' },
    'mistral':   { temperature: 0.7, top_p: 0.9, num_ctx: 32768, label: 'Mistral 推荐' },
    'mixtral':   { temperature: 0.7, top_p: 0.9, num_ctx: 32768, label: 'Mixtral 推荐' },
    'llava':     { temperature: 0.7, top_p: 0.9, num_ctx: 4096, label: 'LLaVA 视觉推荐' },
    'command-r': { temperature: 0.3, top_p: 0.9, num_ctx: 131072, label: 'Command R 推荐' },
};

function matchBuiltinPreset(modelName) {
    const name = (modelName || '').toLowerCase();
    // Match specific patterns first, then generic
    if (name.includes('codellama') || name.includes('code-llama')) return BUILTIN_PRESETS['codellama'];
    if (name.includes('coder') || name.includes('starcoder')) return BUILTIN_PRESETS['coder'];
    if (name.includes('command-r') || name.includes('command_r')) return BUILTIN_PRESETS['command-r'];
    if (name.includes('deepseek')) return BUILTIN_PRESETS['deepseek'];
    if (name.includes('qwen')) return BUILTIN_PRESETS['qwen'];
    if (name.includes('llava') || name.includes('vision')) return BUILTIN_PRESETS['llava'];
    if (name.includes('llama')) return BUILTIN_PRESETS['llama'];
    if (name.includes('gemma')) return BUILTIN_PRESETS['gemma'];
    if (name.includes('phi')) return BUILTIN_PRESETS['phi'];
    if (name.includes('mistral')) return BUILTIN_PRESETS['mistral'];
    if (name.includes('mixtral')) return BUILTIN_PRESETS['mixtral'];
    return null;
}

function isVisionModel(modelName) {
    const name = (modelName || '').toLowerCase();
    return ['llava', 'vision', 'minicpm-v', 'moondream', 'bakllava', 'cogvlm', 'internvl'].some(k => name.includes(k));
}

function friendlyChatError(err) {
    if (!err) return '未知错误';
    const e = err.toLowerCase();
    if (e.includes('image input') || e.includes('missing data required for image'))
        return '当前模型不支持图片输入，请使用视觉模型（如 llava、llama3.2-vision）';
    if (e.includes('not found') || e.includes('no such model'))
        return '模型不存在，请先下载该模型';
    if (e.includes('context length') || e.includes('num_ctx') || e.includes('too long'))
        return '输入内容超出模型上下文长度限制，请缩短输入或增大 num_ctx';
    if (e.includes('out of memory') || e.includes('oom') || e.includes('not enough memory'))
        return '显存不足，请尝试使用更小的模型或减小上下文长度';
    if (e.includes('connection refused') || e.includes('connect:'))
        return 'Ollama 服务连接失败，请检查服务是否运行中';
    if (e.includes('timeout') || e.includes('deadline'))
        return '请求超时，模型可能正在加载中，请稍后重试';
    return err;
}

function openImageLightbox(src, name) {
    let lb = document.getElementById('chatImageLightbox');
    if (!lb) {
        lb = document.createElement('div');
        lb.id = 'chatImageLightbox';
        lb.className = 'chat-lightbox';
        lb.innerHTML = `<div class="chat-lightbox-backdrop" onclick="closeImageLightbox()"></div>
            <div class="chat-lightbox-content">
                <img id="chatLightboxImg" src="" alt="">
                <div class="chat-lightbox-name" id="chatLightboxName"></div>
                <button class="chat-lightbox-close" onclick="closeImageLightbox()">✕</button>
            </div>`;
        document.body.appendChild(lb);
    }
    document.getElementById('chatLightboxImg').src = src;
    document.getElementById('chatLightboxName').textContent = name || '';
    lb.classList.add('open');
    document.addEventListener('keydown', lightboxEscHandler);
}

function closeImageLightbox() {
    const lb = document.getElementById('chatImageLightbox');
    if (lb) lb.classList.remove('open');
    document.removeEventListener('keydown', lightboxEscHandler);
}

function lightboxEscHandler(e) {
    if (e.key === 'Escape') closeImageLightbox();
}

// User custom presets stored in localStorage
function getUserPresets() {
    try { return JSON.parse(localStorage.getItem('ollama_chat_presets') || '{}'); } catch { return {}; }
}
function saveUserPreset(modelName, preset) {
    const presets = getUserPresets();
    presets[modelName] = preset;
    localStorage.setItem('ollama_chat_presets', JSON.stringify(presets));
}
function deleteUserPreset(modelName) {
    const presets = getUserPresets();
    delete presets[modelName];
    localStorage.setItem('ollama_chat_presets', JSON.stringify(presets));
}

// Apply a preset to the settings panel
function applyPresetToPanel(preset) {
    if (!preset) return;
    if (preset.temperature != null) {
        document.getElementById('chatTemperature').value = preset.temperature;
        document.getElementById('chatTempVal').textContent = preset.temperature;
    }
    if (preset.top_p != null) {
        document.getElementById('chatTopP').value = preset.top_p;
        document.getElementById('chatTopPVal').textContent = preset.top_p;
    }
    if (preset.num_ctx != null) {
        document.getElementById('chatNumCtx').value = String(preset.num_ctx);
    }
    if (preset.num_predict != null) {
        document.getElementById('chatNumPredict').value = String(preset.num_predict);
    }
    if (preset.system_prompt != null) {
        document.getElementById('chatSystemPrompt').value = preset.system_prompt;
    }
    if (preset.json_mode != null) {
        document.getElementById('chatJsonMode').checked = preset.json_mode;
    }
    if (preset.keep_alive != null) {
        document.getElementById('chatKeepAlive').value = preset.keep_alive;
    }
}

// Read current panel values as a preset object
function readPanelAsPreset() {
    return {
        temperature: parseFloat(document.getElementById('chatTemperature').value),
        top_p: parseFloat(document.getElementById('chatTopP').value),
        num_ctx: parseInt(document.getElementById('chatNumCtx').value) || null,
        num_predict: parseInt(document.getElementById('chatNumPredict').value) || null,
        system_prompt: document.getElementById('chatSystemPrompt').value || '',
        json_mode: document.getElementById('chatJsonMode').checked,
        keep_alive: document.getElementById('chatKeepAlive').value || '',
    };
}

// Called when model selection changes — auto-fill parameters
async function onChatModelChange() {
    const model = document.getElementById('chatModelSelect').value;
    if (!model) return;

    updatePresetIndicator(model);

    // Priority: user preset > Ollama model defaults > built-in preset > defaults
    const userPresets = getUserPresets();
    if (userPresets[model]) {
        applyPresetToPanel(userPresets[model]);
        showPresetSource('用户预设');
        return;
    }

    // Try fetching model info from Ollama
    try {
        const info = await api(`/api/models/${encodeURIComponent(model)}/info`);
        if (info && info.parameters) {
            const parsed = parseOllamaParameters(info.parameters);
            if (Object.keys(parsed).length > 0) {
                applyPresetToPanel(parsed);
                showPresetSource('模型默认');
                return;
            }
        }
    } catch { /* ignore, fall through */ }

    // Built-in preset
    const builtin = matchBuiltinPreset(model);
    if (builtin) {
        applyPresetToPanel(builtin);
        showPresetSource(builtin.label);
        return;
    }

    showPresetSource('默认参数');
}

// Parse Ollama's "parameters" text block (key value per line)
function parseOllamaParameters(text) {
    const preset = {};
    if (!text) return preset;
    const lines = text.split('\n');
    for (const line of lines) {
        const parts = line.trim().split(/\s+/);
        if (parts.length < 2) continue;
        const key = parts[0], val = parts.slice(1).join(' ');
        switch (key) {
            case 'temperature': preset.temperature = parseFloat(val); break;
            case 'top_p': preset.top_p = parseFloat(val); break;
            case 'num_ctx': preset.num_ctx = parseInt(val); break;
            case 'num_predict': preset.num_predict = parseInt(val); break;
            case 'stop': break; // skip stop tokens
            default: break;
        }
    }
    return preset;
}

function showPresetSource(text) {
    const el = document.getElementById('chatPresetSource');
    if (el) { el.textContent = text; el.style.display = text ? '' : 'none'; }
}

function updatePresetIndicator(model) {
    const userPresets = getUserPresets();
    const saveBtn = document.getElementById('chatSavePresetBtn');
    const delBtn = document.getElementById('chatDeletePresetBtn');
    if (saveBtn) saveBtn.style.display = model ? '' : 'none';
    if (delBtn) delBtn.style.display = userPresets[model] ? '' : 'none';
}

function saveCurrentPreset() {
    const model = document.getElementById('chatModelSelect').value;
    if (!model) { showToast('请先选择模型', 'error'); return; }
    saveUserPreset(model, readPanelAsPreset());
    updatePresetIndicator(model);
    showToast(`已保存 ${model} 参数预设`, 'success');
    showPresetSource('用户预设');
}

function deleteCurrentPreset() {
    const model = document.getElementById('chatModelSelect').value;
    if (!model) return;
    deleteUserPreset(model);
    updatePresetIndicator(model);
    showToast(`已删除 ${model} 参数预设`, 'info');
    onChatModelChange(); // re-apply defaults
}

function initChat() {
    if (!chatInitialized) {
        chatInitialized = true;
    }
    // Populate model selector from cached status or fetch
    const select = document.getElementById('chatModelSelect');
    const models = (currentStatus && currentStatus.models) || _allModels || [];
    const currentVal = select.value;
    select.innerHTML = '<option value="">选择模型...</option>';
    models.forEach(m => {
        if (m.name && !m.name.toLowerCase().includes('embed')) {
            const opt = document.createElement('option');
            opt.value = m.name;
            opt.textContent = `${m.name} (${m.size_human || ''})`;
            select.appendChild(opt);
        }
    });
    if (currentVal) select.value = currentVal;
    // If no models loaded yet, try fetching
    if (models.length === 0) {
        api('/api/models').then(list => {
            if (list && list.length) {
                list.forEach(m => {
                    if (m.name && !m.name.toLowerCase().includes('embed')) {
                        const opt = document.createElement('option');
                        opt.value = m.name;
                        opt.textContent = `${m.name} (${m.size_human || ''})`;
                        select.appendChild(opt);
                    }
                });
            }
        }).catch(() => {});
    }
}

function ensureChatWs() {
    if (chatWs && chatWs.readyState === WebSocket.OPEN) return chatWs;
    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    chatWs = new WebSocket(`${wsProtocol}//${location.host}/api/ws/chat?key=${encodeURIComponent(getApiKey())}`);
    chatWs.onclose = () => { chatWs = null; };
    chatWs.onerror = () => { showToast('对话连接失败', 'error'); };
    return chatWs;
}

function sendChat() {
    const input = document.getElementById('chatInput');
    const text = input.value.trim();
    const model = document.getElementById('chatModelSelect').value;
    if (!text || !model) {
        if (!model) showToast('请先选择模型', 'error');
        return;
    }

    // Check: uploading images requires a vision-capable model
    const hasImages = chatUploadedFiles.some(f => f.is_image);
    if (hasImages && !isVisionModel(model)) {
        showToast('当前模型不支持图片输入，请选择视觉模型（如 llava、llama3.2-vision、minicpm-v 等）', 'error');
        return;
    }

    // Add user message
    const userMsg = { role: 'user', content: text, files: chatUploadedFiles.map(f => f.id) };
    chatMessages.push(userMsg);
    const fileInfos = chatUploadedFiles.map(f => ({ name: f.name, isImage: f.is_image, url: f._localUrl }));
    appendChatBubble('user', text, fileInfos);

    // Clear input
    input.value = '';
    autoResizeChatInput();
    chatUploadedFiles = [];
    renderChatUploadTags();

    // Prepare and send
    const ws = ensureChatWs();
    const doSend = () => {
        chatStreaming = true;
        chatCurrentResponse = '';
        chatThinkingContent = '';
        document.getElementById('chatSendBtn').style.display = 'none';
        document.getElementById('chatStopBtn').style.display = '';
        appendChatBubble('assistant', '', []);

        // Build messages with optional system prompt
        const msgsToSend = [...chatMessages];
        const sysPrompt = getChatSystemPrompt();
        if (sysPrompt && (msgsToSend.length === 0 || msgsToSend[0].role !== 'system')) {
            msgsToSend.unshift({ role: 'system', content: sysPrompt });
        }

        const payload = {
            type: 'chat',
            model: model,
            messages: msgsToSend,
            options: getChatOptions(),
        };
        const fmt = getChatFormat();
        if (fmt) payload.format = fmt;
        const ka = getChatKeepAlive();
        if (ka) payload.keep_alive = ka;
        if (document.getElementById('chatThinkMode').checked) payload.think = true;

        ws.send(JSON.stringify(payload));
    };

    if (ws.readyState === WebSocket.OPEN) {
        doSend();
    } else {
        ws.onopen = doSend;
    }

    ws.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            switch (data.type) {
                case 'token':
                    chatCurrentResponse += data.content;
                    updateLastAssistantBubble(chatCurrentResponse, false, chatThinkingContent);
                    break;
                case 'thinking':
                    chatThinkingContent += data.content;
                    updateLastAssistantBubble(chatCurrentResponse, false, chatThinkingContent);
                    break;
                case 'done':
                    chatStreaming = false;
                    chatMessages.push({ role: 'assistant', content: chatCurrentResponse });
                    updateLastAssistantBubble(chatCurrentResponse, true, chatThinkingContent);
                    document.getElementById('chatSendBtn').style.display = '';
                    document.getElementById('chatStopBtn').style.display = 'none';
                    // Show stats
                    if (data.eval_count && data.total_duration) {
                        const secs = data.total_duration / 1e9;
                        const tps = (data.eval_count / (data.eval_duration / 1e9)).toFixed(1);
                        showChatStats(`${data.eval_count} tokens · ${secs.toFixed(1)}s · ${tps} tok/s`);
                    }
                    autoSaveSession(); // persist to SQLite
                    break;
                case 'stopped':
                    chatStreaming = false;
                    chatMessages.push({ role: 'assistant', content: chatCurrentResponse });
                    updateLastAssistantBubble(chatCurrentResponse, true, chatThinkingContent);
                    document.getElementById('chatSendBtn').style.display = '';
                    document.getElementById('chatStopBtn').style.display = 'none';
                    autoSaveSession(); // persist to SQLite
                    break;
                case 'error':
                    chatStreaming = false;
                    showToast('对话错误: ' + friendlyChatError(data.error), 'error');
                    document.getElementById('chatSendBtn').style.display = '';
                    document.getElementById('chatStopBtn').style.display = 'none';
                    break;
            }
        } catch (e) { /* ignore parse errors */ }
    };
}

function stopChat() {
    if (chatWs && chatWs.readyState === WebSocket.OPEN) {
        chatWs.send(JSON.stringify({ type: 'stop' }));
    }
}

// clearChat is an alias for newChatSession for backward compatibility
function clearChat() { newChatSession(); }

function handleChatKeydown(e) {
    // Ctrl+Enter or Cmd+Enter to send (anti-accidental-send)
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        sendChat();
    }
    // Ctrl+A / Cmd+A to select all text in textarea
    if (e.key === 'a' && (e.ctrlKey || e.metaKey)) {
        e.target.select();
    }
}

function autoResizeChatInput() {
    const el = document.getElementById('chatInput');
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 200) + 'px';
}

// ── Chat file upload ────────────────────────────────────────────
async function handleChatFileUpload(event) {
    const files = event.target.files;
    if (!files.length) return;
    for (const file of files) {
        const formData = new FormData();
        formData.append('file', file);
        try {
            const resp = await fetch('/api/chat/upload', {
                method: 'POST',
                headers: { 'X-API-Key': getApiKey() },
                body: formData,
            });
            const data = await resp.json();
            if (data.success && data.data) {
                const fileData = data.data;
                // For images, create a local blob URL for thumbnail preview
                if (fileData.is_image) {
                    fileData._localUrl = URL.createObjectURL(file);
                }
                chatUploadedFiles.push(fileData);
                renderChatUploadTags();
            } else {
                showToast(`上传失败: ${data.error || '未知错误'}`, 'error');
            }
        } catch (err) {
            showToast(`上传失败: ${err.message}`, 'error');
        }
    }
    event.target.value = '';
}

function renderChatUploadTags() {
    const container = document.getElementById('chatUploadTags');
    if (!chatUploadedFiles.length) { container.innerHTML = ''; return; }
    container.innerHTML = chatUploadedFiles.map((f, i) => {
        if (f.is_image && f._localUrl) {
            return `<span class="chat-file-tag chat-file-tag-img"><img src="${f._localUrl}" class="chat-upload-thumb" alt="${escapeHtml(f.name)}"> ${escapeHtml(f.name)} <span class="chat-file-tag-remove" onclick="removeChatFile(${i})">✕</span></span>`;
        }
        return `<span class="chat-file-tag">📄 ${escapeHtml(f.name)} <span class="chat-file-tag-remove" onclick="removeChatFile(${i})">✕</span></span>`;
    }).join('');
}

function removeChatFile(index) {
    chatUploadedFiles.splice(index, 1);
    renderChatUploadTags();
}

// ── Chat bubble rendering ───────────────────────────────────────
function appendChatBubble(role, content, fileInfos) {
    const container = document.getElementById('chatMessages');
    const empty = container.querySelector('.chat-empty-state');
    if (empty) empty.remove();

    const bubble = document.createElement('div');
    bubble.className = `chat-message chat-message-${role}`;

    let filesHtml = '';
    if (fileInfos && fileInfos.length) {
        filesHtml = '<div class="chat-msg-files">' + fileInfos.map(f => {
            if (f.isImage && f.url) {
                return `<img src="${f.url}" class="chat-msg-img-preview" alt="${escapeHtml(f.name)}" title="点击查看原图" onclick="openImageLightbox(this.src, '${escapeAttr(f.name)}')">`;
            }
            return `<span class="chat-msg-file-tag">📄 ${escapeHtml(f.name)}</span>`;
        }).join('') + '</div>';
    }

    if (role === 'user') {
        bubble.innerHTML = `<div class="chat-bubble chat-bubble-user">${filesHtml}<div class="chat-bubble-text">${escapeHtml(content)}</div><div class="chat-bubble-actions"><button class="chat-copy-btn" onclick="copyChatBubble(this,'text')" title="复制文本">📋</button></div></div>`;
    } else {
        bubble.innerHTML = `<div class="chat-bubble chat-bubble-assistant"><div class="chat-bubble-text chat-md-content">${content ? renderChatMarkdown(content) : '<span class="chat-typing">●●●</span>'}</div><div class="chat-bubble-actions"><button class="chat-copy-btn" onclick="copyChatBubble(this,'text')" title="复制文本">📋</button><button class="chat-copy-btn" onclick="copyChatBubble(this,'md')" title="复制 Markdown">📝</button></div></div>`;
    }
    container.appendChild(bubble);
    container.scrollTop = container.scrollHeight;
}

function updateLastAssistantBubble(content, finalize, thinking) {
    const container = document.getElementById('chatMessages');
    const bubbles = container.querySelectorAll('.chat-message-assistant');
    const last = bubbles[bubbles.length - 1];
    if (!last) return;
    const textEl = last.querySelector('.chat-bubble-text');

    let thinkHtml = '';
    if (thinking) {
        if (finalize) {
            thinkHtml = `<details class="chat-thinking-block"><summary>🧠 思维链</summary><div class="chat-thinking-content">${renderChatMarkdown(thinking)}</div></details>`;
        } else {
            thinkHtml = `<div class="chat-thinking-streaming">🧠 思考中...<div class="chat-thinking-preview">${escapeHtml(thinking.slice(-200))}</div></div>`;
        }
    }

    if (finalize) {
        textEl.innerHTML = thinkHtml + renderChatMarkdown(content);
    } else {
        textEl.textContent = content;
        textEl.innerHTML = thinkHtml + textEl.innerHTML + '<span class="chat-typing-cursor">▌</span>';
    }
    container.scrollTop = container.scrollHeight;
}

function showChatStats(text) {
    const container = document.getElementById('chatMessages');
    const stats = document.createElement('div');
    stats.className = 'chat-stats';
    stats.textContent = text;
    container.appendChild(stats);
    container.scrollTop = container.scrollHeight;
}

// ── Lightweight Markdown renderer ───────────────────────────────
function renderChatMarkdown(text) {
    // Escape HTML first
    let html = text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');

    // Code blocks: ```lang\n...\n```
    html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
        const langLabel = lang ? `<span class="chat-code-lang">${lang.toUpperCase()}</span>` : '';
        const highlighted = highlightCode(code, lang);
        return `<div class="chat-code-block">${langLabel}<pre><code>${highlighted}</code></pre><button class="chat-code-copy" onclick="copyCodeBlock(this)">复制</button></div>`;
    });

    // Inline code: `code`
    html = html.replace(/`([^`]+)`/g, '<code class="chat-inline-code">$1</code>');

    // Tables: | ... | ... |
    html = html.replace(/((?:\|[^\n]+\|\n?)+)/g, (match) => {
        const rows = match.trim().split('\n').filter(r => r.trim());
        if (rows.length < 2) return match;
        // Check if second row is separator
        if (!/^\|[\s\-:|]+\|$/.test(rows[1])) return match;
        const headerCells = rows[0].split('|').filter(c => c.trim());
        const thead = `<thead><tr>${headerCells.map(c => `<th>${c.trim()}</th>`).join('')}</tr></thead>`;
        const bodyRows = rows.slice(2).map(row => {
            const cells = row.split('|').filter(c => c.trim());
            return `<tr>${cells.map(c => `<td>${c.trim()}</td>`).join('')}</tr>`;
        }).join('');
        return `<table class="chat-table">${thead}<tbody>${bodyRows}</tbody></table>`;
    });

    // Headers
    html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
    html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');

    // Bold / Italic
    html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');

    // Unordered lists
    html = html.replace(/^[\-\*] (.+)$/gm, '<li>$1</li>');
    html = html.replace(/(<li>.*<\/li>\n?)+/g, '<ul>$&</ul>');

    // Ordered lists
    html = html.replace(/^\d+\. (.+)$/gm, '<li>$1</li>');

    // Links: [text](url)
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');

    // Images: ![alt](url)
    html = html.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, '<img src="$2" alt="$1" class="chat-img" />');

    // Line breaks → paragraphs
    html = html.replace(/\n\n/g, '</p><p>');
    html = html.replace(/\n/g, '<br>');
    html = '<p>' + html + '</p>';

    // Clean up empty paragraphs
    html = html.replace(/<p>\s*<\/p>/g, '');
    html = html.replace(/<p>(<h[1-4]>)/g, '$1');
    html = html.replace(/(<\/h[1-4]>)<\/p>/g, '$1');
    html = html.replace(/<p>(<div class="chat-code-block">)/g, '$1');
    html = html.replace(/(<\/div>)<\/p>/g, '$1');
    html = html.replace(/<p>(<table)/g, '$1');
    html = html.replace(/(<\/table>)<\/p>/g, '$1');
    html = html.replace(/<p>(<ul>)/g, '$1');
    html = html.replace(/(<\/ul>)<\/p>/g, '$1');

    return html;
}

// Lightweight syntax highlighting (no external deps)
function highlightCode(code, lang) {
    if (!code) return code;
    lang = (lang || '').toLowerCase();
    const kwSets = {
        py: 'def|class|import|from|return|if|elif|else|for|while|in|not|and|or|is|with|as|try|except|finally|raise|yield|lambda|pass|break|continue|True|False|None|self|print|async|await',
        js: 'function|const|let|var|return|if|else|for|while|do|switch|case|break|continue|class|new|this|import|export|from|default|async|await|try|catch|finally|throw|typeof|instanceof|null|undefined|true|false|of|in|yield',
        go: 'func|package|import|return|if|else|for|range|switch|case|break|continue|type|struct|interface|map|chan|go|defer|select|var|const|nil|true|false|make|append|len|cap|error|string|int|bool|byte|float64',
        java: 'public|private|protected|static|final|class|interface|extends|implements|return|if|else|for|while|do|switch|case|break|continue|new|this|super|try|catch|finally|throw|throws|null|true|false|void|int|long|double|boolean|String|import|package',
        sh: 'if|then|else|elif|fi|for|do|done|while|until|case|esac|function|return|exit|echo|export|source|local|readonly|set|unset|true|false',
        sql: 'SELECT|FROM|WHERE|INSERT|UPDATE|DELETE|CREATE|DROP|ALTER|TABLE|INDEX|JOIN|LEFT|RIGHT|INNER|OUTER|ON|AND|OR|NOT|IN|EXISTS|NULL|AS|ORDER|BY|GROUP|HAVING|LIMIT|OFFSET|SET|INTO|VALUES|COUNT|SUM|AVG|MAX|MIN|DISTINCT|UNION',
    };
    const langMap = { python: 'py', javascript: 'js', typescript: 'js', golang: 'go', bash: 'sh', shell: 'sh', zsh: 'sh', c: 'java', cpp: 'java', rust: 'go', ruby: 'py', php: 'js', swift: 'go', kotlin: 'java', mysql: 'sql', postgresql: 'sql' };
    const mapped = langMap[lang] || lang;
    const kw = kwSets[mapped] || kwSets['js'] || '';
    let result = code;
    const tokens = [];
    let ti = 0;
    // Protect strings
    result = result.replace(/(["'])(?:(?!\1|\\).|\\.)*\1/g, m => { const ph = `__TK${ti}__`; tokens.push(`<span class="hl-str">${m}</span>`); ti++; return ph; });
    // Protect comments
    result = result.replace(/(#[^\n]*|\/\/[^\n]*)/g, m => { const ph = `__TK${ti}__`; tokens.push(`<span class="hl-cmt">${m}</span>`); ti++; return ph; });
    // Keywords
    if (kw) result = result.replace(new RegExp(`\\b(${kw})\\b`, 'g'), '<span class="hl-kw">$1</span>');
    // Numbers
    result = result.replace(/\b(\d+\.?\d*)\b/g, '<span class="hl-num">$1</span>');
    // Restore
    for (let i = 0; i < tokens.length; i++) result = result.replace(`__TK${i}__`, tokens[i]);
    return result;
}

// ── Init ────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', async () => {
    const savedKey = getApiKey();
    if (savedKey) {
        // Validate saved key
        try {
            const resp = await fetch('/api/auth/verify', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ key: savedKey }),
            });
            const data = await resp.json();
            if (data.success) {
                showApp();
                return;
            }
        } catch (e) {
            // Verification failed, show login
        }
        clearApiKey();
    }
    // Show auth screen (already visible by default)
});

// ── Performance Monitor Module ──────────────────────────────────
let perfWs = null;
let perfHistory = [];
let perfEnabled = true;
let perfInterval = 3;
let perfWindow = 300; // seconds
const PERF_SVG_W = 300, PERF_SVG_H = 120;

function initPerfMonitor() {
    if (perfWs && perfWs.readyState <= 1) return; // already connected
    const wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    perfWs = new WebSocket(`${wsProto}//${location.host}/api/ws/perf?key=${encodeURIComponent(getApiKey())}`);
    perfWs.onopen = () => {
        if (perfEnabled) perfWs.send(JSON.stringify({ type: 'start', interval: perfInterval }));
    };
    perfWs.onmessage = (e) => {
        try {
            const msg = JSON.parse(e.data);
            if (msg.type === 'perf' && msg.data) {
                perfHistory.push(msg.data);
                // Trim to window
                const maxPoints = Math.ceil(perfWindow / perfInterval) + 5;
                if (perfHistory.length > maxPoints) perfHistory = perfHistory.slice(-maxPoints);
                renderPerfCharts();
            }
        } catch {}
    };
    perfWs.onclose = () => { perfWs = null; };
    perfWs.onerror = () => {};
}

function stopPerfMonitor() {
    if (perfWs && perfWs.readyState === WebSocket.OPEN) {
        perfWs.send(JSON.stringify({ type: 'stop' }));
    }
}

function togglePerfMonitor(on) {
    perfEnabled = on;
    document.getElementById('perfToggleLabel').textContent = on ? '实时' : '暂停';
    if (on) {
        if (!perfWs || perfWs.readyState > 1) initPerfMonitor();
        else perfWs.send(JSON.stringify({ type: 'start', interval: perfInterval }));
    } else {
        stopPerfMonitor();
    }
}

function changePerfInterval(val) {
    perfInterval = parseInt(val) || 3;
    if (perfWs && perfWs.readyState === WebSocket.OPEN && perfEnabled) {
        perfWs.send(JSON.stringify({ type: 'interval', value: perfInterval }));
    }
}

function changePerfWindow(val) {
    perfWindow = parseInt(val) || 300;
}

function renderPerfCharts() {
    const h = perfHistory;
    if (h.length < 2) return;
    const now = h[h.length - 1].ts;
    const windowStart = now - perfWindow;
    const visible = h.filter(p => p.ts >= windowStart);
    if (visible.length < 2) return;

    // Helper: map data points to SVG polyline string
    function toPolyline(points, maxVal) {
        if (maxVal <= 0) maxVal = 1;
        return points.map((p, i) => {
            const x = ((p.ts - windowStart) / perfWindow) * PERF_SVG_W;
            const y = PERF_SVG_H - (p.val / maxVal) * PERF_SVG_H;
            return `${x.toFixed(1)},${Math.max(0, Math.min(PERF_SVG_H, y)).toFixed(1)}`;
        }).join(' ');
    }
    function toArea(points, maxVal) {
        if (maxVal <= 0) maxVal = 1;
        const first = ((points[0].ts - windowStart) / perfWindow) * PERF_SVG_W;
        const last = ((points[points.length-1].ts - windowStart) / perfWindow) * PERF_SVG_W;
        let pts = `${first.toFixed(1)},${PERF_SVG_H} `;
        pts += points.map(p => {
            const x = ((p.ts - windowStart) / perfWindow) * PERF_SVG_W;
            const y = PERF_SVG_H - (p.val / maxVal) * PERF_SVG_H;
            return `${x.toFixed(1)},${Math.max(0, Math.min(PERF_SVG_H, y)).toFixed(1)}`;
        }).join(' ');
        pts += ` ${last.toFixed(1)},${PERF_SVG_H}`;
        return pts;
    }
    function setTimeAxis(id) {
        const el = document.getElementById(id);
        if (!el) return;
        const labels = [];
        for (let i = 0; i < 5; i++) {
            const t = windowStart + (perfWindow / 4) * i;
            const d = new Date(t * 1000);
            labels.push(d.toTimeString().slice(0, 8));
        }
        el.innerHTML = labels.map(l => `<span>${l}</span>`).join('');
    }

    // --- CPU ---
    const cpuPts = visible.map(p => ({ ts: p.ts, val: p.cpu }));
    const cpuMax = 100;
    const cpuLast = cpuPts[cpuPts.length - 1].val;
    document.getElementById('perfCPUVal').textContent = cpuLast.toFixed(1) + '%';
    document.getElementById('perfCPULine').setAttribute('points', toPolyline(cpuPts, cpuMax));
    document.getElementById('perfCPUArea').setAttribute('points', toArea(cpuPts, cpuMax));
    setTimeAxis('perfCPUTime');

    // --- GPU ---
    const gpuPts = visible.map(p => ({ ts: p.ts, val: p.gpu_util }));
    const gpuLast = gpuPts[gpuPts.length - 1].val;
    document.getElementById('perfGPUVal').textContent = gpuLast + '%';
    document.getElementById('perfGPULine').setAttribute('points', toPolyline(gpuPts, 100));
    document.getElementById('perfGPUArea').setAttribute('points', toArea(gpuPts, 100));
    setTimeAxis('perfGPUTime');

    // --- Memory ---
    const memPts = visible.map(p => ({ ts: p.ts, val: p.mem_used }));
    const memMax = visible[0].mem_total || Math.max(...memPts.map(p => p.val)) * 1.2;
    const memLast = memPts[memPts.length - 1].val;
    document.getElementById('perfMemVal').textContent = memLast.toFixed(1) + ' / ' + (memMax).toFixed(0) + ' GiB';
    document.getElementById('perfMemLine').setAttribute('points', toPolyline(memPts, memMax));
    document.getElementById('perfMemArea').setAttribute('points', toArea(memPts, memMax));
    setTimeAxis('perfMemTime');

    // --- Network (rate: delta / interval) ---
    const netRxRates = [], netTxRates = [];
    for (let i = 1; i < visible.length; i++) {
        const dt = visible[i].ts - visible[i-1].ts || perfInterval;
        netRxRates.push({ ts: visible[i].ts, val: Math.max(0, (visible[i].net_rx - visible[i-1].net_rx) / dt) });
        netTxRates.push({ ts: visible[i].ts, val: Math.max(0, (visible[i].net_tx - visible[i-1].net_tx) / dt) });
    }
    if (netRxRates.length > 0) {
        const netMax = Math.max(1024, ...netRxRates.map(p => p.val), ...netTxRates.map(p => p.val)) * 1.2;
        const rxLast = netRxRates[netRxRates.length - 1].val;
        const txLast = netTxRates[netTxRates.length - 1].val;
        document.getElementById('perfNetVal').textContent = `↓${formatRate(rxLast)} ↑${formatRate(txLast)}`;
        document.getElementById('perfNetRxLine').setAttribute('points', toPolyline(netRxRates, netMax));
        document.getElementById('perfNetRxArea').setAttribute('points', toArea(netRxRates, netMax));
        document.getElementById('perfNetTxLine').setAttribute('points', toPolyline(netTxRates, netMax));
    }
    setTimeAxis('perfNetTime');

    // --- Disk IO (rate) ---
    const diskRRates = [], diskWRates = [];
    for (let i = 1; i < visible.length; i++) {
        const dt = visible[i].ts - visible[i-1].ts || perfInterval;
        diskRRates.push({ ts: visible[i].ts, val: Math.max(0, (visible[i].block_read - visible[i-1].block_read) / dt) });
        diskWRates.push({ ts: visible[i].ts, val: Math.max(0, (visible[i].block_write - visible[i-1].block_write) / dt) });
    }
    if (diskRRates.length > 0) {
        const diskMax = Math.max(1024, ...diskRRates.map(p => p.val), ...diskWRates.map(p => p.val)) * 1.2;
        const rLast = diskRRates[diskRRates.length - 1].val;
        const wLast = diskWRates[diskWRates.length - 1].val;
        document.getElementById('perfDiskVal').textContent = `R:${formatRate(rLast)} W:${formatRate(wLast)}`;
        document.getElementById('perfDiskRLine').setAttribute('points', toPolyline(diskRRates, diskMax));
        document.getElementById('perfDiskRArea').setAttribute('points', toArea(diskRRates, diskMax));
        document.getElementById('perfDiskWLine').setAttribute('points', toPolyline(diskWRates, diskMax));
    }
    setTimeAxis('perfDiskTime');

    // --- Inference latency ---
    const inferPts = visible.map(p => ({ ts: p.ts, val: p.infer_ms || 0 }));
    const inferMax = Math.max(100, ...inferPts.map(p => p.val)) * 1.2;
    const inferLast = inferPts[inferPts.length - 1].val;
    document.getElementById('perfInferVal').textContent = inferLast > 0 ? inferLast + ' ms' : '-- ms';
    document.getElementById('perfInferLine').setAttribute('points', toPolyline(inferPts, inferMax));
    document.getElementById('perfInferArea').setAttribute('points', toArea(inferPts, inferMax));
    setTimeAxis('perfInferTime');
}

function formatRate(bytesPerSec) {
    if (bytesPerSec > 1e9) return (bytesPerSec / 1e9).toFixed(1) + ' GB/s';
    if (bytesPerSec > 1e6) return (bytesPerSec / 1e6).toFixed(1) + ' MB/s';
    if (bytesPerSec > 1e3) return (bytesPerSec / 1e3).toFixed(1) + ' KB/s';
    return bytesPerSec.toFixed(0) + ' B/s';
}

// Start/stop perf monitor when navigating pages
function onDashboardEnter() { if (perfEnabled) initPerfMonitor(); }
function onDashboardLeave() { stopPerfMonitor(); }

// Handle tab visibility
document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
        stopPerfMonitor();
    } else if (perfEnabled && document.getElementById('page-dashboard').classList.contains('active')) {
        if (!perfWs || perfWs.readyState > 1) initPerfMonitor();
        else perfWs.send(JSON.stringify({ type: 'start', interval: perfInterval }));
    }
});

// ── Benchmark Module ────────────────────────────────────────────
let benchmarkWs = null;

function initBenchmark() {
    let models = (currentStatus && currentStatus.models) || _allModels || [];
    if (models.length > 0) {
        renderBenchmarkModels(models);
    } else {
        // Fetch models if not yet loaded
        api('/api/models').then(list => {
            if (list && list.length) {
                _allModels = list;
                renderBenchmarkModels(list);
            } else {
                document.getElementById('benchmarkModelSelect').innerHTML = '<div class="empty-state">暂无模型</div>';
            }
        }).catch(() => {
            document.getElementById('benchmarkModelSelect').innerHTML = '<div class="empty-state">加载模型失败</div>';
        });
    }
    loadBenchmarkResults();
}

function renderBenchmarkModels(models) {
    const container = document.getElementById('benchmarkModelSelect');
    const filtered = models.filter(m => m.name && !m.name.toLowerCase().includes('embed'));
    if (!filtered.length) {
        container.innerHTML = '<div class="empty-state">暂无可评测的模型</div>';
        return;
    }
    container.innerHTML = filtered
        .map(m => `<label class="benchmark-model-check"><input type="checkbox" value="${escapeAttr(m.name)}"><span>${escapeHtml(m.name)}</span><span style="color:var(--text-muted);font-size:11px;margin-left:8px">${m.size_human || ''}</span></label>`)
        .join('');
}

async function loadBenchmarkResults() {
    try {
        const results = await api('/api/benchmark/results');
        if (results && results.length > 0) {
            renderBenchmarkResults(results);
        }
    } catch { /* ignore */ }
}

function startBenchmark() {
    const checkboxes = document.querySelectorAll('#benchmarkModelSelect input[type="checkbox"]:checked');
    const models = Array.from(checkboxes).map(cb => cb.value);
    if (models.length === 0) { showToast('请至少选择一个模型', 'error'); return; }

    document.getElementById('benchmarkStartBtn').style.display = 'none';
    document.getElementById('benchmarkStopBtn').style.display = '';
    document.getElementById('benchmarkProgressCard').style.display = '';
    document.getElementById('benchmarkProgressFill').style.width = '0%';
    document.getElementById('benchmarkProgressText').textContent = '正在连接...';

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    benchmarkWs = new WebSocket(`${wsProtocol}//${location.host}/api/ws/benchmark?key=${encodeURIComponent(getApiKey())}`);

    benchmarkWs.onopen = () => {
        benchmarkWs.send(JSON.stringify({ models }));
    };

    benchmarkWs.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            switch (data.phase) {
                case 'testing':
                    document.getElementById('benchmarkProgressFill').style.width = `${(data.progress / data.total) * 100}%`;
                    document.getElementById('benchmarkProgressText').textContent = `正在测试 ${data.model} — ${data.dimension} (${data.progress}/${data.total})`;
                    break;
                case 'score':
                    document.getElementById('benchmarkProgressFill').style.width = `${(data.progress / data.total) * 100}%`;
                    break;
                case 'done':
                    document.getElementById('benchmarkProgressFill').style.width = '100%';
                    document.getElementById('benchmarkProgressText').textContent = '评测完成！';
                    benchmarkFinish();
                    renderBenchmarkResults(data.results);
                    benchmarkWs.close();
                    break;
                case 'stopped':
                    document.getElementById('benchmarkProgressText').textContent = '评测已取消（已完成部分已保存）';
                    benchmarkFinish();
                    if (data.results && data.results.length) renderBenchmarkResults(data.results);
                    benchmarkWs.close();
                    break;
                case 'error':
                    showToast('评测错误: ' + data.error, 'error');
                    benchmarkFinish();
                    benchmarkWs.close();
                    break;
            }
        } catch { /* ignore */ }
    };

    benchmarkWs.onerror = () => {
        showToast('评测连接失败', 'error');
        benchmarkFinish();
    };
    benchmarkWs.onclose = () => { benchmarkWs = null; };
}

function stopBenchmark() {
    if (benchmarkWs && benchmarkWs.readyState === WebSocket.OPEN) {
        benchmarkWs.send(JSON.stringify({ type: 'stop' }));
    }
}

function benchmarkFinish() {
    document.getElementById('benchmarkStartBtn').style.display = '';
    document.getElementById('benchmarkStopBtn').style.display = 'none';
}

function renderBenchmarkResults(results) {
    if (!results || results.length === 0) return;
    const body = document.getElementById('benchmarkResultsBody');

    // Dimension icons
    const dimIcons = { reasoning: '🧠', math: '🔢', code: '💻', writing: '✍️', instruction: '📏', chinese: '🇨🇳' };

    // Leaderboard table
    let html = `<div class="table-container"><table class="benchmark-table">
        <thead><tr><th>排名</th><th>模型</th><th>总分</th><th>百分比</th><th>平均速度</th>`;

    // Add dimension columns from first result
    if (results[0].scores) {
        results[0].scores.forEach(s => {
            const icon = dimIcons[s.dimension_id] || '📋';
            html += `<th title="${s.name || s.dimension_id}">${icon}</th>`;
        });
    }
    html += `<th>时间</th></tr></thead><tbody>`;

    // Sort by percentage desc
    const sorted = [...results].sort((a, b) => (b.percentage || 0) - (a.percentage || 0));
    sorted.forEach((r, i) => {
        const pct = (r.percentage || 0).toFixed(1);
        const medal = i === 0 ? '🥇' : i === 1 ? '🥈' : i === 2 ? '🥉' : `${i + 1}`;
        const barColor = pct >= 80 ? 'var(--accent-green)' : pct >= 60 ? 'var(--accent-yellow,#f0ad4e)' : 'var(--accent-red)';
        html += `<tr>
            <td style="text-align:center">${medal}</td>
            <td><strong>${escapeHtml(r.model_name)}</strong></td>
            <td>${(r.total_score || 0).toFixed(1)} / ${r.max_total || 60}</td>
            <td><div style="display:flex;align-items:center;gap:6px"><div style="flex:1;height:6px;background:var(--bg-secondary);border-radius:3px"><div style="width:${pct}%;height:100%;background:${barColor};border-radius:3px"></div></div><span style="min-width:40px">${pct}%</span></div></td>
            <td>${(r.avg_tok_sec || 0).toFixed(1)} tok/s</td>`;
        if (r.scores) {
            r.scores.forEach(s => {
                const sc = (s.score || 0).toFixed(0);
                const clr = s.score >= 8 ? 'var(--accent-green)' : s.score >= 5 ? 'var(--accent-yellow,#f0ad4e)' : 'var(--accent-red)';
                html += `<td style="text-align:center"><span style="color:${clr};font-weight:600" title="${s.reasoning || ''}">${sc}</span></td>`;
            });
        }
        html += `<td style="font-size:11px;color:var(--text-muted)">${r.run_at ? new Date(r.run_at).toLocaleString('zh-CN') : '--'}</td></tr>`;
    });

    html += '</tbody></table></div>';

    // Detail cards for each model
    sorted.forEach(r => {
        if (!r.scores) return;
        html += `<div class="card" style="margin-top:12px">
            <div class="card-header"><h3>${escapeHtml(r.model_name)} — 详细评分</h3></div>
            <div class="benchmark-detail-grid">`;
        r.scores.forEach(s => {
            const icon = dimIcons[s.dimension_id] || '📋';
            const pct = ((s.score / (s.max_score || 10)) * 100).toFixed(0);
            html += `<div class="benchmark-dim-card">
                <div class="benchmark-dim-header">${icon} ${s.name || s.dimension_id} <span style="float:right;font-weight:600">${(s.score||0).toFixed(1)}/10</span></div>
                <div class="progress-bar" style="height:4px;margin:6px 0"><div class="progress-fill" style="width:${pct}%"></div></div>
                <div style="font-size:11px;color:var(--text-muted)">${s.reasoning || ''}</div>
                ${s.tok_per_sec ? `<div style="font-size:11px;color:var(--text-muted)">${s.token_count || 0} tokens · ${((s.duration_ms||0)/1000).toFixed(1)}s · ${(s.tok_per_sec||0).toFixed(1)} tok/s</div>` : ''}
            </div>`;
        });
        html += '</div></div>';
    });

    body.innerHTML = html;
}

// ── Model Compare Module ────────────────────────────────────────
let compareWsA = null, compareWsB = null;
let compareResponseA = '', compareResponseB = '';
let compareThinkingA = '', compareThinkingB = '';
let compareStreaming = false;

function initCompare() {
    let models = (currentStatus && currentStatus.models) || _allModels || [];
    populateCompareSelects(models);
    // If empty, fetch
    if (models.length === 0) {
        api('/api/models').then(list => {
            if (list && list.length) {
                _allModels = list;
                populateCompareSelects(list);
            }
        }).catch(() => {});
    }
}

function populateCompareSelects(models) {
    ['compareModelA', 'compareModelB'].forEach(id => {
        const sel = document.getElementById(id);
        const cur = sel.value;
        sel.innerHTML = `<option value="">${id.includes('A') ? '模型 A' : '模型 B'}</option>`;
        models.forEach(m => {
            if (m.name && !m.name.toLowerCase().includes('embed')) {
                const opt = document.createElement('option');
                opt.value = m.name;
                opt.textContent = `${m.name} (${m.size_human || ''})`;
                sel.appendChild(opt);
            }
        });
        if (cur) sel.value = cur;
    });
}

function toggleCompareSettings() {
    const panel = document.getElementById('compareSettingsPanel');
    panel.style.display = panel.style.display === 'none' ? 'block' : 'none';
}

function sendCompare() {
    const modelA = document.getElementById('compareModelA').value;
    const modelB = document.getElementById('compareModelB').value;
    const prompt = document.getElementById('compareInput').value.trim();
    if (!modelA || !modelB) { showToast('请选择两个模型', 'error'); return; }
    if (!prompt) { showToast('请输入 prompt', 'error'); return; }
    if (modelA === modelB) { showToast('请选择不同的模型', 'error'); return; }

    document.getElementById('compareLabelA').textContent = modelA;
    document.getElementById('compareLabelB').textContent = modelB;

    // Collect settings
    const systemPrompt = document.getElementById('compareSystemPrompt').value.trim();
    const temperature = parseFloat(document.getElementById('compareTemperature').value);
    const think = document.getElementById('compareThinkMode').checked;

    compareResponseA = '';
    compareResponseB = '';
    compareThinkingA = '';
    compareThinkingB = '';
    compareStreaming = true;

    // Toggle buttons
    document.getElementById('compareSendBtn').style.display = 'none';
    document.getElementById('compareStopBtn').style.display = '';

    startCompareStream('A', modelA, prompt, systemPrompt, temperature, think);
    startCompareStream('B', modelB, prompt, systemPrompt, temperature, think);
}

let compareDoneCount = 0;
function onCompareStreamEnd() {
    compareDoneCount++;
    if (compareDoneCount >= 2) {
        compareStreaming = false;
        compareDoneCount = 0;
        document.getElementById('compareSendBtn').style.display = '';
        document.getElementById('compareStopBtn').style.display = 'none';
    }
}

function startCompareStream(side, model, prompt, systemPrompt, temperature, think) {
    const outputEl = document.getElementById(`compareOutput${side}`);
    const statsEl = document.getElementById(`compareStats${side}`);
    outputEl.innerHTML = '<span class="chat-typing">●●●</span>';
    statsEl.textContent = '';

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${location.host}/api/ws/chat?key=${encodeURIComponent(getApiKey())}`);

    if (side === 'A') { if (compareWsA) compareWsA.close(); compareWsA = ws; }
    else { if (compareWsB) compareWsB.close(); compareWsB = ws; }

    let response = '';
    let thinking = '';
    const startTime = Date.now();

    ws.onopen = () => {
        const messages = [];
        if (systemPrompt) messages.push({ role: 'system', content: systemPrompt });
        messages.push({ role: 'user', content: prompt });

        const payload = {
            type: 'chat', model,
            messages,
            options: { temperature },
        };
        if (think) payload.think = true;
        ws.send(JSON.stringify(payload));
    };

    ws.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            switch (data.type) {
                case 'token':
                    response += data.content;
                    if (side === 'A') compareResponseA = response; else compareResponseB = response;
                    outputEl.innerHTML = renderChatMarkdown(response) + '<span class="chat-typing-cursor">▌</span>';
                    outputEl.scrollTop = outputEl.scrollHeight;
                    break;
                case 'thinking':
                    thinking += data.content;
                    if (side === 'A') compareThinkingA = thinking; else compareThinkingB = thinking;
                    break;
                case 'done': {
                    let thinkHtml = '';
                    if (thinking) {
                        thinkHtml = `<details class="chat-thinking-block"><summary>🧠 思维链 (${thinking.length} 字符)</summary><div class="chat-thinking-content">${renderChatMarkdown(thinking)}</div></details>`;
                    }
                    outputEl.innerHTML = thinkHtml + renderChatMarkdown(response);
                    const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
                    if (data.eval_count && data.eval_duration) {
                        const tps = (data.eval_count / (data.eval_duration / 1e9)).toFixed(1);
                        statsEl.innerHTML = `<span class="compare-stat-item">📊 ${data.eval_count} tokens</span><span class="compare-stat-item">⏱ ${elapsed}s</span><span class="compare-stat-item">⚡ ${tps} tok/s</span>`;
                    } else {
                        statsEl.innerHTML = `<span class="compare-stat-item">⏱ ${elapsed}s</span>`;
                    }
                    ws.close();
                    onCompareStreamEnd();
                    break;
                }
                case 'stopped':
                    outputEl.innerHTML = renderChatMarkdown(response) + '<div style="color:var(--text-muted);font-style:italic;margin-top:8px">⏹ 已停止</div>';
                    onCompareStreamEnd();
                    break;
                case 'error':
                    outputEl.innerHTML = `<span style="color:var(--accent-red)">${friendlyChatError(data.error)}</span>`;
                    ws.close();
                    onCompareStreamEnd();
                    break;
            }
        } catch { /* ignore */ }
    };
    ws.onerror = () => {
        outputEl.innerHTML = '<span style="color:var(--accent-red)">连接失败</span>';
        onCompareStreamEnd();
    };
}

function stopCompare() {
    [compareWsA, compareWsB].forEach(ws => {
        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'stop' }));
        }
    });
}

function clearCompare() {
    stopCompare();
    compareWsA = null;
    compareWsB = null;
    compareResponseA = '';
    compareResponseB = '';
    compareThinkingA = '';
    compareThinkingB = '';
    compareDoneCount = 0;
    compareStreaming = false;
    document.getElementById('compareSendBtn').style.display = '';
    document.getElementById('compareStopBtn').style.display = 'none';
    document.getElementById('compareOutputA').innerHTML = '<div class="chat-empty-state"><div class="chat-empty-text">等待输入</div></div>';
    document.getElementById('compareOutputB').innerHTML = '<div class="chat-empty-state"><div class="chat-empty-text">等待输入</div></div>';
    document.getElementById('compareStatsA').textContent = '';
    document.getElementById('compareStatsB').textContent = '';
    document.getElementById('compareInput').value = '';
}

function copySingleCompare(side) {
    const resp = side === 'A' ? compareResponseA : compareResponseB;
    if (!resp) { showToast('无内容可复制', 'info'); return; }
    navigator.clipboard.writeText(resp).then(
        () => showToast('已复制', 'success'),
        () => { fallbackCopy(resp); showToast('已复制', 'success'); }
    );
}

function copyCompareResult() {
    const modelA = document.getElementById('compareModelA').value || '模型 A';
    const modelB = document.getElementById('compareModelB').value || '模型 B';
    const prompt = document.getElementById('compareInput').value.trim();
    if (!compareResponseA && !compareResponseB) { showToast('无内容可复制', 'info'); return; }
    const statsA = document.getElementById('compareStatsA').textContent;
    const statsB = document.getElementById('compareStatsB').textContent;
    const md = `# 模型对比\n\n**Prompt:** ${prompt}\n\n---\n\n## ${modelA}\n${statsA ? `> ${statsA}\n\n` : ''}${compareResponseA}\n\n---\n\n## ${modelB}\n${statsB ? `> ${statsB}\n\n` : ''}${compareResponseB}`;
    navigator.clipboard.writeText(md).then(
        () => showToast('对比结果已复制为 Markdown', 'success'),
        () => { fallbackCopy(md); showToast('对比结果已复制为 Markdown', 'success'); }
    );
}
