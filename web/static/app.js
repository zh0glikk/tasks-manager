(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);

  const WD = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'нд'];
  const MN = ['січ', 'лют', 'бер', 'квіт', 'трав', 'черв', 'лип', 'серп', 'вер', 'жовт', 'лист', 'груд'];
  const MNG = ['січня', 'лютого', 'березня', 'квітня', 'травня', 'червня', 'липня', 'серпня', 'вересня', 'жовтня', 'листопада', 'грудня'];
  const MONTHS = ['Січень', 'Лютий', 'Березень', 'Квітень', 'Травень', 'Червень', 'Липень', 'Серпень', 'Вересень', 'Жовтень', 'Листопад', 'Грудень'];
  const KINDS = ['лекція', 'практичне', 'семінар', 'консультація', 'лабораторна', 'залік', 'іспит'];
  const GENERAL = { id: null, name: 'Загальне', short: 'Загальне', teacher: '', link: '', color: '#7C867F' };
  const MAX_PILLS = 4;

  // ---- dates ----
  const pad = (n) => String(n).padStart(2, '0');
  const iso = (d) => d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
  const parse = (s) => { const [y, m, d] = s.split('-').map(Number); return new Date(y, m - 1, d); };
  const addDays = (d, n) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
  const today = () => iso(new Date());
  const diffDays = (s) => Math.round((parse(s) - parse(today())) / 864e5);
  const wd = (d) => WD[(d.getDay() + 6) % 7];
  const shortDate = (s) => { const d = parse(s); return wd(d) + ' ' + d.getDate() + ' ' + MN[d.getMonth()]; };
  const longDate = (s) => { const d = parse(s); return wd(d) + ', ' + d.getDate() + ' ' + MNG[d.getMonth()]; };
  const plural = (n, one, few, many) => {
    const m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20)) return few;
    return many;
  };
  const inDays = (n) => n < 0 ? 'Прострочено' : n === 0 ? 'Сьогодні' : n === 1 ? 'Завтра' : 'Через ' + n + ' ' + plural(n, 'день', 'дні', 'днів');
  const ago = (ts) => {
    const d = new Date(ts), mins = Math.round((Date.now() - d) / 6e4);
    if (mins < 1) return 'щойно';
    if (mins < 60) return mins + ' хв тому';
    if (mins < 60 * 24) return Math.round(mins / 60) + ' год тому';
    const days = Math.round(mins / 1440);
    if (days < 7) return days + ' ' + plural(days, 'день', 'дні', 'днів') + ' тому';
    return d.getDate() + ' ' + MN[d.getMonth()];
  };
  const stamp = (ts) => {
    const d = new Date(ts), k = iso(d), hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
    if (k === today()) return 'сьогодні<br>' + hm;
    if (k === iso(addDays(new Date(), -1))) return 'вчора<br>' + hm;
    return d.getDate() + ' ' + MN[d.getMonth()] + '<br>' + hm;
  };

  // ---- safety ----
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const safeUrl = (u) => /^https?:\/\//i.test(u || '') ? u : '';
  const safeColor = (c) => /^#[0-9a-f]{6}$/i.test(c || '') ? c : GENERAL.color;

  // ---- storage (best effort) ----
  const store = {
    get(k, def) { try { const v = localStorage.getItem(k); return v === null ? def : JSON.parse(v); } catch { return def; } },
    set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private mode */ } },
  };

  // ---- state ----
  const now = new Date();
  const state = {
    courses: [], events: [], assignments: [], upcoming: [], posts: [],
    month: new Date(now.getFullYear(), now.getMonth(), 1),
    sel: today(),
    off: new Set(store.get('hiddenCourses', [])),
    protected: false,
  };

  const courseOf = (id) => state.courses.find((c) => c.id === id) || GENERAL;
  const cvars = (c) => { const col = safeColor(c.color); return `--c:${col};--cs:${col}1f`; };
  const visible = (courseId) => courseId == null || !state.off.has(courseId);

  // ---- api ----
  // ---- edit code ----
  const editCode = () => store.get('editCode', '');
  const validCode = (c) => /^[\x21-\x7e]+$/.test(c); // header-safe ASCII

  // ensureCode asks for the group code once when the server requires it.
  function ensureCode() {
    if (!state.protected || editCode()) return true;
    const c = (prompt('Код групи для редагування (спитай у чаті групи):') || '').trim();
    if (!c) return false;
    store.set('editCode', c);
    return true;
  }

  async function api(method, path, body) {
    const headers = body ? { 'Content-Type': 'application/json' } : {};
    if (method !== 'GET' && validCode(editCode())) headers['X-Edit-Code'] = editCode();
    const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
    let data = null;
    try { data = await res.json(); } catch { /* empty */ }
    if (!res.ok) {
      if (data && data.code_required) {
        store.set('editCode', '');
        state.protected = true;
        throw new Error('Неправильний код групи. Перевір його й спробуй ще раз.');
      }
      if (data && data.error) throw new Error(data.error);
      // the hosting proxy may replace our JSON error with its own HTML page
      if (res.status === 502 || res.status === 503 || res.status === 504) throw new Error('сервер запускається або недоступний, спробуйте за хвилину');
      throw new Error('Помилка ' + res.status);
    }
    return data;
  }

  function gridRange() {
    const first = state.month;
    const start = addDays(first, -((first.getDay() + 6) % 7));
    const daysInMonth = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate();
    const cells = Math.ceil((((first.getDay() + 6) % 7) + daysInMonth) / 7) * 7;
    return { start, cells, end: addDays(start, cells - 1) };
  }

  async function loadMonth() {
    const { start, end } = gridRange();
    const q = `?from=${iso(start)}&to=${iso(end)}`;
    [state.events, state.assignments] = await Promise.all([api('GET', '/api/events' + q), api('GET', '/api/assignments' + q)]);
  }
  async function loadUpcoming() {
    state.upcoming = await api('GET', `/api/assignments?from=${today()}&to=${iso(addDays(new Date(), 90))}`);
  }
  async function loadPosts() { state.posts = await api('GET', '/api/posts?limit=30'); }
  async function loadCourses() { state.courses = await api('GET', '/api/courses'); }
  async function loadConfig() { state.protected = (await api('GET', '/api/config')).edit_protected; }

  async function reload() {
    try {
      await Promise.all([loadConfig(), loadCourses(), loadMonth(), loadUpcoming(), loadPosts()]);
    } catch (e) {
      toast('Не вдалося завантажити: ' + e.message, true);
    }
    render();
  }

  // ---- render ----
  function render() {
    renderUpcoming();
    renderChips();
    renderCalendar();
    renderDay();
    renderNews();
    renderCourses();
  }

  function renderUpcoming() {
    const list = state.upcoming.filter((a) => visible(a.course_id));
    const soon = list.filter((a) => diffDays(a.due_date) <= 14).length;
    $('#dl-count').textContent = soon + ' ' + plural(soon, 'завдання', 'завдання', 'завдань') + ' за найближчі 14 днів';
    if (!list.length) {
      $('#upcoming').innerHTML = '<div class="empty-card">Дедлайнів попереду немає. Додай перший — кнопка «Додати» вгорі.</div>';
      return;
    }
    $('#upcoming').innerHTML = list.slice(0, 8).map((a) => {
      const c = courseOf(a.course_id), n = diffDays(a.due_date);
      return `<button type="button" class="dl-card${n <= 1 ? ' hot' : ''}" data-goto="${a.due_date}">
        <span class="row"><span class="when">${inDays(n)}</span><span class="date">${shortDate(a.due_date)}</span></span>
        <span class="title">${esc(a.title)}</span>
        <span class="course-line"><span class="dot" style="${cvars(c)}"></span>${esc(c.short)}${a.due_time ? ' · до ' + esc(a.due_time) : ''}</span>
      </button>`;
    }).join('');
  }

  function renderChips() {
    $('#chips').innerHTML = state.courses.map((c) =>
      `<button type="button" class="chip" style="${cvars(c)}" data-chip="${c.id}" aria-pressed="${!state.off.has(c.id)}"><span class="dot"></span>${esc(c.short)}</button>`
    ).join('');
  }

  function renderCalendar() {
    const { start, cells } = gridRange();
    const m = state.month.getMonth(), t = today();
    $('#month-title').textContent = MONTHS[m] + ' ' + state.month.getFullYear();
    $('#wdrow').innerHTML = WD.map((w) => `<div class="wd">${w}</div>`).join('');

    let html = '';
    for (let i = 0; i < cells; i++) {
      const d = addDays(start, i), k = iso(d);
      const dls = state.assignments.filter((a) => a.due_date === k && visible(a.course_id));
      const evs = state.events.filter((e) => e.date === k && visible(e.course_id));
      const cls = ['cell'];
      if (d.getMonth() !== m) cls.push('out');
      else if ((d.getDay() + 6) % 7 > 4) cls.push('we');
      if (k === t) cls.push('today');
      if (k === state.sel) cls.push('sel');
      const aria = d.getDate() + ' ' + MNG[d.getMonth()] +
        (dls.length ? ', дедлайнів: ' + dls.length : '') + (evs.length ? ', занять: ' + evs.length : '');
      const pills = evs.slice(0, Math.max(0, MAX_PILLS - dls.length)).map((e) => {
        const c = courseOf(e.course_id);
        return `<span class="pill${e.tentative ? ' tent' : ''}" style="${cvars(c)}"><b>${esc(e.start_time)}</b> ${esc(c.short)}</span>`;
      }).join('');
      const hidden = evs.length - Math.max(0, MAX_PILLS - dls.length);
      html += `<button type="button" class="${cls.join(' ')}" data-day="${k}" aria-label="${esc(aria)}">
        <span class="dnum">${d.getDate()}</span>
        ${dls.map((a) => `<span class="tag-due"><svg width="10" height="10" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 3v18M5 4h12l-2 4 2 4H5"/></svg>${esc(a.title)}</span>`).join('')}
        ${pills}${hidden > 0 ? `<span class="more">+${hidden}</span>` : ''}
      </button>`;
    }
    $('#grid').innerHTML = html;
  }

  function renderDay() {
    const k = state.sel;
    const pool = new Map([...state.assignments, ...state.upcoming].map((a) => [a.id, a]));
    const dls = [...pool.values()].filter((a) => a.due_date === k && visible(a.course_id));
    const evs = state.events.filter((e) => e.date === k && visible(e.course_id));
    const actions = (type, id) => `<span class="item-actions">
      <button type="button" class="link-btn" data-act="edit" data-type="${type}" data-id="${id}">Змінити</button>
      <button type="button" class="link-btn danger" data-act="delete" data-type="${type}" data-id="${id}">Видалити</button></span>`;

    let html = `<div class="panel-head"><h3>${longDate(k)}${k === today() ? ' · сьогодні' : ''}</h3>
      <button type="button" class="link-btn" data-add-on="${k}">+ додати на цей день</button></div>`;
    if (!dls.length && !evs.length) html += '<p class="muted">Нічого не заплановано.</p>';
    html += dls.map((a) => {
      const c = courseOf(a.course_id), link = safeUrl(a.link);
      return `<div class="item due">
        <span class="t">${a.due_time ? 'до ' + esc(a.due_time) : 'протягом дня'}</span>
        <div class="body">
          <span class="kind">Дедлайн · ${esc(c.short)}</span>
          <span class="nm">${esc(a.title)}</span>
          ${a.description ? `<span class="desc">${esc(a.description)}</span>` : ''}
          ${link ? `<a href="${esc(link)}" target="_blank" rel="noopener" class="sub">${esc(link.replace(/^https?:\/\//, ''))}</a>` : ''}
          <span class="sub">додав(ла) ${esc(a.author)}</span>
        </div>${actions('assignment', a.id)}</div>`;
    }).join('');
    html += evs.map((e) => {
      const c = courseOf(e.course_id), link = safeUrl(e.link || c.link);
      return `<div class="item" style="${cvars(c)}">
        <span class="t">${esc(e.start_time)}${e.end_time ? '–' + esc(e.end_time) : ''}</span>
        <div class="body">
          <span class="kind">${esc(e.kind || 'заняття')}${e.tentative ? ' · орієнтовно' : ''}</span>
          <span class="nm">${esc(c.name)}</span>
          ${(e.teacher || c.teacher) ? `<span class="sub">${esc(e.teacher || c.teacher)}</span>` : ''}
          ${e.note ? `<span class="desc">${esc(e.note)}</span>` : ''}
          ${link ? `<a href="${esc(link)}" target="_blank" rel="noopener" class="sub">${esc(link.replace(/^https?:\/\//, ''))}</a>` : ''}
        </div>${actions('event', e.id)}</div>`;
    }).join('');
    $('#day').innerHTML = html;
  }

  function renderNews() {
    const list = state.posts.filter((p) => visible(p.course_id));
    if (!list.length) {
      $('#news').innerHTML = '<div class="empty-card">Новин поки немає.</div>';
      return;
    }
    $('#news').innerHTML = list.map((p) => {
      const c = courseOf(p.course_id);
      return `<article class="post${p.pinned ? ' pinned' : ''}">
        ${p.pinned ? '<span class="pin"><svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 17v5"/><path d="M9 3h6l-1 7 4 3v2H6v-2l4-3z"/></svg>Закріплено</span>' : ''}
        <h3>${esc(p.title)}</h3>
        ${p.body ? `<p>${esc(p.body)}</p>` : ''}
        <div class="meta">
          <span class="course-line"><span class="dot" style="${cvars(c)}"></span>${esc(p.author)} · ${ago(p.created_at)} · ${esc(c.short)}</span>
          <span class="item-actions">
            <button type="button" class="link-btn" data-act="edit" data-type="post" data-id="${p.id}">Змінити</button>
            <button type="button" class="link-btn danger" data-act="delete" data-type="post" data-id="${p.id}">Видалити</button>
          </span>
        </div>
      </article>`;
    }).join('');
  }

  function renderCourses() {
    $('#courses').innerHTML = state.courses.map((c) => {
      const link = safeUrl(c.link);
      return `<div class="course" style="${cvars(c)}">
        <h3>${esc(c.name)}</h3>
        ${c.teacher ? `<span class="sub">${esc(c.teacher)}</span>` : ''}
        ${link ? `<a href="${esc(link)}" target="_blank" rel="noopener">${esc(link.replace(/^https?:\/\//, ''))}</a>` : ''}
        <span class="item-actions">
          <button type="button" class="link-btn" data-act="edit" data-type="course" data-id="${c.id}">Змінити</button>
          <button type="button" class="link-btn danger" data-act="delete" data-type="course" data-id="${c.id}">Видалити</button>
        </span>
      </div>`;
    }).join('') || '<div class="empty-card">Предметів ще немає.</div>';
  }

  // ---- navigation ----
  async function goTo(dateStr, scroll) {
    const d = parse(dateStr);
    const target = new Date(d.getFullYear(), d.getMonth(), 1);
    state.sel = dateStr;
    if (target.getTime() !== state.month.getTime()) {
      state.month = target;
      try { await loadMonth(); } catch (e) { toast(e.message, true); }
    }
    render();
    if (scroll) $('#day').scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  async function shiftMonth(delta) {
    state.month = new Date(state.month.getFullYear(), state.month.getMonth() + delta, 1);
    try { await loadMonth(); } catch (e) { toast(e.message, true); }
    render();
  }

  // ---- forms ----
  const TYPES = {
    assignment: { tab: 'ДЗ / дедлайн', add: 'Новий дедлайн', edit: 'Змінити дедлайн', save: 'Додати дедлайн', path: '/api/assignments' },
    event: { tab: 'Заняття', add: 'Нове заняття', edit: 'Змінити заняття', save: 'Додати заняття', path: '/api/events' },
    post: { tab: 'Новина', add: 'Нова новина', edit: 'Змінити новину', save: 'Опублікувати', path: '/api/posts' },
    course: { tab: 'Предмет', add: 'Новий предмет', edit: 'Змінити предмет', save: 'Додати предмет', path: '/api/courses' },
  };
  const form = { type: 'assignment', item: null, preset: {} };

  const input = (name, label, value, attrs = '', hint = '') =>
    `<label class="field">${label}<input name="${name}" value="${esc(value)}" ${attrs}>${hint ? `<span class="hint">${hint}</span>` : ''}</label>`;
  const textarea = (name, label, value, rows = 3) =>
    `<label class="field">${label}<textarea name="${name}" rows="${rows}">${esc(value)}</textarea></label>`;
  const checkbox = (name, label, checked, extra = '') =>
    `<label class="check"><input type="checkbox" name="${name}"${checked ? ' checked' : ''}>${label}${extra}</label>`;
  const courseSelect = (value) => `<label class="field">Предмет<select name="course_id">
    ${state.courses.map((c) => `<option value="${c.id}"${c.id === value ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}
    <option value=""${value == null ? ' selected' : ''}>Загальне (без предмета)</option></select></label>`;

  function fieldsFor(type, v, isNew) {
    switch (type) {
      case 'assignment':
        return courseSelect(v.course_id) +
          input('title', 'Що треба зробити', v.title, 'required maxlength="200"') +
          `<div class="row2">${input('due_date', 'Дедлайн', v.due_date, 'type="date" required')}${input('due_time', 'До котрої (необов’язково)', v.due_time, 'type="time"')}</div>` +
          textarea('description', 'Деталі', v.description) +
          input('link', 'Посилання (необов’язково)', v.link, 'type="url" placeholder="https://classroom.google.com/…"');
      case 'event': {
        const kinds = KINDS.includes(v.kind) || !v.kind ? KINDS : [v.kind, ...KINDS];
        return courseSelect(v.course_id) +
          `<div class="row2"><label class="field">Тип<select name="kind">${kinds.map((k) => `<option${k === v.kind ? ' selected' : ''}>${esc(k)}</option>`).join('')}</select></label>
          ${input('date', 'Дата', v.date, 'type="date" required')}</div>` +
          `<div class="row2">${input('start_time', 'Початок', v.start_time, 'type="time" required')}${input('end_time', 'Кінець', v.end_time, 'type="time"')}</div>` +
          (isNew ? checkbox('repeat', 'Повторювати щотижня до', false,
            `<input type="date" name="repeat_until" aria-label="Повторювати до дати" value="${esc(v.repeat_until || '')}">`) : '') +
          input('teacher', 'Викладач', v.teacher, 'maxlength="200"', 'Залиш порожнім, якщо як у предмета') +
          input('link', 'Посилання на пару', v.link, 'type="url" placeholder="https://meet.google.com/…"', 'Залиш порожнім, якщо як у предмета') +
          textarea('note', 'Примітка', v.note, 2) +
          checkbox('tentative', 'Дата орієнтовна', v.tentative);
      }
      case 'post':
        return courseSelect(v.course_id) +
          input('title', 'Заголовок', v.title, 'required maxlength="200"') +
          textarea('body', 'Текст', v.body, 5) +
          checkbox('pinned', 'Закріпити вгорі стрічки', v.pinned);
      case 'course':
        return input('name', 'Повна назва', v.name, 'required maxlength="200"') +
          input('short', 'Коротка назва (для календаря)', v.short, 'required maxlength="30"') +
          input('teacher', 'Викладач', v.teacher, 'maxlength="200"') +
          input('link', 'Посилання на пари', v.link, 'type="url" placeholder="https://meet.google.com/…"') +
          input('color', 'Колір', v.color || '#0E5D69', 'type="color"');
    }
    return '';
  }

  function openForm(type, item = null, preset = {}) {
    form.type = type; form.item = item; form.preset = preset;
    drawForm();
    $('#form-error').hidden = true;
    $('#form-dialog').showModal();
    const first = $('#fields').querySelector('input:not([type=hidden]), textarea');
    if (first && type !== 'event') first.focus();
  }

  function drawForm() {
    const { type, item, preset } = form, t = TYPES[type];
    $('#form-title').textContent = item ? t.edit : 'Додати';
    $('#form-submit').textContent = item ? 'Зберегти' : t.save;
    $('#tabs').hidden = !!item;
    $('#tabs').innerHTML = Object.entries(TYPES).map(([k, v]) =>
      `<button type="button" class="tab" role="tab" data-tab="${k}" aria-selected="${k === type}">${v.tab}</button>`).join('');
    const v = Object.assign({ course_id: state.courses[0] ? state.courses[0].id : null }, preset, item || {});
    $('#fields').innerHTML = fieldsFor(type, v, !item) +
      input('author', 'Ваше ім’я', store.get('author', ''), 'maxlength="60" autocomplete="name"', 'Запам’ятаємо в цьому браузері. Видно в журналі змін.') +
      (state.protected ? input('edit_code', 'Код групи', editCode(), 'required autocomplete="off" spellcheck="false" autocapitalize="none"', 'Потрібен для змін. Спитай у чаті групи — запам’ятаємо в цьому браузері.') : '');
  }

  function collectForm() {
    const el = $('#form').elements, val = (n) => (el[n] ? el[n].value.trim() : '');
    const course = () => (val('course_id') ? Number(val('course_id')) : null);
    switch (form.type) {
      case 'assignment':
        return { course_id: course(), title: val('title'), due_date: val('due_date'), due_time: val('due_time'), description: val('description'), link: val('link') };
      case 'event': {
        const body = { course_id: course(), kind: val('kind'), date: val('date'), start_time: val('start_time'), end_time: val('end_time'),
          teacher: val('teacher'), link: val('link'), note: val('note'), tentative: el.tentative.checked };
        if (el.repeat && el.repeat.checked) body.repeat_until = val('repeat_until');
        return body;
      }
      case 'post':
        return { course_id: course(), title: val('title'), body: val('body'), pinned: el.pinned.checked };
      case 'course':
        return { name: val('name'), short: val('short'), teacher: val('teacher'), link: val('link'), color: val('color') };
    }
    return {};
  }

  async function submitForm(ev) {
    ev.preventDefault();
    const body = collectForm(), author = $('#form').elements.author.value.trim();
    body.author = author;
    store.set('author', author);
    if (form.type === 'event' && body.repeat_until === '') return showFormError('Вкажіть, до якої дати повторювати');
    const codeEl = $('#form').elements.edit_code;
    if (codeEl) {
      const c = codeEl.value.trim();
      if (!c) return showFormError('Вкажіть код групи');
      if (!validCode(c)) return showFormError('Код групи — латиниця, цифри і дефіси');
      store.set('editCode', c);
    }

    const t = TYPES[form.type], btn = $('#form-submit');
    btn.disabled = true;
    try {
      if (form.item) await api('PUT', `${t.path}/${form.item.id}`, body);
      else await api('POST', t.path, body);
      $('#form-dialog').close();
      toast(form.item ? 'Збережено' : 'Додано');
      const jump = body.due_date || body.date;
      if (jump) state.sel = jump;
      if (jump) state.month = new Date(parse(jump).getFullYear(), parse(jump).getMonth(), 1);
      await reload();
    } catch (e) {
      showFormError(e.message);
    } finally {
      btn.disabled = false;
    }
  }

  function showFormError(msg) {
    const box = $('#form-error');
    box.textContent = msg;
    box.hidden = false;
  }

  // ---- actions ----
  function findItem(type, id) {
    const lists = { assignment: [...state.assignments, ...state.upcoming], event: state.events, post: state.posts, course: state.courses };
    return (lists[type] || []).find((x) => x.id === id);
  }

  async function removeItem(type, id) {
    const item = findItem(type, id);
    const label = item ? (item.title || item.name || item.kind) : '';
    if (!confirm(`Видалити «${label}»?\nЦе можна повернути в журналі змін.`)) return;
    if (!ensureCode()) return;
    try {
      await api('DELETE', `${TYPES[type].path}/${id}`, { author: store.get('author', '') });
      toast('Видалено — можна повернути в журналі змін');
      await reload();
    } catch (e) {
      toast(e.message, true);
    }
  }

  // ---- history ----
  const VERBS = { create: 'додав(ла)', update: 'змінив(ла)', delete: 'видалив(ла)', revert: 'повернув(ла)' };
  const NOUNS = { course: 'предмет', event: 'заняття', event_series: 'серію занять', assignment: 'дедлайн', post: 'новину' };
  const FIELDS = {
    title: 'назва', name: 'назва', short: 'коротка назва', description: 'деталі', body: 'текст', due_date: 'дата', due_time: 'час',
    date: 'дата', start_time: 'початок', end_time: 'кінець', kind: 'тип', teacher: 'викладач', link: 'посилання',
    note: 'примітка', tentative: 'орієнтовно', pinned: 'закріплення', course_id: 'предмет', color: 'колір',
  };
  const UNDO = { create: 'Скасувати', update: 'Повернути', delete: 'Відновити' };

  function changedFields(c) {
    if (c.action !== 'update' || !c.before || !c.after) return '';
    const keys = Object.keys(FIELDS).filter((k) => k in c.after && JSON.stringify(c.before[k]) !== JSON.stringify(c.after[k]));
    return keys.length ? 'змінено: ' + [...new Set(keys.map((k) => FIELDS[k]))].join(', ') : '';
  }

  async function openHistory() {
    $('#history').innerHTML = '<li><span class="muted">Завантаження…</span></li>';
    $('#history-dialog').showModal();
    try {
      const list = await api('GET', '/api/changes?limit=100');
      $('#history').innerHTML = list.length ? list.map((c) => {
        const fields = changedFields(c);
        const right = c.reverted_at ? '<span class="badge">повернуто</span>'
          : c.action === 'revert' ? '<span class="badge">відкат</span>'
            : `<button type="button" class="btn small${c.action === 'delete' ? ' primary' : ''}" data-revert="${c.id}">${UNDO[c.action] || 'Повернути'}</button>`;
        return `<li class="${c.action === 'delete' && !c.reverted_at ? 'del' : ''}">
          <span class="t">${stamp(c.created_at)}</span>
          <span class="stack" style="gap:2px">
            <span class="what"><b>${esc(c.author)}</b> ${VERBS[c.action] || esc(c.action)} ${NOUNS[c.entity] || esc(c.entity)}</span>
            <span class="summary">«${esc(c.summary)}»${fields ? ' · ' + esc(fields) : ''}</span>
          </span>${right}</li>`;
      }).join('') : '<li><span class="muted">Змін ще не було.</span></li>';
    } catch (e) {
      $('#history').innerHTML = `<li><span class="muted">${esc(e.message)}</span></li>`;
    }
  }

  async function revert(id, btn) {
    if (!ensureCode()) return;
    btn.disabled = true;
    try {
      await api('POST', `/api/changes/${id}/revert`, { author: store.get('author', '') });
      toast('Повернуто');
      await Promise.all([openHistory(), reload()]);
    } catch (e) {
      toast(e.message, true);
      btn.disabled = false;
    }
  }

  // ---- toast ----
  let toastTimer;
  function toast(msg, isErr) {
    const t = $('#toast');
    t.textContent = msg;
    t.className = 'toast' + (isErr ? ' err' : '');
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { t.hidden = true; }, 3500);
  }

  // ---- events ----
  document.addEventListener('click', (ev) => {
    const el = ev.target.closest('button, [data-goto]');
    if (!el) return;
    const ds = el.dataset;
    if (ds.day) goTo(ds.day);
    else if (ds.goto) goTo(ds.goto, true);
    else if (ds.chip) {
      const id = Number(ds.chip);
      state.off.has(id) ? state.off.delete(id) : state.off.add(id);
      store.set('hiddenCourses', [...state.off]);
      render();
    } else if (ds.act === 'edit') {
      const item = findItem(ds.type, Number(ds.id));
      if (item) openForm(ds.type, item);
    } else if (ds.act === 'delete') removeItem(ds.type, Number(ds.id));
    else if (ds.addOn) openForm('assignment', null, { due_date: ds.addOn, date: ds.addOn });
    else if (ds.tab) {
      const keep = form.preset;
      form.type = ds.tab;
      form.preset = keep;
      drawForm();
    } else if (ds.revert) revert(Number(ds.revert), el);
    else if ('close' in ds) el.closest('dialog').close();
  });

  $('#form').addEventListener('submit', submitForm);
  $('#btn-add').addEventListener('click', () => openForm('assignment', null, { due_date: state.sel, date: state.sel }));
  $('#btn-add-post').addEventListener('click', () => openForm('post'));
  $('#btn-add-course').addEventListener('click', () => openForm('course'));
  $('#btn-history').addEventListener('click', openHistory);
  $('#prev').addEventListener('click', () => shiftMonth(-1));
  $('#next').addEventListener('click', () => shiftMonth(1));
  $('#today-btn').addEventListener('click', () => goTo(today()));

  reload();
})();
