/* qb-stream 前端逻辑：Vue 3（global build），无构建步骤 */
/* global Vue */
const { createApp, ref, reactive, onMounted, onUnmounted } = Vue;

// 复制到剪贴板：优先 Clipboard API，http 局域网页面（非安全上下文）用 execCommand 兜底
async function copyText(t) {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(t); return true; } catch (e) { /* 落入兜底 */ }
  }
  const ta = document.createElement('textarea');
  ta.value = t;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try { document.execCommand('copy'); } catch (e) { /* 忽略 */ }
  document.body.removeChild(ta);
  return true;
}

createApp({
  setup() {
    const route = reactive({ view: 'torrents', hash: '' });
    const torrents = ref([]);
    const loadingTorrents = ref(false);
    const adding = ref(false);
    const addFile = ref(null);
    const addMagnet = ref('');
    const files = ref([]);
    const torrentName = ref('');
    const loadingFiles = ref(false);
    const settings = ref(null);
    const hasPass = ref(false);
    const saving = ref(false);
    const toasts = ref([]);
    let toastSeq = 0;
    let timer = null;

    // ---------- 基础设施 ----------
    function toast(msg, type) {
      const id = ++toastSeq;
      toasts.value.push({ id, msg, type: type || '' });
      setTimeout(() => {
        const i = toasts.value.findIndex(x => x.id === id);
        if (i >= 0) toasts.value.splice(i, 1);
      }, 2600);
    }

    async function api(path, opts) {
      const res = await fetch(path, opts);
      let body = {};
      try { body = await res.json(); } catch (e) { /* 非 JSON 响应 */ }
      if (!res.ok || body.ok === false) {
        throw new Error(body.error || ('请求失败 (' + res.status + ')'));
      }
      return body;
    }

    // ---------- 路由（hash） ----------
    function parseRoute() {
      const h = location.hash;
      const m = h.match(/^#\/files\/([0-9a-f]{8,64})$/i);
      if (m) {
        // 切换到不同种子时先清空，避免短暂显示上一个种子的文件
        if (route.view !== 'files' || route.hash !== m[1]) {
          files.value = [];
          torrentName.value = '';
        }
        route.view = 'files'; route.hash = m[1];
      }
      else if (h.startsWith('#/settings')) { route.view = 'settings'; route.hash = ''; }
      else { route.view = 'torrents'; route.hash = ''; }
      onRouteEnter();
    }
    function onRouteEnter() {
      if (route.view === 'torrents') loadTorrents();
      if (route.view === 'files') loadFiles();
      if (route.view === 'settings') loadSettings();
    }

    // ---------- 种子列表 ----------
    async function loadTorrents() {
      loadingTorrents.value = true;
      try {
        const data = await api('/api/torrents');
        torrents.value = data.torrents;
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        loadingTorrents.value = false;
      }
    }

    function pickFile(ev) {
      addFile.value = ev.target.files[0] || null;
    }

    async function addTorrent() {
      if (!addFile.value && !addMagnet.value.trim()) {
        toast('请选择 .torrent 文件或填写磁力链接', 'error');
        return;
      }
      adding.value = true;
      try {
        const fd = new FormData();
        if (addFile.value) fd.append('torrent', addFile.value);
        if (addMagnet.value.trim()) fd.append('magnet', addMagnet.value.trim());
        const data = await api('/api/add', { method: 'POST', body: fd });
        toast(data.message || '添加成功');
        addFile.value = null;
        addMagnet.value = '';
        document.querySelectorAll('.file-btn input').forEach(i => { i.value = ''; });
        loadTorrents();
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        adding.value = false;
      }
    }

    function isPaused(t) {
      return /^(paused|stopped)/.test(t.state);
    }

    function barClass(t) {
      if (t.progress >= 1) return 'done';
      if (isPaused(t)) return 'paused';
      return '';
    }

    function chipClass(state) {
      if (/^(downloading|forcedDL|stalledDL)/.test(state)) return 'st-blue';
      if (/^(uploading|forcedUP|stalledUP)/.test(state)) return 'st-green';
      if (/^(paused|stopped)/.test(state)) return 'st-gray';
      if (/^(error|missingFiles)/.test(state)) return 'st-red';
      if (/^metaDL|^forcedMetaDL/.test(state)) return 'st-purple';
      if (/^checking|^moving/.test(state)) return 'st-cyan';
      if (/^queued/.test(state)) return 'st-amber';
      return 'st-gray';
    }

    async function doAction(t, op) {
      if (op === 'delete' && !confirm('确认删除该种子任务？（保留已下载文件）')) return;
      if (op === 'deletefiles' && !confirm('确认删除该种子并同时删除所有已下载文件？\n此操作不可恢复！')) return;
      try {
        await api('/api/action', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hash: t.hash, op }),
        });
        const msg = { start: '已开始', stop: '已暂停', delete: '已删除（文件保留）', deletefiles: '种子与文件已删除' };
        toast(msg[op] || '操作完成');
        loadTorrents();
      } catch (e) {
        toast(e.message, 'error');
      }
    }

    function playlistURL(hash) {
      return location.origin + '/playlist?hash=' + hash;
    }

    // 种子卡片传 torrent 对象，文件页直接传 hash 字符串，两种都兼容
    async function copyPlaylist(arg) {
      const hash = typeof arg === 'object' && arg ? arg.hash : arg;
      await copyText(playlistURL(hash));
      toast('剧集列表地址已复制，粘贴到 VLC/SenPlayer 即可选集');
    }

    function gotoFiles(hash) {
      location.hash = '#/files/' + hash;
    }

    // ---------- 文件列表 ----------
    async function loadFiles() {
      loadingFiles.value = true;
      try {
        const data = await api('/api/files?hash=' + encodeURIComponent(route.hash));
        files.value = data.files;
        torrentName.value = data.torrent_name;
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        loadingFiles.value = false;
      }
    }

    function shortName(name) {
      const i = name.lastIndexOf('/');
      return i >= 0 ? name.slice(i + 1) : name;
    }

    function streamURL(f) {
      return location.origin + '/stream?hash=' + route.hash + '&file=' + f.index;
    }

    async function copyStream(f) {
      await copyText(streamURL(f));
      toast('播放地址已复制，粘贴到 VLC/SenPlayer 等播放器打开');
    }

    // ---------- 设置 ----------
    async function loadSettings() {
      try {
        const data = await api('/api/settings');
        settings.value = data.config;
        hasPass.value = data.has_pass;
      } catch (e) {
        toast(e.message, 'error');
      }
    }

    async function saveSettings() {
      saving.value = true;
      try {
        const c = settings.value;
        await api('/api/settings', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            qb_url: c.qb_url,
            qb_user: c.qb_user,
            qb_pass: c.qb_pass || '',
            host: c.host,
            port: Number(c.port),
            poll: c.poll,
            max_wait: c.max_wait,
            chunk: Number(c.chunk),
            prio_lead: Number(c.prio_lead),
            prio_interval: c.prio_interval,
          }),
        });
        settings.value.qb_pass = '';
        toast('已保存：除监听地址/端口需重启外，其余即时生效');
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        saving.value = false;
      }
    }

    // ---------- 工具 ----------
    function fmtSize(n) {
      if (n == null) return '';
      const unit = 1024;
      if (n < unit) return n + ' B';
      let div = unit, exp = 0;
      for (let m = n / unit; m >= unit; m /= unit) { div *= unit; exp++; }
      return (n / div).toFixed(2) + ' ' + 'KMGTPE'[exp] + 'B';
    }

    // qB 的 eta：<=0 或 >=8640000 表示未知/无限
    function fmtETA(sec) {
      if (sec == null || sec <= 0 || sec >= 8640000) return '—';
      const d = Math.floor(sec / 86400);
      const h = Math.floor((sec % 86400) / 3600);
      const m = Math.floor((sec % 3600) / 60);
      const s = sec % 60;
      if (d > 0) return d + ' 天' + (h ? ' ' + h + ' 小时' : '');
      if (h > 0) return h + ' 小时' + (m ? ' ' + m + ' 分' : '');
      if (m > 0) return m + ' 分' + (s && m < 10 ? ' ' + s + ' 秒' : '');
      return s + ' 秒';
    }

    function fmtRatio(r) {
      if (r == null || r < 0) return '—';
      return r.toFixed(2);
    }

    // 人数显示「已连接（swarm 总数）」，与 qB 界面口径一致
    function peerText(connected, total) {
      if (total > 0 && total !== connected) return connected + '（' + total + '）';
      return String(connected);
    }

    // pendingText：完成时间为 0/负数时显示「未完成」；添加时间异常显示 —
    function fmtTime(ts, pendingText) {
      if (ts == null || ts <= 0) return pendingText || '—';
      const d = new Date(ts * 1000);
      const pad = n => String(n).padStart(2, '0');
      return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
        ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    }

    // tracker 只显示 host+路径，完整地址放在 title 悬停查看
    function shortTracker(u) {
      let s = u;
      try {
        const x = new URL(u);
        s = x.host + (x.pathname && x.pathname !== '/' ? x.pathname : '');
      } catch (e) { /* 非标准 URL，原样截断 */ }
      return s.length > 52 ? s.slice(0, 51) + '…' : s;
    }

    onMounted(() => {
      window.addEventListener('hashchange', parseRoute);
      parseRoute();
      timer = setInterval(() => {
        if (route.view === 'torrents' && document.visibilityState === 'visible') loadTorrents();
      }, 5000);
    });
    onUnmounted(() => clearInterval(timer));

    return {
      route, torrents, loadingTorrents, adding, addFile, addMagnet,
      files, torrentName, loadingFiles, settings, hasPass, saving, toasts,
      loadTorrents, pickFile, addTorrent, isPaused, barClass, chipClass,
      doAction, copyPlaylist, gotoFiles, copyStream, shortName,
      saveSettings, fmtSize, fmtETA, fmtRatio, fmtTime, peerText, shortTracker,
    };
  },
}).mount('#app');
