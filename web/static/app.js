// The app shell: the menu, the signed-in user block, and the active nav link.
// Shared by every page so the navigation can never drift between them — the
// nav markup is identical in each file and this decides which link is lit.
(function () {
  var byId = function (id) { return document.getElementById(id); };

  // --- drawer (narrow screens) ---------------------------------------
  var sb = byId('side-nav'), bd = byId('backdrop'), mo = byId('menu-open'), mc = byId('menu-close');
  if (sb && bd) {
    var open = function (v) {
      sb.classList.toggle('open', v);
      bd.classList.toggle('open', v);
      if (mo) mo.setAttribute('aria-expanded', String(v));
    };
    if (mo) mo.onclick = function () { open(true); };
    if (mc) mc.onclick = function () { open(false); };
    bd.onclick = function () { open(false); };
    addEventListener('keydown', function (e) { if (e.key === 'Escape') open(false); });
  }

  // --- which page am I on --------------------------------------------
  // Derived from the URL rather than hand-marked in each file, so a copied
  // nav block cannot end up highlighting the wrong page.
  var here = location.pathname.replace(/\/+$/, '') || '/dashboard';
  Array.prototype.forEach.call(document.querySelectorAll('.nav a'), function (a) {
    var href = a.getAttribute('href').replace(/\/+$/, '');
    var on = href === here;
    a.classList.toggle('active', on);
    if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  });

  var today = byId('today');
  if (today && !today.textContent.trim()) {
    today.textContent = new Intl.DateTimeFormat('en-GB',
      { weekday: 'long', month: 'long', day: 'numeric' }).format(new Date());
  }

  // --- who is signed in ------------------------------------------------
  // A demo account says so plainly rather than letting fictional data pass
  // as real. Every element here is optional: pages opt in by including the id.
  fetch('/api/me').then(function (r) { return r.json(); }).then(function (me) {
    if (!me || !me.authenticated) return;
    var first = (me.name || 'there').split(' ')[0];
    var set = function (id, text) { var el = byId(id); if (el) el.textContent = text; };
    var greeting = byId('greeting');
    if (greeting && greeting.dataset.greet !== 'off') greeting.textContent = 'Hello, ' + first;
    set('user-name', me.name || 'Member');
    set('user-meta', me.is_demo ? 'demo · fictional data' : (me.email || ''));
    set('avatar', (first[0] || 'M').toUpperCase());
    set('first-name', first);
    if (me.is_demo) {
      var n = byId('demo-notice');
      if (n) n.hidden = false;
    }
  }).catch(function () {});
})();
