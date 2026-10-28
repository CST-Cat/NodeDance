export interface AsyncOperation {
  run<T>(task: () => Promise<T>): Promise<T | undefined>;
  begin(): number;
  isCurrent(token: number): boolean;
  invalidate(): void;
}

export function createAsyncOperation(): AsyncOperation {
  let epoch = 0;
  return {
    async run<T>(task: () => Promise<T>): Promise<T | undefined> {
      const own = ++epoch;
      try {
        const result = await task();
        return own === epoch ? result : undefined;
      } catch (reason) {
        if (own !== epoch) return undefined;
        throw reason;
      }
    },
    begin() {
      return ++epoch;
    },
    isCurrent(token: number) {
      return token === epoch;
    },
    invalidate() {
      epoch++;
    },
  };
}
