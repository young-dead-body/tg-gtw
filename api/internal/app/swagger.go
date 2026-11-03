package app

// swaggerUIHTML is a self-contained Swagger-style UI for the OpenAPI spec.
// It loads the spec from ./swagger.json and the renderer from ./swagger-ui.js.
// Scripts/extras are same-origin only so the served page remains CSP-safe
// (script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline').
const swaggerUIHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Gateway API — OpenAPI</title>
  <style>
    :root { color-scheme: light; }
    * { box-sizing: border-box; }
    body { margin: 0; font-family: -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif; background: #f6f8fa; color: #1f2328; }
    header { background: #24292f; color: #fff; padding: 16px 28px; }
    header h1 { margin: 0; font-size: 20px; }
    header .sub { opacity: .8; font-size: 13px; margin-top: 2px; }
    main { max-width: 960px; margin: 24px auto; padding: 0 16px; }
    .servers, .op { background: #fff; border: 1px solid #d0d7de; border-radius: 8px; padding: 14px 18px; margin-bottom: 14px; }
    .servers code { background: #eff1f3; padding: 2px 6px; border-radius: 4px; }
    .op { border-left: 4px solid #57606a; }
    .op .head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
    .method { font-weight: 700; padding: 3px 10px; border-radius: 4px; color: #fff; text-transform: uppercase; font-size: 13px; }
    .m-get { background:#1a7f37; } .m-post { background:#0969da; } .m-patch { background:#9a6700; } .m-delete { background:#cf222e; } .m-put { background:#8250df; }
    .path { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 15px; }
    .summary { flex: 1; min-width: 120px; font-size: 13px; color: #57606a; }
    section { margin: 12px 0 2px; font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: #57606a; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; margin-top: 4px; }
    th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #eef1f4; vertical-align: top; }
    th { color: #57606a; font-weight: 600; }
    code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
    pre { background: #f6f8fa; border: 1px solid #eef1f4; border-radius: 6px; padding: 10px; overflow: auto; }
    .err { color: #cf222e; }
  </style>
</head>
<body>
  <header>
    <h1>Защищённый шлюз доступа к Telegram Web — API</h1>
    <div class="sub" id="subtitle">OpenAPI 3.1</div>
  </header>
  <main id="root">Загрузка спецификации…</main>
  <script src="./swagger-ui.js"></script>
</body>
</html>`

// swaggerUIJS renders the OpenAPI spec fetched from ./swagger.json into a
// readable, method/response-accurate view of every path and operation.
const swaggerUIJS = `(function () {
  var root = document.getElementById('root');
  var sub = document.getElementById('subtitle');
  fetch('./swagger.json').then(function (r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(render).catch(function (e) {
    root.innerHTML = '<p class="err">Не удалось загрузить swagger.json: ' + e.message + '</p>';
  });

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function render(spec) {
    sub.textContent = spec.info.title + ' — OpenAPI 3.1 / v' + spec.info.version;
    var html = '';
    if (spec.servers && spec.servers.length) {
      html += '<div class="servers">Серверы: ';
      html += spec.servers.map(function (s) { return '<code>' + esc(s.url) + '</code>'; }).join(' ');
      html += '</div>';
    }
    Object.keys(spec.paths || {}).sort().forEach(function (path) {
      Object.keys(spec.paths[path]).forEach(function (method) {
        if (['get', 'post', 'put', 'patch', 'delete'].indexOf(method) < 0) return;
        var op = spec.paths[path][method];
        html += '<div class="op">';
        html += '<div class="head"><span class="method m-' + method + '">' + method + '</span>';
        html += '<span class="path">' + esc(path) + '</span>';
        html += '<span class="summary">' + esc(op.summary || op.description || '') + '</span></div>';

        if (op.parameters && op.parameters.length) {
          html += '<section>Параметры</section><table><tr><th>Имя</th><th>В</th><th>Тип</th><th>Требуется</th></tr>';
          op.parameters.forEach(function (p) {
            html += '<tr><td>' + esc(p.name) + '</td><td>' + esc(p.in) + '</td><td><code>' + esc((p.schema && p.schema.type) || '') + '</code></td><td>' + (p.required ? 'да' : '—') + '</td></tr>';
          });
          html += '</table>';
        }
        if (op.requestBody && op.requestBody.content && op.requestBody.content['application/json']) {
          var schema = op.requestBody.content['application/json'].schema;
          if (schema) {
            html += '<section>Тело запроса</section><pre>' + esc(JSON.stringify(schema, null, 2)) + '</pre>';
          }
        }
        html += '<section>Ответы</section><table><thead><tr><th>Код</th><th>Описание</th></tr></thead>';
        Object.keys(op.responses || {}).forEach(function (code) {
          html += '<tr><td>' + esc(code) + '</td><td>' + esc(op.responses[code].description) + '</td></tr>';
        });
        html += '</table></div>';
      });
    });
    root.innerHTML = html;
  }
})();
`
