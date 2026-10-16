// jsdom does not always expose window.localStorage in the vitest environment
// (an opaque origin leaves it undefined). Component tests that read or clear it
// (e.g. remoteUse.components.test.tsx) need a working storage object, so provide
// a minimal in-memory shim when the real one is missing. The real implementation,
// when present, is left untouched.
if (typeof window !== "undefined" && typeof window.localStorage === "undefined") {
  const store = new Map<string, string>();
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: {
      getItem: (key: string) => (store.has(key) ? store.get(key)! : null),
      setItem: (key: string, value: string) => {
        store.set(key, String(value));
      },
      removeItem: (key: string) => {
        store.delete(key);
      },
      clear: () => {
        store.clear();
      },
      key: (index: number) => Array.from(store.keys())[index] ?? null,
      get length() {
        return store.size;
      },
    },
  });
}
