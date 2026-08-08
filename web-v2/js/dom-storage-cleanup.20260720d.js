/* One-time cleanup for claim secrets accidentally persisted by the legacy miner lookup UI. */
'use strict';

  const SECRET = /(?:dom[wp]_[0-9a-f]{64}|DOMSLATE4\.|DOM-MAINNET-RECOVERY:)/i;

  function cleanupOne(storage) {
    if (!storage || typeof storage.getItem !== 'function' || typeof storage.removeItem !== 'function') return false;
    try {
      const keys = new Set(['ntm_addr_dom']);
      if (typeof storage.length === 'number' && typeof storage.key === 'function') {
        for (let i = 0; i < storage.length; i++) {
          const key = storage.key(i);
          if (typeof key === 'string' && key.startsWith('ntm_addr_')) keys.add(key);
        }
      }
      let removed = false;
      keys.forEach((key) => {
        const value = storage.getItem(key);
        if (!SECRET.test(typeof value === 'string' ? value : '')) return;
        storage.removeItem(key);
        removed = true;
      });
      return removed;
    } catch (_) {
      return false;
    }
  }

  function cleanup(persistentStorage, sessionStorage) {
    const removedPersistent = cleanupOne(persistentStorage);
    const removedSession = cleanupOne(sessionStorage);
    return removedPersistent || removedSession;
  }

export { cleanup };
