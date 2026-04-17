document.addEventListener("htmx:configRequest", function(evt) {
    var m = document.querySelector('meta[name="csrf-token"]');
    if (m) evt.detail.headers['X-CSRF-Token'] = m.content;
});

function handleForceStart(event) {
    if (event.detail.xhr.status === 412) {
        if (confirm(event.detail.xhr.responseText + '\n\nBypass health protection?')) {
            var url = new URL(event.detail.requestConfig.path, window.location.origin);
            url.searchParams.set('force', 'true');
            var hdrs = {};
            var m = document.querySelector('meta[name="csrf-token"]');
            if (m) hdrs['X-CSRF-Token'] = m.content;
            htmx.ajax('POST', url.toString(), {target: event.detail.target, swap: 'innerHTML', headers: hdrs});
        }
    }
}
