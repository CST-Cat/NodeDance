export interface AsyncOperation {
  run<T>(task: () => Promise<T>): Promise<T | undefined>;
  begin(): number;
  current(): number;
  isCurrent(token: number): boolean;
  invalidate(): void;
}

export function createAsyncOperation(): AsyncOperation {
  let epoch = 0;
  let pending: Promise<unknown> | undefined;
  return {
    async run<T>(task: () => Promise<T>): Promise<T | undefined> {
      if (pending) return pending as Promise<T | undefined>;
      const own = ++epoch;
      const current = (async () => {
        try {
          const result = await task();
          return own === epoch ? result : undefined;
        } catch (reason) {
          if (own !== epoch) return undefined;
          throw reason;
        }
      })();
      pending = current;
      try {
        return await current;
      } finally {
        if (pending === current) pending = undefined;
      }
    },
    begin() {
      pending = undefined;
      return ++epoch;
    },
    current() {
      return epoch;
    },
    isCurrent(token: number) {
      return token === epoch;
    },
    invalidate() {
      epoch++;
      pending = undefined;
    },
  };
}
