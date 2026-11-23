// Fills the "On this page" column from the page's h2 and h3 headings.
(function () {
  var list = document.getElementById('toc');
  if (!list) return;

  var heads = document.querySelectorAll('.content article h2[id], .content article h3[id]');
  if (heads.length === 0) {
    list.parentNode.hidden = true;
    return;
  }

  var frag = document.createDocumentFragment();
  heads.forEach(function (h) {
    var li = document.createElement('li');
    if (h.tagName === 'H3') li.className = 'sub';
    var a = document.createElement('a');
    a.href = '#' + h.id;
    a.textContent = h.textContent;
    li.appendChild(a);
    frag.appendChild(li);
  });
  list.appendChild(frag);
})();
