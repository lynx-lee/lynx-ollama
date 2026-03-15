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

    // Update settings panel active state (if rendered)
    document.querySelectorAll('#themeOptions .theme-option').forEach(opt => {
        opt.classList.toggle('active', opt.dataset.themeValue === pref);
    });

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

    currentPage = page;

    // Notify WebSocket to switch between full/lite mode
    sendStatusWsCommand({
        type: 'subscribe',
        mode: page === 'dashboard' ? 'full' : 'lite',
    });

    // Load data for the page
    switch (page) {
        case 'dashboard': refreshStatus(); break;
        case 'models': loadModels(); break;
        case 'health': break; // Manual trigger
        case 'logs': loadLogs(); break;
        case 'config': loadConfig(); break;
        case 'gpu': loadGPUInfo(); break;
        case 'settings': applyTheme(getThemePreference()); break;
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
            } else {
                currentStatus = lite;
            }
            updateTopbarBadges(currentStatus);
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
    } else {
        // lite mode — merge into currentStatus
        if (currentStatus) {
            currentStatus.container = status.container;
            currentStatus.running_models = status.running_models;
            currentStatus.ollama_version = status.ollama_version;
            currentStatus.api_reachable = status.api_reachable;
            currentStatus.project_version = status.project_version;
        } else {
            currentStatus = status;
        }
        updateTopbarBadges(currentStatus);
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

    if (action === 'update' && !confirm('确定要更新 Ollama 到最新版本吗？这将拉取最新镜像并重建容器。')) return;
    if (action === 'stop' && !confirm('确定要停止 Ollama 服务吗？')) return;

    showToast(`正在${name}服务...`, 'info');

    // Disable all control buttons
    document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = true);

    try {
        const data = await api(`/api/service/${action}`, { method: 'POST' });
        showToast(`服务${name}成功`, 'success');

        if (action === 'update' && data.old_version && data.new_version) {
            showToast(`版本更新: ${data.old_version} → ${data.new_version}`, 'success');
        }

        // Refresh status after a delay
        setTimeout(refreshStatus, 2000);
    } catch (err) {
        showToast(`${name}失败: ${err.message}`, 'error');
    } finally {
        document.querySelectorAll('.control-buttons .btn').forEach(b => b.disabled = false);
    }
}

// ── Models Page ─────────────────────────────────────────────────
async function loadModels() {
    try {
        const models = await api('/api/models');
        const container = document.getElementById('modelsContainer');

        if (!models || models.length === 0) {
            container.innerHTML = '<div class="card"><div class="empty-state">暂无模型，点击"拉取模型"下载</div></div>';
            return;
        }

        // Split into cloud and local models
        const cloudModels = models.filter(m => m.name && m.name.includes(':cloud'));
        const localModels = models.filter(m => !m.name || !m.name.includes(':cloud'));

        let html = '';

        // Cloud models section
        if (cloudModels.length > 0) {
            html += `
                <div class="card">
                    <div class="card-header"><h3>☁️ 云端模型 (${cloudModels.length})</h3></div>
                    <div class="table-container">
                        <table>
                            <thead><tr><th>名称</th><th>大小</th><th>修改时间</th><th>操作</th></tr></thead>
                            <tbody>${cloudModels.map(m => `
                                <tr>
                                    <td><strong>${escapeHtml(m.name)}</strong><br><span style="color:var(--text-muted);font-size:11px">${m.family || '云端推理'}</span></td>
                                    <td>${m.size_human}</td>
                                    <td>${formatTime(m.modified_at)}</td>
                                    <td>
                                        <button class="btn btn-sm" onclick="showModelInfo('${escapeAttr(m.name)}')" title="详情">📋</button>
                                        <button class="btn btn-sm btn-danger" onclick="deleteModel('${escapeAttr(m.name)}')" title="删除">🗑</button>
                                    </td>
                                </tr>
                            `).join('')}</tbody>
                        </table>
                    </div>
                </div>`;
        }

        // Local models section
        if (localModels.length > 0) {
            html += `
                <div class="card">
                    <div class="card-header"><h3>💻 本地模型 (${localModels.length})</h3></div>
                    <div class="table-container">
                        <table>
                            <thead><tr><th>名称</th><th>大小</th><th>参数</th><th>量化</th><th>修改时间</th><th>操作</th></tr></thead>
                            <tbody>${localModels.map(m => `
                                <tr>
                                    <td><strong>${escapeHtml(m.name)}</strong><br><span style="color:var(--text-muted);font-size:11px">${m.family || ''}</span></td>
                                    <td>${m.size_human}</td>
                                    <td>${m.parameters || '--'}</td>
                                    <td>${m.quantization || '--'}</td>
                                    <td>${formatTime(m.modified_at)}</td>
                                    <td>
                                        <button class="btn btn-sm" onclick="showModelInfo('${escapeAttr(m.name)}')" title="详情">📋</button>
                                        <button class="btn btn-sm btn-danger" onclick="deleteModel('${escapeAttr(m.name)}')" title="删除">🗑</button>
                                    </td>
                                </tr>
                            `).join('')}</tbody>
                        </table>
                    </div>
                </div>`;
        }

        // Summary
        const totalSize = models.reduce((sum, m) => sum + (m.size || 0), 0);
        const totalSizeHuman = totalSize >= 1073741824 ? `${(totalSize / 1073741824).toFixed(1)} GiB` : `${(totalSize / 1048576).toFixed(1)} MiB`;
        html += `<div style="text-align:right;color:var(--text-muted);font-size:12px;margin-top:4px">共 ${models.length} 个模型，总计 ${totalSizeHuman}</div>`;

        container.innerHTML = html;
    } catch (err) {
        showToast('加载模型列表失败: ' + err.message, 'error');
    }
}

