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
    if (statusInterval) clearInterval(statusInterval);
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

// ── State ────────────────────────────────────────────────────────
let logWs = null;
let logStreaming = false;
let statusInterval = null;
let currentStatus = null;

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

    // Load data for the page
    switch (page) {
        case 'dashboard': refreshStatus(); break;
        case 'models': loadModels(); break;
        case 'health': break; // Manual trigger
        case 'logs': loadLogs(); break;
        case 'config': loadConfig(); break;
        case 'gpu': loadGPUInfo(); break;
    }
}

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
async function refreshStatus() {
    try {
        const status = await api('/api/status');
        currentStatus = status;
        updateDashboard(status);
    } catch (err) {
        showToast('无法获取服务状态: ' + err.message, 'error');
    }
}

function updateDashboard(s) {
    // Project version (sidebar badge)
    document.getElementById('projectVersion').textContent = s.project_version || '--';
    // Ollama engine version (sidebar meta)
    document.getElementById('ollamaVersion').textContent = s.ollama_version || '--';

    // API Status
    const apiStatus = document.getElementById('apiStatus');
    apiStatus.className = `status-indicator ${s.api_reachable ? 'online' : 'offline'}`;
    apiStatus.querySelector('span:last-child').textContent = s.api_reachable ? 'API 在线' : 'API 离线';

    // Service Status
    const svcStatus = document.getElementById('serviceStatus');
    const health = s.container.health || s.container.status || 'unknown';
    const statusMap = {
        healthy: { cls: 'healthy', text: '运行中 (healthy)' },
        starting: { cls: 'starting', text: '启动中...' },
        unhealthy: { cls: 'unhealthy', text: '异常' },
        running: { cls: 'healthy', text: '运行中' },
        exited: { cls: 'stopped', text: '已停止' },
        not_found: { cls: 'stopped', text: '未创建' },
    };
    const st = statusMap[health] || { cls: 'stopped', text: health };
    svcStatus.className = `service-status ${st.cls}`;
    svcStatus.querySelector('span:last-child').textContent = st.text;

    // Stats
    document.getElementById('modelCount').textContent = s.models ? s.models.length : 0;
    document.getElementById('runningCount').textContent = s.running_models ? s.running_models.length : 0;
    document.getElementById('diskUsage').textContent = s.disk.use_percent || '--';

    if (s.gpu && s.gpu.length > 0) {
        document.getElementById('gpuUsage').textContent = `${s.gpu[0].mem_used} / ${s.gpu[0].mem_total}`;
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
        const tbody = document.querySelector('#modelsTable tbody');

        if (!models || models.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" class="empty-state">暂无模型，点击"拉取模型"下载</td></tr>';
            return;
        }

        tbody.innerHTML = models.map(m => `
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
        `).join('');
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
            const memTotal = parseFloat(gpu.mem_total) || 1;
            const memUsed = parseFloat(gpu.mem_used) || 0;
            const memPct = ((memUsed / memTotal) * 100).toFixed(1);
            const utilPct = parseFloat(gpu.utilization) || 0;

            return `
                <div class="gpu-card">
                    <h3>🎮 GPU ${gpu.index}: ${escapeHtml(gpu.name)}</h3>
                    <div class="gpu-meter">
                        <div class="gpu-meter-label">
                            <span>显存使用</span>
                            <span>${gpu.mem_used} / ${gpu.mem_total} (${memPct}%)</span>
                        </div>
                        <div class="meter-bar">
                            <div class="meter-fill ${memPct > 90 ? 'warn' : ''}" style="width:${memPct}%"></div>
                        </div>
                    </div>
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
                            '空闲显存': gpu.mem_free,
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

// ── Auto-refresh ────────────────────────────────────────────────
function startAutoRefresh() {
    refreshStatus();
    statusInterval = setInterval(refreshStatus, 10000); // 10s
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
