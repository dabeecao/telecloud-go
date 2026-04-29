function cloudApp(initialIsLoggedIn, initialMaxUploadSizeMB, webdavEnabled = false, webdavUser = '', webdavPassword = '', uploadAPIEnabled = false, uploadAPIKey = '') {
    return {
        isLoggedIn: initialIsLoggedIn,
        maxUploadSizeMB: initialMaxUploadSizeMB,
        webdavEnabled: webdavEnabled,
        webdavUser: webdavUser,
        webdavPassword: webdavPassword,
        uploadAPIEnabled: uploadAPIEnabled,
        uploadAPIKey: uploadAPIKey,
        showAPIKey: false,
        currentTab: 'files',
        updateAvailable: false,
        changelog: [],
        latestReleaseUrl: '',
        username: '',
        password: '', 
        confirmPassword: '',
        settingsForm: { oldPassword: '', newPassword: '', confirmPassword: '' },
        isLoading: false, 
        isRefreshing: false,
        isPreparingDownload: false,
        ws: null,
        lang: TeleCloud.lang,
        t(key, params) { return TeleCloud.t(key, params, this.lang); },
        handleCommonError(errorStr, defaultKey) {
            if (!errorStr) return this.t(defaultKey);
            const errorKey = 'err_' + errorStr.toLowerCase().replace(/ /g, '_');
            const translated = this.t(errorKey);
            return (translated !== errorKey) ? translated : (this.t(defaultKey) + ' (' + errorStr + ')');
        },
        async setupAdmin() {
            if (this.password !== this.confirmPassword) {
                this.showToast(this.t('toast_pass_mismatch'), 'error');
                return;
            }
            let fd = new FormData();
            fd.append('username', this.username);
            fd.append('password', this.password);
            try {
                let res = await fetch('/setup', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) window.location.href = '/';
                else {
                    let d = await res.json();
                    this.showToast(this.handleCommonError(d.error, 'setup_failed'), 'error');
                }
            } catch (e) {
                this.showToast(this.t('setup_error'), 'error');
            }
        },
        async changePassword() {
            if (this.settingsForm.newPassword !== this.settingsForm.confirmPassword) {
                this.showToast(this.t('toast_pass_mismatch'), 'error');
                return;
            }
            let fd = new FormData();
            fd.append('old_password', this.settingsForm.oldPassword);
            fd.append('new_password', this.settingsForm.newPassword);
            try {
                let res = await fetch('/api/settings/password', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) {
                    this.showToast(this.t('toast_pass_changed'), 'success');
                    this.settingsForm = { oldPassword: '', newPassword: '', confirmPassword: '' };
                } else {
                    let d = await res.json();
                    this.showToast(this.handleCommonError(d.error, 'status_error'), 'error');
                }
            } catch (e) {
                this.showToast(this.t('conn_error'), 'error');
            }
        },
        async toggleWebDAV() {
            let newState = !this.webdavEnabled;
            let fd = new FormData();
            fd.append('enabled', newState);
            try {
                let res = await fetch('/api/settings/webdav', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) {
                    this.webdavEnabled = newState;
                } else {
                    this.showToast(this.t('webdav_toggle_error'), 'error');
                }
            } catch(e) {
                this.showToast(this.t('status_error'), 'error');
            }
        },
        async toggleUploadAPI() {
            let newState = !this.uploadAPIEnabled;
            let fd = new FormData();
            fd.append('enabled', newState);
            try {
                let res = await fetch('/api/settings/upload-api', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) {
                    this.uploadAPIEnabled = newState;
                    // Auto-generate a key if enabling and no key exists
                    if (newState && !this.uploadAPIKey) {
                        await this.regenerateAPIKey();
                    }
                } else {
                    this.showToast(this.t('api_toggle_error'), 'error');
                }
            } catch(e) {
                this.showToast(this.t('status_error'), 'error');
            }
        },
        async regenerateAPIKey() {
            try {
                let res = await fetch('/api/settings/upload-api/regenerate-key', { method: 'POST', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) {
                    let d = await res.json();
                    this.uploadAPIKey = d.api_key;
                    this.showAPIKey = true;
                    this.showToast(this.t('api_key_regenerated'), 'success');
                } else {
                    this.showToast(this.t('api_toggle_error'), 'error');
                }
            } catch(e) {
                this.showToast(this.t('status_error'), 'error');
            }
        },
        async deleteAPIKey() {
            const confirmed = await this.customConfirm(this.t('api_key_delete_title'), this.t('api_key_delete_msg'), true);
            if (!confirmed) return;
            try {
                let res = await fetch('/api/settings/upload-api/key', { method: 'DELETE', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                if (res.ok) {
                    this.uploadAPIKey = '';
                    this.showAPIKey = false;
                    this.showToast(this.t('api_key_deleted'), 'success');
                }
            } catch(e) {
                this.showToast(this.t('status_error'), 'error');
            }
        },
        toggleLang() { 
            this.lang = TeleCloud.toggleLang();
        },
        formatBytes(b, d) { return TeleCloud.formatBytes(b, d); },
        formatDate(d) { return TeleCloud.formatDate(d, this.lang); },
        getFileTypeData(f) { return TeleCloud.getFileTypeData(f); },
        parseMarkdown(t) { return TeleCloud.parseMarkdown(t); },

        startDownload(fileId) {
            this.isPreparingDownload = true;
            document.cookie = "dl_started=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;";
            const iframe = document.createElement('iframe');
            iframe.style.display = 'none';
            iframe.src = `/download/${fileId}`;
            document.body.appendChild(iframe);
            let checkCookie = setInterval(() => {
                if (document.cookie.includes('dl_started=1')) {
                    clearInterval(checkCookie);
                    this.isPreparingDownload = false;
                    this.showToast(this.t('toast_dl_started'), 'success');
                    document.cookie = "dl_started=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;";
                    setTimeout(() => iframe.remove(), 2000); 
                }
            }, 500);
            setTimeout(() => {
                if (this.isPreparingDownload) {
                    clearInterval(checkCookie);
                    this.isPreparingDownload = false;
                    iframe.remove();
                    this.showToast(this.t('toast_tg_timeout'), 'error');
                }
            }, 15000);
        },
        async downloadSelectedBatch() {
            const fileIdsToDownload = this.selectedIds.filter(id => {
                const f = this.files.find(file => file.id === id);
                return f && !f.is_folder;
            });
            if (fileIdsToDownload.length === 0) {
                this.showToast(this.t('toast_only_files'), 'error');
                return;
            }
            if (this.selectedIds.length !== fileIdsToDownload.length) {
                this.showToast(this.t('toast_skipped_folders'));
            }
            this.showToast(this.t('toast_preparing_dl', {n: fileIdsToDownload.length}));
            this.isPreparingDownload = true;
            document.cookie = "dl_started=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;";
            for (let i = 0; i < fileIdsToDownload.length; i++) {
                const fileId = fileIdsToDownload[i];
                const iframe = document.createElement('iframe');
                iframe.style.display = 'none';
                iframe.src = `/download/${fileId}`;
                document.body.appendChild(iframe);
                if (i === 0) {
                    let checkCookie = setInterval(() => {
                        if (document.cookie.includes('dl_started=1')) {
                            clearInterval(checkCookie);
                            this.isPreparingDownload = false;
                            document.cookie = "dl_started=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;";
                        }
                    }, 500);
                    setTimeout(() => { if (this.isPreparingDownload) this.isPreparingDownload = false; }, 15000);
                }
                setTimeout(() => iframe.remove(), 20000);
                if (i < fileIdsToDownload.length - 1) {
                    await new Promise(resolve => setTimeout(resolve, 1500));
                }
            }
            this.selectedIds = [];
        },
        files: [], 
        searchQuery: '',
        currentPage: 1,
        itemsPerPage: 15,
        get filteredFiles() {
            if (this.searchQuery.trim() === '') return this.files;
            const query = this.searchQuery.toLowerCase();
            return this.files.filter(f => f.filename.toLowerCase().includes(query));
        },
        get totalPages() {
            return Math.ceil(this.filteredFiles.length / this.itemsPerPage) || 1;
        },
        get displayedFiles() {
            const start = (this.currentPage - 1) * this.itemsPerPage;
            const end = start + this.itemsPerPage;
            return this.filteredFiles.slice(start, end);
        },
        currentPath: '/', 
        openMenuId: null,
        selectedIds: [], 
        clipboard: { action: null, ids: [] },
        dragOver: false, 
        uploadModal: false,
        uploadDragOver: false,
        uploadQueue: [], 
        isQueueMinimized: false,
        get isAllUploadsDone() {
            if (this.uploadQueue.length === 0) return false;
            return this.uploadQueue.every(t => t.progress === 100 || t.isCancelled);
        },
        cancelUpload(taskId) {
            let task = this.uploadQueue.find(t => t.id === taskId);
            if (task && task.progress < 100) {
                task.isCancelled = true;
                task.statusText = this.t('cancelled');
                
                // Notify backend to cancel the task and clean up temporary files
                let fd = new FormData();
                fd.append('task_id', taskId);
                fd.append('filename', task.name);
                fetch('/api/cancel_upload', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } }).catch(e => console.error("Cancel failed:", e));
            }
        },
        toastModal: { show: false, message: '', type: 'success' },
        toastTimeout: null,
        plyrInstance: null,
        fileInfoModal: { show: false, file: null, typeName: '', svgIcon: '', bgColor: '', isMedia: false, mediaHtml: '' },
        modal: { show: false, type: 'alert', title: '', message: '', input: '', resolve: null, isDanger: false },
        contextMenu: { show: false, x: 0, y: 0, file: null },
        init() { 
            if (this.isLoggedIn) {
                this.fetchFiles(false);
                this.checkUpdate();
                this.initWebSocket();
                
                // Add hasError to existing tasks if any (for persistence, though queue is currently memory-only)
                this.uploadQueue.forEach(t => { if(t.hasError === undefined) t.hasError = false; });
            }
        },
        async checkUpdate() {
            const compareVersions = (v1, v2) => {
                const p1 = (v1 || 'v0.0.0').replace(/^v/, '').split('.').map(Number);
                const p2 = (v2 || 'v0.0.0').replace(/^v/, '').split('.').map(Number);
                for (let i = 0; i < Math.max(p1.length, p2.length); i++) {
                    const n1 = p1[i] || 0;
                    const n2 = p2[i] || 0;
                    if (n1 > n2) return 1;
                    if (n1 < n2) return -1;
                }
                return 0;
            };

            try {
                const res = await fetch('https://api.github.com/repos/dabeecao/telecloud-go/releases');
                if (res.ok) {
                    const releases = await res.json();
                    if (releases && releases.length > 0) {
                        const latest = releases[0];
                        const latestVersion = latest.tag_name;
                        const currentVersion = TeleCloud.version || 'v1.0.0';
                        
                        if (latestVersion && compareVersions(latestVersion, currentVersion) === 1) {
                            this.updateAvailable = true;
                            this.latestReleaseUrl = latest.html_url;
                            this.changelog = releases.slice(0, 5).map(r => ({
                                tag: r.tag_name,
                                name: r.name,
                                body: r.body,
                                url: r.html_url,
                                date: r.published_at
                            }));

                            const dismissedDate = localStorage.getItem('tc_update_dismissed');
                            const today = new Date().toDateString();
                            
                            if (dismissedDate !== today) {
                                const choice = await this.showUIModal('update', this.t('update_title'), this.t('update_msg') + ` (${latestVersion})`);
                                if (choice === 'confirm') {
                                    this.currentTab = 'changelog';
                                } else if (choice === 'dismiss_today') {
                                    localStorage.setItem('tc_update_dismissed', today);
                                }
                            }
                        }
                    }
                }
            } catch (e) { console.error('Failed to check for updates', e); }
        },
        initWebSocket() {
            if (this.ws) return;
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = `${protocol}//${window.location.host}/api/ws`;
            this.ws = new WebSocket(wsUrl);
            
            this.ws.onmessage = (event) => {
                try {
                    const data = JSON.parse(event.data);
                    let task = this.uploadQueue.find(t => t.id === data.task_id);
                    if (task) {
                        if (data.status === 'uploading_to_server') {
                            // Let the client handle the first 50% of progress to avoid jumping during parallel uploads
                            if (!task.hasError) {
                                task.statusText = data.message || task.statusText;
                            }
                        } else if (data.message === 'waiting_slot') {
                            task.statusText = this.t('waiting_slot');
                            task.hasError = false;
                            task.progress = 50;
                        } else if (data.status === 'telegram' || data.status === 'done') {
                            task.progress = 50 + Math.round(data.percent / 2);
                            task.hasError = false;
                        }
                        
                        if (data.status === 'done') {
                            task.progress = 100;
                            task.statusText = this.t('done');
                            task.hasError = false;
                            this.fetchFiles(true);
                        } else if (data.status === 'error') {
                            const errorMsg = data.message;
                            const translated = this.t(errorMsg);
                            task.statusText = this.t('status_error') + ': ' + (translated !== errorMsg ? translated : errorMsg);
                            task.hasError = true;
                        } else if (data.status === 'telegram') {
                            task.statusText = this.t('syncing_tg');
                            task.hasError = false;
                        }
                    }
                } catch (e) {
                    console.error('WS message error:', e);
                }
            };

            this.ws.onclose = () => {
                this.ws = null;
                // Reconnect after 5 seconds
                setTimeout(() => this.initWebSocket(), 5000);
            };

            this.ws.onerror = (err) => {
                console.error('WS error:', err);
                this.ws.close();
            };
        },
        showUIModal(type, title, message = '', defaultValue = '', isDanger = false) {
            return new Promise((resolve) => {
                this.modal = { show: true, type, title, message, input: defaultValue, resolve, isDanger };
                if (type === 'prompt') {
                    setTimeout(() => { if (this.$refs.modalInput) this.$refs.modalInput.focus(); }, 100);
                }
            });
        },
        closeUIModal(result) {
            if (this.modal.resolve) this.modal.resolve(result);
            this.modal.show = false;
        },
        async customPrompt(title, defaultValue = '') { return await this.showUIModal('prompt', title, '', defaultValue); },
        async customConfirm(title, message, isDanger = false) { return await this.showUIModal('confirm', title, message, '', isDanger); },
        async customAlert(title, message) { return await this.showUIModal('alert', title, message); },
        openContextMenu(e, file) {
            if (!file) return; 
            this.contextMenu.file = file;
            let x = e.clientX; let y = e.clientY;
            if (window.innerWidth - x < 210) x = window.innerWidth - 210;
            if (window.innerHeight - y < 250) y = window.innerHeight - 250;
            this.contextMenu.x = x;
            this.contextMenu.y = y;
            this.contextMenu.show = true;
        },
        closeContextMenu() { this.contextMenu.show = false; },
        async login() {
            const fd = new FormData(); 
            fd.append('username', this.username);
            fd.append('password', this.password);
            const res = await fetch('/login', { method: 'POST', body: fd });
            if (res.ok) { 
                window.location.href = '/'; 
            } else {
                const data = await res.json();
                this.showToast(this.handleCommonError(data.error, 'toast_login_fail'), 'error');
            }
        },
        async logout() { await fetch('/logout', { method: 'POST', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } }); window.location.href = '/login'; },
        getBreadcrumbs() { return this.currentPath === '/' ? [] : this.currentPath.split('/').filter(Boolean); },
        navigateToFolder(folderName) { this.currentPath = this.currentPath === '/' ? '/' + folderName : this.currentPath + '/' + folderName; this.fetchFiles(); },
        navigateToIndex(index) { this.currentPath = '/' + this.getBreadcrumbs().slice(0, index + 1).join('/'); this.fetchFiles(); },
        navigateTo(path) { this.currentPath = path; this.fetchFiles(); },
        async fetchFiles(silentLoad = false) {
            if (!silentLoad && (!this.files || this.files.length === 0)) { this.isLoading = true; } else { this.isRefreshing = true; }
            try {
                const res = await fetch(`/api/files?path=${encodeURIComponent(this.currentPath)}`);
                const data = await res.json();
                this.files = data.files || [];
                this.selectedIds = this.selectedIds.filter(id => this.files.some(f => f.id === id));
                if (!silentLoad) { this.searchQuery = ''; this.currentPage = 1; } else { if (this.currentPage > this.totalPages) this.currentPage = Math.max(1, this.totalPages); }
            } catch (e) { console.error('Fetch error', e); } finally { this.isLoading = false; this.isRefreshing = false; }
        },
        async createNewFolder() {
            const name = await this.customPrompt(this.t('new_folder_title'), "");
            if (!name || name.trim() === "") return;
            const tempId = 'temp_' + Date.now();
            this.files.unshift({ id: tempId, filename: name.trim(), is_folder: true, size: 0, created_at: new Date().toISOString() });
            const fd = new FormData(); fd.append('name', name.trim()); fd.append('path', this.currentPath);
            await fetch('/api/folders', { method: 'POST', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
            this.fetchFiles(true); 
            this.showToast(this.t('toast_created', {n: name.trim()}));
        },
        copyToClipboard(action, idsArray) { this.clipboard = { action: action, ids: [...idsArray] }; this.selectedIds = []; },
        async executePaste() {
            if (this.clipboard.ids.length === 0) return;
            if (this.clipboard.action === 'move') this.files = this.files.filter(f => !this.clipboard.ids.includes(f.id));
            await fetch('/api/actions/paste', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': TeleCloud.getCsrfToken() }, body: JSON.stringify({ action: this.clipboard.action, item_ids: this.clipboard.ids, destination: this.currentPath }) });
            this.clipboard = { action: null, ids: [] }; 
            this.fetchFiles(true);
            this.showToast(this.t('toast_pasted'));
        },
        async deleteBatch() {
            const confirmed = await this.customConfirm(this.t('delete_confirm_title'), this.t('delete_batch_msg', {n: this.selectedIds.length}), true);
            if (!confirmed) return;
            const idsToDelete = [...this.selectedIds];
            this.files = this.files.filter(f => !idsToDelete.includes(f.id));
            this.selectedIds = [];
            for (let id of idsToDelete) await fetch(`/api/files/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
            this.fetchFiles(true);
            this.showToast(this.t('toast_deleted', {n: idsToDelete.length}), 'success');
        },
        handleDrop(e) { this.dragOver = false; this.uploadFiles(Array.from(e.dataTransfer.files)); },
        handleUploadModalSelect(e) { this.uploadFiles(Array.from(e.target.files)); e.target.value = ''; this.uploadModal = false; },
        handleUploadModalDrop(e) { this.uploadDragOver = false; this.uploadModal = false; this.uploadFiles(Array.from(e.dataTransfer.files)); },
        async uploadFiles(fileList) {
            const maxSizeBytes = this.maxUploadSizeMB * 1024 * 1024;
            const newTasks = [];
            
            for (let i = 0; i < fileList.length; i++) {
                const file = fileList[i];
                const taskId = 'task_' + Math.random().toString(36).substring(2, 11) + '_' + Date.now();
                const task = { 
                    id: taskId, 
                    name: file.name, 
                    progress: 0, 
                    statusText: this.t('waiting_slot'), 
                    isCancelled: false,
                    file: file,
                    hasError: false
                };
                
                if (this.maxUploadSizeMB > 0 && file.size > maxSizeBytes) {
                    task.statusText = this.t('status_error') + ': ' + this.t('file_too_large_title');
                    task.hasError = true;
                    task.progress = 0;
                }
                
                newTasks.push(task);
            }
            
            // Add all to queue at once for better performance
            this.uploadQueue.unshift(...newTasks);
            
            const CONCURRENCY = 3;
            const activeQueue = newTasks.filter(t => !t.hasError);

            const processQueue = async () => {
                while (activeQueue.length > 0) {
                    const task = activeQueue.shift();
                    if (task.isCancelled) continue;
                    
                    task.statusText = this.t('preparing_upload');
                    await this.uploadSingleFile(task.file, task.id);
                }
            };

            const workers = [];
            for (let i = 0; i < Math.min(CONCURRENCY, activeQueue.length); i++) {
                workers.push(processQueue());
            }
            await Promise.all(workers);
        },

        async retryUpload(taskId) {
            const task = this.uploadQueue.find(t => t.id === taskId);
            if (!task || !task.file) return;
            
            task.progress = 0;
            task.statusText = this.t('preparing_upload');
            task.isCancelled = false;
            
            await this.uploadSingleFile(task.file, taskId);
        },

        async uploadSingleFile(file, taskId) {
            const CHUNK_SIZE = 10 * 1024 * 1024;
            const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE));
            let hasError = false;
            let uploadedChunks = 0;

            // Worker pool for parallel chunks
            const CHUNK_CONCURRENCY = 3;
            const chunkQueue = Array.from({ length: totalChunks }, (_, i) => i);
            
            const uploadWorker = async () => {
                while (chunkQueue.length > 0 && !hasError) {
                    const chunkIndex = chunkQueue.shift();
                    let taskObj = this.uploadQueue.find(t => t.id === taskId);
                    if (taskObj && taskObj.isCancelled) break;

                    const start = chunkIndex * CHUNK_SIZE;
                    const end = Math.min(start + CHUNK_SIZE, file.size);
                    const chunk = file.slice(start, end);
                    const fd = new FormData(); 
                    fd.append('file', chunk); fd.append('filename', file.name); fd.append('path', this.currentPath); 
                    fd.append('task_id', taskId); fd.append('chunk_index', chunkIndex); fd.append('total_chunks', totalChunks);

                    let retries = 3;
                    let success = false;
                    while (retries > 0 && !success) {
                        try {
                            let task = this.uploadQueue.find(t => t.id === taskId);
                            if (task && !task.statusText.includes(this.t('status_error'))) {
                                task.statusText = `${this.t('pushing')} (${uploadedChunks + 1}/${totalChunks})... ${retries < 3 ? '(' + this.t('retry') + ' ' + (3 - retries) + ')' : ''}`;
                            }

                            const response = await fetch('/api/upload', { 
                                method: 'POST', 
                                body: fd, 
                                headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } 
                            });
                            
                            if (!response.ok) throw new Error("Upload failed");
                            const result = await response.json();
                            
                            uploadedChunks++;
                            if (task) task.progress = Math.round((uploadedChunks / totalChunks) * 50);
                            
                            if (result.status === "processing_telegram") {
                                if (task) task.statusText = this.t('syncing_tg');
                            }
                            success = true;
                        } catch (err) { 
                            retries--;
                            console.error(`Upload chunk ${chunkIndex} error (retries left: ${retries}):`, err);
                            if (retries === 0) {
                                let task = this.uploadQueue.find(t => t.id === taskId); 
                                if(task) {
                                    task.statusText = this.t('conn_error');
                                    task.hasError = true;
                                }
                                hasError = true;
                            } else {
                                await new Promise(r => setTimeout(r, 2000)); // Wait before retry
                            }
                        }
                    }
                }
            };

            const workers = [];
            for (let i = 0; i < Math.min(CHUNK_CONCURRENCY, totalChunks); i++) {
                workers.push(uploadWorker());
            }
            await Promise.all(workers);
        },
        async toggleShare(file) {
            const targetFile = this.files.find(f => f.id === file.id);
            if (targetFile) {
                if (targetFile.share_token) {
                    targetFile.share_token = null;
                    await fetch(`/api/files/${file.id}/share`, { method: 'DELETE', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                    this.showToast(this.t('toast_revoked'), 'success');
                } else {
                    targetFile.share_token = 'loading...';
                    const res = await fetch(`/api/files/${file.id}/share`, { method: 'POST', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } });
                    const data = await res.json();
                    targetFile.share_token = data.share_token;
                    targetFile.direct_token = data.direct_token; 
                    this.copyShareLink(targetFile, 'regular');
                }
            }
        },
        async copyShareLink(file, type = 'regular') {
            const link = type === 'direct' ? `${window.location.origin}/dl/${file.direct_token}` : `${window.location.origin}/s/${file.share_token}`;
            try {
                await TeleCloud.copyToClipboard(link);
                const label = type === 'direct' ? this.t('link_direct') : this.t('link_share');
                this.showToast(this.t('toast_copied', {t: label}));
            } catch (err) {
                console.error('Failed to copy link:', err);
            }
        },
        showToast(msg, type = 'success') {
            this.toastModal = { show: true, message: msg, type: type };
            if (this.toastTimeout) clearTimeout(this.toastTimeout);
            this.toastTimeout = setTimeout(() => { this.toastModal.show = false; }, 3500);
        },
        async deleteFile(id) { 
            const confirmed = await this.customConfirm(this.t('delete_confirm_title'), this.t('delete_confirm_msg'), true); 
            if (!confirmed) return; 
            this.files = this.files.filter(f => f.id !== id);
            await fetch(`/api/files/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } }); 
            this.fetchFiles(true);
            this.showToast(this.t('delete'), 'success'); 
        },
        async renameFile(file) { 
            const newName = await this.customPrompt(this.t('rename_title'), file.filename); 
            if (!newName || newName === file.filename) return; 
            const targetFile = this.files.find(f => f.id === file.id);
            if(targetFile) targetFile.filename = newName;
            const fd = new FormData(); fd.append('new_name', newName); 
            await fetch(`/api/files/${file.id}/rename`, { method: 'PUT', body: fd, headers: { 'X-CSRF-Token': TeleCloud.getCsrfToken() } }); 
            this.fetchFiles(true); 
            this.showToast(this.t('toast_renamed')); 
        },
        closeFileInfoModal() {
            this.fileInfoModal.show = false;
            if (this.plyrInstance) { this.plyrInstance.destroy(); this.plyrInstance = null; }
            setTimeout(() => { if (!this.fileInfoModal.show) { this.fileInfoModal.isMedia = false; this.fileInfoModal.mediaHtml = ''; } }, 300);
        },
        showFileInfo(file) {
            if (file.is_folder) return;
            const typeData = this.getFileTypeData(file.filename);
            const ext = file.filename.split('.').pop().toLowerCase();
            const imgExts = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp'];
            const videoExts = ['mp4', 'webm', 'ogg', 'mov'];
            const audioExts = ['mp3', 'wav', 'ogg', 'm4a', 'flac'];
            const mimeTypes = { 'mp4': 'video/mp4', 'webm': 'video/webm', 'ogg': 'video/ogg', 'mov': 'video/mp4', 'mp3': 'audio/mpeg', 'wav': 'audio/wav', 'flac': 'audio/flac', 'm4a': 'audio/mp4' };
            let isMedia = false; let mediaHtml = ''; let playerTarget = null;
            const streamUrl = `/api/files/${file.id}/stream`;
            const thumbUrl = `/api/files/${file.id}/thumb`;
            if (imgExts.includes(ext)) { mediaHtml = '<img src="' + streamUrl + '" alt="' + file.filename + '" class="max-h-64 object-contain rounded-[1rem] w-full shadow-md">'; isMedia = true; } 
            else if (videoExts.includes(ext)) {
                const typeAttr = mimeTypes[ext] || 'video/mp4';
                mediaHtml = '<div class="w-full relative z-20 rounded-[1rem] bg-black shadow-md"><video id="index-tele-player" playsinline controls preload="none" ' + (file.has_thumb ? 'data-poster="' + thumbUrl + '"' : '') + '><source src="' + streamUrl + '" type="' + typeAttr + '"></video></div>';
                isMedia = true; playerTarget = '#index-tele-player';
            } else if (audioExts.includes(ext)) {
                const typeAttr = mimeTypes[ext] || 'audio/mpeg';
                mediaHtml = '<div class="w-full relative z-20 rounded-[1rem] p-2 sm:p-4 bg-slate-100 dark:bg-slate-800/50 shadow-inner">' + (file.has_thumb ? '<img src="' + thumbUrl + '" class="w-32 h-32 mx-auto rounded-2xl mb-4 object-cover shadow-md">' : '<div class="w-32 h-32 mx-auto rounded-2xl mb-4 flex items-center justify-center bg-white dark:bg-slate-700 shadow-sm"><i class="fa-solid fa-music text-5xl text-slate-300 dark:text-slate-500"></i></div>') + '<audio id="index-tele-player" controls preload="none"><source src="' + streamUrl + '" type="' + typeAttr + '"></audio></div>';
                isMedia = true; playerTarget = '#index-tele-player';
            }
            this.fileInfoModal = { show: true, file: file, typeName: typeData.n, svgIcon: typeData.i, bgColor: typeData.c, isMedia: isMedia, mediaHtml: mediaHtml };
            if (playerTarget) { setTimeout(() => { if (this.plyrInstance) this.plyrInstance.destroy(); this.plyrInstance = new Plyr(playerTarget, { ratio: '16:9', controls: ['play-large', 'play', 'progress', 'current-time', 'duration', 'mute', 'settings', 'fullscreen'], settings: ['speed'] }); }, 50); }
        }
    }
}