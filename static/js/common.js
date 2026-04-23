/**
 * TeleCloud Common Utilities & I18n
 */

const TeleCloud = {
    lang: localStorage.getItem('lang') || (navigator.language.startsWith('vi') ? 'vi' : 'en'),

    translations: {
        vi: {
            home: 'Quản lý tệp',
            upload: 'Tải lên',
            new_folder: 'Thư mục mới',
            logout: 'Đăng xuất',
            login: 'Đăng nhập',
            refresh: 'Làm mới',
            search_placeholder: 'Tìm kiếm tập tin hoặc thư mục...',
            empty_folder: 'Thư mục này đang trống',
            no_results: 'Không tìm thấy kết quả phù hợp cho',
            clear_search: 'Xóa tìm kiếm',
            max_limit: 'Giới hạn tải lên',
            items_count: 'mục trong thư mục này',
            lang_vi: 'Tiếng Việt',
            lang_en: 'English',
            unlimited: 'Không giới hạn',
            online: 'Hệ thống trực tuyến',
            download: 'Tải xuống',
            rename: 'Đổi tên',
            move: 'Di chuyển',
            copy: 'Sao chép',
            delete: 'Xóa',
            share_link: 'Tạo Link Chia sẻ',
            revoke_link: 'Thu hồi Link',
            copy_share_link: 'Copy Link Chia sẻ',
            copy_direct_link: 'Copy Link tải Direct',
            file_info: 'Thông tin tệp tin',
            size: 'Kích thước',
            date: 'Ngày tải lên',
            status: 'Trạng thái',
            public: 'Công khai',
            private: 'Riêng tư',
            cancel: 'Hủy',
            confirm: 'Xác nhận',
            close: 'Đóng',
            access_code: 'Nhập mật khẩu truy cập...',
            access_system: 'Truy cập hệ thống',
            connecting_tg: 'Đang kết nối tới Telegram...',
            wait_msg: 'Vui lòng không đóng trang này',
            success: 'Thành công!',
            failure: 'Thất bại!',
            preparing_upload: 'Đang chuẩn bị...',
            syncing_tg: 'Đang đồng bộ với Telegram...',
            done: 'Hoàn tất!',
            cancelled: 'Đã hủy',
            capcity: 'Dung lượng còn lại',
            upload_progress: 'Tiến trình tải lên',
            upload_done: 'Đã tải lên xong',
            max_size: 'Kích thước tối đa mỗi tệp',
            drop_files: 'Thả file vào đây để tải lên',
            drop_modal: 'Kéo thả tệp vào đây',
            or_click: 'Hoặc click vào nút dưới đây để duyệt tệp',
            select_files: 'Chọn tệp từ thiết bị',
            name_placeholder: 'Tên Thư mục / Tập tin',
            actions: 'Thao tác',
            folder: 'Thư mục',
            pasting_items: 'Đang giữ {n} mục ({a})',
            paste_here: 'Dán vào đây',
            delete_confirm_title: 'Xóa dữ liệu',
            delete_confirm_msg: 'Bạn có chắc chắn muốn xóa mục này?',
            delete_batch_msg: 'Bạn có chắc chắn muốn xóa {n} mục đã chọn? Không thể hoàn tác!',
            rename_title: 'Đổi tên file/thư mục:',
            new_folder_title: 'Tạo thư mục mới',
            toast_dl_started: 'Luồng tải xuống đã bắt đầu!',
            toast_tg_timeout: 'Kết nối Telegram quá lâu, vui lòng thử lại sau.',
            toast_only_files: 'Vui lòng chỉ chọn các tệp tin (Chưa hỗ trợ tải cả thư mục).',
            toast_skipped_folders: 'Đã tự động bỏ qua các thư mục trong danh sách.',
            toast_preparing_dl: 'Đang chuẩn bị tải {n} tệp...',
            toast_pasted: 'Hoàn tất di chuyển/sao chép!',
            toast_deleted: 'Đã xóa {n} mục',
            toast_renamed: 'Đổi tên thành công',
            toast_revoked: 'Đã thu hồi link Public',
            toast_copied: 'Đã copy link {t}!',
            toast_login_fail: 'Sai mật khẩu truy cập!',
            toast_created: 'Đã tạo: {n}',
            status_error: 'Lỗi',
            conn_error: 'Lỗi kết nối!',
            file_too_large_title: 'File quá lớn',
            file_too_large_msg: 'File "{f}" vượt quá giới hạn tải lên.',
            type_image: 'Hình ảnh',
            type_video: 'Video',
            type_audio: 'Âm thanh',
            type_code: 'Mã nguồn',
            type_archive: 'Tệp nén',
            type_doc: 'Tài liệu',
            type_unknown: 'Tập tin',
            // WebDAV Guide
            webdav_guide: 'Hướng dẫn WebDAV',
            webdav_desc: 'Sử dụng TeleCloud như một ổ đĩa mạng trên máy tính của bạn.',
            webdav_url: 'Địa chỉ máy chủ',
            webdav_user: 'Tên đăng nhập',
            webdav_pass: 'Mật khẩu',
            webdav_step1: 'Mở ứng dụng hỗ trợ WebDAV (như RaiDrive, Cyberduck, hoặc tính năng Map Network Drive trên Windows/macOS).',
            webdav_step2: 'Nhập các thông tin dưới đây để kết nối:',
            webdav_note: 'Lưu ý: Nếu dùng Windows Explorer mặc định, bạn nên sử dụng giao thức HTTP và đảm bảo cổng (port) là chính xác.',
            // share.html specific
            preparing_file: 'Đang chuẩn bị file...',
            wait_tg: 'Kết nối máy chủ Telegram, vui lòng đợi',
            loading: 'Đang tải...',
            download_now: 'Tải xuống ngay',
            safe_msg: 'Đã quét an toàn bởi hệ thống',
            at_time: 'lúc',
            back_home: 'Quay lại trang chủ',
            access_failed: 'Truy cập thất bại',
            file_not_found_msg: 'Tệp không tồn tại hoặc link đã bị thu hồi.'
        },
        en: {
            home: 'File Manager',
            upload: 'Upload',
            new_folder: 'New Folder',
            logout: 'Logout',
            login: 'Login',
            refresh: 'Refresh',
            search_placeholder: 'Search files or folders...',
            empty_folder: 'This folder is empty',
            no_results: 'No matching results for',
            clear_search: 'Clear search',
            max_limit: 'Upload limit',
            items_count: 'items in this folder',
            lang_vi: 'Tiếng Việt',
            lang_en: 'English',
            unlimited: 'Unlimited',
            online: 'System Online',
            download: 'Download',
            rename: 'Rename',
            move: 'Move',
            copy: 'Copy',
            delete: 'Delete',
            share_link: 'Create Share Link',
            revoke_link: 'Revoke Link',
            copy_share_link: 'Copy Share Link',
            copy_direct_link: 'Copy Direct Link',
            file_info: 'File Info',
            size: 'Size',
            capcity: 'Remaining Capacity',
            date: 'Upload Date',
            status: 'Status',
            public: 'Public',
            private: 'Private',
            cancel: 'Cancel',
            confirm: 'Confirm',
            close: 'Close',
            access_code: 'Enter access password...',
            access_system: 'Access System',
            connecting_tg: 'Connecting to Telegram...',
            wait_msg: 'Please do not close this page',
            success: 'Success!',
            failure: 'Failure!',
            preparing_upload: 'Preparing...',
            syncing_tg: 'Syncing with Telegram...',
            done: 'Done!',
            cancelled: 'Cancelled',
            upload_progress: 'Upload Progress',
            upload_done: 'Upload Finished',
            max_size: 'Max size per file',
            drop_files: 'Drop files here to upload',
            drop_modal: 'Drag & drop files here',
            or_click: 'Or click the button below to browse',
            select_files: 'Select files from device',
            name_placeholder: 'Name',
            actions: 'Actions',
            folder: 'Folder',
            pasting_items: 'Holding {n} items ({a})',
            paste_here: 'Paste here',
            delete_confirm_title: 'Delete Data',
            delete_confirm_msg: 'Are you sure you want to delete this item?',
            delete_batch_msg: 'Are you sure you want to delete {n} selected items? Cannot be undone!',
            rename_title: 'Rename file/folder:',
            new_folder_title: 'Create new folder',
            toast_dl_started: 'Download stream started!',
            toast_tg_timeout: 'Telegram connection took too long, please try again.',
            toast_only_files: 'Please select files only (Folder download not supported).',
            toast_skipped_folders: 'Automatically skipped folders in the list.',
            toast_preparing_dl: 'Preparing to download {n} files...',
            toast_pasted: 'Move/Copy complete!',
            toast_deleted: 'Deleted {n} items',
            toast_renamed: 'Rename successful',
            toast_revoked: 'Public link revoked',
            toast_copied: 'Copied {t} link!',
            toast_login_fail: 'Wrong access password!',
            toast_created: 'Created: {n}',
            status_error: 'Error',
            conn_error: 'Connection Error!',
            file_too_large_title: 'File too large',
            file_too_large_msg: 'File "{f}" exceeds upload limit.',
            type_image: 'Image',
            type_video: 'Video',
            type_audio: 'Audio',
            type_code: 'Source Code',
            type_archive: 'Archive',
            type_doc: 'Document',
            type_unknown: 'File',
            // WebDAV Guide
            webdav_guide: 'WebDAV Guide',
            webdav_desc: 'Use TeleCloud as a network drive on your computer.',
            webdav_url: 'Server Address',
            webdav_user: 'Username',
            webdav_pass: 'Password',
            webdav_step1: 'Open a WebDAV client (like RaiDrive, Cyberduck, or Map Network Drive on Windows/macOS).',
            webdav_step2: 'Enter the following details to connect:',
            webdav_note: 'Note: If using default Windows Explorer, ensure you use the correct port and HTTP protocol.',
            // share.html specific
            preparing_file: 'Preparing file...',
            wait_tg: 'Connecting to Telegram, please wait',
            loading: 'Loading...',
            download_now: 'Download Now',
            safe_msg: 'Safely scanned by system',
            at_time: 'at',
            back_home: 'Back to Home',
            access_failed: 'Access Failed',
            file_not_found_msg: 'File not found or link has been revoked.'
        }
    },

    t(key, params = {}, lang = this.lang) {
        let text = (this.translations[lang] && this.translations[lang][key]) || key;
        Object.keys(params).forEach(p => {
            text = text.replace(`{${p}}`, params[p]);
        });
        return text;
    },

    toggleLang() {
        this.lang = this.lang === 'vi' ? 'en' : 'vi';
        localStorage.setItem('lang', this.lang);
        return this.lang;
    },

    setLang(l) {
        this.lang = l;
        localStorage.setItem('lang', l);
        return l;
    },

    formatBytes(bytes, decimals = 2) {
        if (!+bytes) return '0 B';
        const k = 1024;
        const dm = decimals < 0 ? 0 : decimals;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
    },

    formatDate(dateStr, lang = this.lang) {
        if (!dateStr) return '--';
        const safeString = dateStr.replace(' ', 'T') + 'Z';
        const d = new Date(safeString);
        if (isNaN(d)) return dateStr;
        const options = { hour: '2-digit', minute: '2-digit' };
        if (lang === 'vi') {
            return d.toLocaleDateString('vi-VN') + ' ' + this.t('at_time', {}, lang) + ' ' + d.toLocaleTimeString('vi-VN', options);
        }
        return d.toLocaleDateString('en-US') + ' ' + this.t('at_time', {}, lang) + ' ' + d.toLocaleTimeString('en-US', options);
    },

    getFileTypeData(filename) {
        if (!filename) return { n: this.t('type_unknown'), c: 'bg-slate-100 dark:bg-slate-800', i: '<i class="fa-solid fa-file text-2xl"></i>' };
        const ext = filename.split('.').pop().toLowerCase();
        const types = {
            'jpg': { n: this.t('type_image'), c: 'bg-rose-100 text-rose-500 dark:bg-rose-500/20 dark:text-rose-400', i: '<i class="fa-solid fa-image text-2xl"></i>' },
            'jpeg': 'jpg', 'png': 'jpg', 'gif': 'jpg', 'webp': 'jpg', 'svg': 'jpg',
            'mp4': { n: this.t('type_video'), c: 'bg-purple-100 text-purple-500 dark:bg-purple-500/20 dark:text-purple-400', i: '<i class="fa-solid fa-film text-2xl"></i>' },
            'mov': 'mp4', 'avi': 'mp4', 'mkv': 'mp4',
            'mp3': { n: this.t('type_audio'), c: 'bg-amber-100 text-amber-500 dark:bg-amber-500/20 dark:text-amber-400', i: '<i class="fa-solid fa-music text-2xl"></i>' },
            'wav': 'mp3', 'flac': 'mp3',
            'php': { n: this.t('type_code'), c: 'bg-indigo-100 text-indigo-500 dark:bg-indigo-500/20 dark:text-indigo-400', i: '<i class="fa-solid fa-code text-2xl"></i>' },
            'js': 'php', 'html': 'php', 'css': 'php', 'py': 'php', 'json': 'php', 'sql': 'php',
            'zip': { n: this.t('type_archive'), c: 'bg-orange-100 text-orange-500 dark:bg-orange-500/20 dark:text-orange-400', i: '<i class="fa-solid fa-file-zipper text-2xl"></i>' },
            'rar': 'zip', 'ipa': 'zip', 'tar': 'zip', 'gz': 'zip', '7z': 'zip', 'apk': 'zip',
            'pdf': { n: this.t('type_doc'), c: 'bg-red-100 text-red-500 dark:bg-red-500/20 dark:text-red-400', i: '<i class="fa-solid fa-file-pdf text-2xl"></i>' },
            'doc': 'pdf', 'docx': 'pdf', 'xls': 'pdf', 'xlsx': 'pdf', 'txt': 'pdf'
        };
        let result = types[ext];
        if (typeof result === 'string') result = types[result];
        return result || { n: this.t('type_unknown') + ' (' + ext.toUpperCase() + ')', c: 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400', i: '<i class="fa-solid fa-file text-2xl"></i>' };
    }
};