async function showModelInfo(name) {
    try {
        const info = await api(`/api/models/${encodeURIComponent(name)}/info`);
        alert(JSON.stringify(info, null, 2));
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

// translateMarketDescriptions translates model descriptions in one batch call and updates the UI.
// The backend handles caching (cached items return instantly) and batch LLM translation
// (all uncached items are packed into a single JSON prompt → one LLM call).
async function translateMarketDescriptions(models) {
    // Filter models that need translation (non-empty, non-Chinese descriptions)
    const toTranslate = models.filter(m => m.description && m.description.length >= 10 && !containsChineseChar(m.description));
    if (toTranslate.length === 0) return;

    // Show translation progress indicator
    const progressEl = document.createElement('div');
    progressEl.id = 'translateProgress';
    progressEl.style.cssText = 'color:var(--text-muted);font-size:12px;margin-bottom:8px;display:flex;align-items:center;gap:6px;';
    progressEl.innerHTML = `<span class="spinner" style="width:14px;height:14px;border-width:2px;"></span><span>正在翻译 ${toTranslate.length} 条模型描述（批量翻译中）...</span>`;
    const marketGrid = document.querySelector('.market-grid');
    if (marketGrid && marketGrid.parentNode) {
        marketGrid.parentNode.insertBefore(progressEl, marketGrid);
    }

    try {
        // Send all items in one request — backend handles cache lookup + batch LLM call
        const items = toTranslate.map(m => ({ name: m.name, description: m.description }));
        const results = await api('/api/models/search/translate', {
            method: 'POST',
            body: JSON.stringify({ items }),
        });

        if (!results || !Array.isArray(results)) {
            throw new Error('Invalid translation response');
        }

        // Update the DOM for each translated description
        let translatedCount = 0;
        for (const r of results) {
            if (!r.description) continue;
            // Check if the description was actually translated (different from original)
            const original = toTranslate.find(m => m.name === r.name);
            if (original && r.description === original.description) continue;

            const descEl = document.querySelector(`.market-card[data-model="${CSS.escape(r.name)}"] .market-card-desc`);
            if (descEl) {
                descEl.textContent = r.description;
                descEl.classList.add('translated');
                translatedCount++;
            }
        }

        // Show completion summary
        if (progressEl) {
            if (translatedCount > 0) {
                progressEl.innerHTML = `<span>✅ 已翻译 ${translatedCount}/${toTranslate.length} 条模型描述</span>`;
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
    document.getElementById('pullBtn').disabled = false;
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
                    document.getElementById('pullBtn').disabled = false;
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
        const result = await api('/api/optimize', { method: 'POST' });
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
async function loadGPUInfo() {
    try {
        const gpus = await api('/api/gpu');
        const container = document.getElementById('gpuCards');

        if (!gpus || gpus.length === 0) {
            container.innerHTML = '<div class="card"><div class="empty-state">未检测到 GPU</div></div>';
            return;
        }

        container.innerHTML = gpus.map((gpu, i) => {
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
                            '驱动': gpu.driver,
                            'CUDA': gpu.cuda || '--',
                            ...(isUnified ? {'内存架构': '统一内存 (CPU/GPU 共享)'} : {'空闲显存': gpu.mem_free}),
                        })}
                    </div>
                </div>
            `;
        }).join('');
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
