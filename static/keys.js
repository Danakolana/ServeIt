(function () {
  function bindRowKeys(table) {
    if (!table || !table.tBodies || !table.tBodies[0]) return;
    var cursor = null;
    function rows() { return Array.from(table.tBodies[0].rows); }
    function move(step) {
      var list = rows();
      if (!list.length) return;
      var index = list.indexOf(cursor);
      if (index < 0) index = step > 0 ? 0 : list.length - 1;
      else index = Math.max(0, Math.min(list.length - 1, index + step));
      if (cursor) cursor.classList.remove('kbd');
      cursor = list[index];
      cursor.classList.add('kbd');
      cursor.scrollIntoView({ block: 'nearest' });
    }
    document.addEventListener('keydown', function (event) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key !== 'j' && event.key !== 'k' && event.key !== ' ' && event.key !== 'Enter') return;
      var active = document.activeElement;
      var tag = active && active.tagName ? active.tagName.toLowerCase() : '';
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || tag === 'button' || (active && active.isContentEditable)) return;
      if (event.key === 'j' || event.key === 'k') {
        event.preventDefault();
        move(event.key === 'j' ? 1 : -1);
        return;
      }
      if (!cursor) return;
      if (event.key === ' ') {
        var box = cursor.querySelector('.pick, .item-check');
        if (!box) return;
        event.preventDefault();
        box.checked = !box.checked;
        box.dispatchEvent(new Event('change', { bubbles: true }));
        return;
      }
      var link = cursor.querySelector('a');
      if (!link) return;
      event.preventDefault();
      link.click();
    });
  }
  window.bindRowKeys = bindRowKeys;
})();
