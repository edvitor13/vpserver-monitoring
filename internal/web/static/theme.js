// Aplica o tema salvo antes de pintar a página (evita piscar claro→escuro).
try {
  var t = localStorage.getItem('vpmon-theme');
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
} catch (e) {}
