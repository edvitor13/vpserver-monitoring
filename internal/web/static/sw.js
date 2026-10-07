// Service worker mínimo do VPServer: existe só para o navegador oferecer
// "Instalar app". NÃO guarda nada em cache: a tela sempre vem do servidor, e
// a versão nova chega sozinha (a página confere a versão em cada resposta).
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', (e) => {
  // só as aberturas de página passam por aqui, direto para a rede; API, SSE e
  // arquivos seguem o caminho normal do navegador
  if (e.request.mode === 'navigate') e.respondWith(fetch(e.request));
});
