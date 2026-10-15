import {
  computed,
  effect,
  signal,
  untracked,
  type Signal,
} from "@preact/signals-react";
import { useSignals } from "@preact/signals-react/runtime";
import {
  Children,
  cloneElement,
  createContext,
  Fragment,
  isValidElement,
  lazy,
  Suspense,
  useContext,
  useId,
  useLayoutEffect,
  useState,
  type CSSProperties,
  type ReactElement,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { t } from "./i18n";
export type Cell<T> = Signal<T>;
export type ContextKey<T> = symbol & { readonly __type?: T };
interface Scope {
  mount: (() => unknown)[];
  dispose: (() => unknown)[];
  provided: Map<symbol, unknown>;
  inherited: Map<symbol, unknown>;
  attrs: Record<string, unknown>;
  id: string;
  refs: WeakMap<object, (element: any) => void>;
}
const Environment = createContext(new Map<symbol, unknown>());
let current: Scope | undefined,
  serial = 0;
const proxies = new WeakMap<object, object>();
export function observable<T extends object>(object: T): T {
  if (
    !object ||
    typeof object !== "object" ||
    "$$typeof" in object ||
    (!Array.isArray(object) &&
      Object.getPrototypeOf(object) !== Object.prototype)
  )
    return object;
  const cached = proxies.get(object);
  if (cached) return cached as T;
  const cells = new Map<PropertyKey, Signal<unknown>>(),
    shape = signal(0);
  const field = (key: PropertyKey) => {
    let value = cells.get(key);
    if (!value) {
      value = signal(Reflect.get(object, key));
      cells.set(key, value);
    }
    return value;
  };
  const proxy = new Proxy(object, {
    get(target, key, receiver) {
      field(key).value;
      const value = Reflect.get(target, key, receiver);
      return value && typeof value === "object" ? observable(value) : value;
    },
    set(target, key, value) {
      const exists = Object.hasOwn(target, key),
        before = Reflect.get(target, key),
        length = Array.isArray(target) ? target.length : undefined;
      const result = Reflect.set(target, key, value);
      if (before !== value && cells.has(key)) cells.get(key)!.value = value;
      if (!exists) shape.value++;
      if (
        Array.isArray(target) &&
        target.length !== length &&
        cells.has("length")
      )
        cells.get("length")!.value = target.length;
      return result;
    },
    deleteProperty(target, key) {
      const exists = Object.hasOwn(target, key),
        result = Reflect.deleteProperty(target, key);
      if (exists) {
        if (cells.has(key)) cells.get(key)!.value = undefined;
        shape.value++;
      }
      return result;
    },
    ownKeys(target) {
      shape.value;
      return Reflect.ownKeys(target);
    },
  });
  proxies.set(object, proxy);
  proxies.set(proxy, proxy);
  return proxy;
}
export function cell<T>(): Signal<T | undefined>;
export function cell<T>(value: T): Signal<T>;
export function cell<T>(value?: T) {
  const inner = signal(
    value && typeof value === "object" ? observable(value) : value,
  );
  return {
    get value() {
      return inner.value;
    },
    set value(next: T | undefined) {
      inner.value = next && typeof next === "object" ? observable(next) : next;
    },
    peek: () => inner.peek(),
    subscribe: inner.subscribe.bind(inner),
  } as Signal<T | undefined>;
}
export const shallowCell = signal;
export function derived<T>(
  source: (() => T) | { get: () => T; set: (value: T) => void },
): Signal<T> {
  if (typeof source === "function") return computed(source) as Signal<T>;
  const value = computed(source.get);
  return {
    get value() {
      return value.value;
    },
    set value(next: T) {
      source.set(next);
    },
    peek: () => value.peek(),
  } as Signal<T>;
}
export function lazyView<T extends import("react").ComponentType<any>>(
  loader: () => Promise<{ default: T }>,
) {
  const View = lazy(loader);
  return function LazyContent(props: import("react").ComponentProps<T>) {
    return (
      <Suspense
        fallback={
          <div className="center-stage" role="status">
            {t("guest.loading")}
          </div>
        }
      >
        <View {...(props as any)} />
      </Suspense>
    );
  };
}
export function afterMount(callback: () => unknown) {
  if (!current) throw Error("Missing controller lifecycle");
  current.mount.push(callback);
}
export function beforeDispose(callback: () => unknown) {
  if (!current) throw Error("Missing controller lifecycle");
  current.dispose.push(callback);
}
export function afterRender<T = void>(callback?: () => T): Promise<T> {
  return new Promise((resolve) =>
    requestAnimationFrame(() => resolve(callback?.() as T)),
  );
}
function read(source: any): any {
  return typeof source === "function"
    ? source()
    : source && typeof source === "object" && "value" in source
      ? source.value
      : source;
}
function traverse(value: any, seen = new WeakSet<object>()) {
  if (!value || typeof value !== "object" || seen.has(value)) return;
  seen.add(value);
  if (Array.isArray(value) || Object.getPrototypeOf(value) === Object.prototype)
    for (const key of Object.keys(value)) traverse(value[key], seen);
}
export function watchState<T>(
  source: () => T,
  callback: (value: T, previous: T) => unknown,
  options?: { immediate?: boolean; deep?: boolean; flush?: string },
): () => void;
export function watchState<T>(
  source: Signal<T>,
  callback: (value: T, previous: T) => unknown,
  options?: { immediate?: boolean; deep?: boolean; flush?: string },
): () => void;
export function watchState<T extends readonly any[]>(
  source: T,
  callback: (value: any, previous: any) => unknown,
  options?: { immediate?: boolean; deep?: boolean; flush?: string },
): () => void;
export function watchState(
  source: any,
  callback: (value: any, previous: any) => unknown,
  options: any = {},
) {
  let stop = () => {},
    cancelled = false;
  const start = () => {
    let initialized = false,
      previous: any;
    stop = effect(() => {
      const value = Array.isArray(source) ? source.map(read) : read(source);
      if (options.deep) traverse(value);
      const changed =
        options.deep ||
        (!Array.isArray(value)
          ? !Object.is(value, previous)
          : !Array.isArray(previous) ||
            value.length !== previous.length ||
            value.some((v, i) => !Object.is(v, previous[i])));
      const run = initialized ? changed : options.immediate;
      const old = previous;
      previous = value;
      initialized = true;
      if (run)
        untracked(() => {
          if (options.flush === "post")
            void afterRender(() => {
              if (!cancelled) callback(value, old);
            });
          else callback(value, old);
        });
    });
  };
  if (current) {
    current.mount.push(start);
    current.dispose.push(() => {
      cancelled = true;
      stop();
    });
  } else start();
  return () => {
    cancelled = true;
    stop();
  };
}
export function trackEffect(callback: () => unknown) {
  let stop = () => {};
  const start = () => {
    stop = effect(() => {
      callback();
    });
  };
  if (current) {
    current.mount.push(start);
    current.dispose.push(() => stop());
  } else start();
  return () => stop();
}
export function provideValue<T>(key: ContextKey<T>, value: T) {
  if (!current) throw Error("Missing controller");
  current.provided.set(key, value);
}
export function consumeValue<T>(key: ContextKey<T>): T | undefined {
  return current?.provided.has(key)
    ? (current.provided.get(key) as T)
    : (current?.inherited.get(key) as T);
}
export function attributes() {
  return current?.attrs ?? {};
}
export function uniqueId() {
  return (current?.id ?? "global") + "-" + serial++;
}
export function __indeterminate(value: boolean) {
  return {
    ref: (element: HTMLInputElement | null) => {
      if (element) element.indeterminate = !!value;
    },
  };
}
export function __call(handler: (...values: any[]) => unknown, event: any) {
  return handler(event);
}
export function __classes(...values: any[]): string {
  return values
    .flatMap((value) =>
      !value
        ? []
        : typeof value === "string"
          ? [value]
          : Array.isArray(value)
            ? [__classes(...value)]
            : Object.entries(value)
                .filter(([, v]) => v)
                .map(([k]) => k),
    )
    .join(" ");
}
export function __style(...values: any[]): CSSProperties {
  return Object.assign(
    {},
    ...values.map((v) =>
      typeof v === "string"
        ? Object.fromEntries(
            v
              .split(";")
              .filter(Boolean)
              .map((s) => {
                const i = s.indexOf(":");
                return [s.slice(0, i).trim(), s.slice(i + 1).trim()];
              }),
          )
        : (v ?? {}),
    ),
  );
}
export function __each<T>(
  values: Iterable<T> | Record<string, T> | number | undefined | null,
  render: (value: T, key: any, index: number) => ReactNode,
): ReactNode {
  if (values == null) return null;
  const list =
    typeof values === "number"
      ? Array.from({ length: values }, (_, i) => [i, i + 1])
      : Symbol.iterator in Object(values)
        ? Array.from(values as Iterable<T>, (value, index) => [index, value])
        : Object.entries(values);
  return list.map(([key, value], i) => {
    const node = render(value as T, key, i);
    return isValidElement(node) && node.key == null
      ? cloneElement(node, { key: String(key) })
      : node;
  });
}
export function __portal(
  children: ReactNode,
  target: string,
  disabled: boolean,
) {
  return disabled
    ? children
    : createPortal(children, document.querySelector(target) ?? document.body);
}
export function __input(
  value: any,
  set: (value: any) => void,
  type: string,
  choice: any,
  modifiers: string[],
) {
  const update = (event: any) => {
    const target = event.target;
    let next = target.value;
    if (type === "checkbox") {
      next = Array.isArray(value)
        ? target.checked
          ? [...value, choice]
          : value.filter((v: any) => v !== choice)
        : target.checked;
    } else if (type === "radio") next = choice ?? next;
    else {
      if (modifiers.includes("trim")) next = next.trim();
      if (type === "number" || modifiers.includes("number")) {
        const number = parseFloat(next);
        if (!Number.isNaN(number)) next = number;
      }
    }
    set(next);
  };
  return type === "checkbox" || type === "radio"
    ? {
        checked: Array.isArray(value)
          ? value.includes(choice)
          : type === "radio"
            ? value === choice
            : !!value,
        onChange: update,
      }
    : { value: value ?? "", onChange: update };
}
export function useController<P extends Record<string, any>>(
  factory: (
    props: P,
    emit: (event: string, ...values: any[]) => void,
    dom: (cell: Signal<any>) => (element: any) => void,
  ) => () => ReactNode,
  input: P,
  declared: string[],
  events: string[],
  defaults: Partial<P> = {},
  inherit = true,
): ReactNode {
  const inherited = useContext(Environment),
    id = useId();
  const [model] = useState(() => {
    const props = observable({ ...defaults, ...input }) as P;
    const attrs = observable(
      Object.fromEntries(
        Object.entries(input).filter(
          ([key]) =>
            key !== "children" &&
            !declared.includes(key) &&
            !/^on[A-Z]/.test(key),
        ),
      ),
    );
    Object.defineProperty(attrs, "class", {
      get: () => attrs.className,
      enumerable: false,
      configurable: true,
    });
    const scope: Scope = {
      mount: [],
      dispose: [],
      provided: new Map(),
      inherited,
      attrs,
      id,
      refs: new WeakMap(),
    };
    const prior = current;
    current = scope;
    let render: () => ReactNode;
    const emit = (event: string, ...values: any[]) => {
      const name =
        "on" +
        event
          .replace(/[:\-]([a-z])/g, (_, c: string) => c.toUpperCase())
          .replace(/^./, (c) => c.toUpperCase());
      props[name]?.(...values);
    };
    const dom = (value: Signal<any>) => {
      let callback = scope.refs.get(value);
      if (!callback) {
        callback = (element) => {
          value.value = element;
        };
        scope.refs.set(value, callback);
      }
      return callback;
    };
    try {
      render = factory(props, emit, dom);
    } finally {
      current = prior;
    }
    return {
      props,
      attrs,
      scope,
      render,
      provided: new Map([...inherited, ...scope.provided]),
    };
  });
  useLayoutEffect(() => {
    const next = { ...defaults, ...input };
    untracked(() => {
      for (const key of new Set([
        ...Object.keys(model.props),
        ...Object.keys(next),
      ]))
        if (!Object.is(model.props[key], next[key]))
          Reflect.set(model.props, key, next[key]);
      for (const key of Object.keys(model.attrs))
        if (!(key in input)) delete model.attrs[key];
      for (const [key, value] of Object.entries(input))
        if (
          key !== "children" &&
          !declared.includes(key) &&
          !/^on[A-Z]/.test(key)
        )
          model.attrs[key] = value;
    });
  });
  useLayoutEffect(() => {
    for (const callback of model.scope.mount) callback();
    return () => {
      for (const callback of model.scope.dispose.slice().reverse()) callback();
    };
  }, [model]);
  const tracker = useSignals(1);
  let output: ReactNode;
  try {
    output = model.render();
  } finally {
    tracker.f();
  }
  while (isValidElement(output) && output.type === Fragment) {
    const children = Children.toArray(
      (output as ReactElement<any>).props.children,
    ).filter((v) => typeof v !== "string" || v.trim());
    if (children.length !== 1) break;
    output = children[0];
  }
  if (inherit && isValidElement(output) && output.type !== Fragment)
    output = cloneElement(output as ReactElement<any>, {
      ...model.attrs,
      ...(output as ReactElement<any>).props,
      className: __classes(
        model.attrs.className,
        (output as ReactElement<any>).props.className,
      ),
    });
  return model.scope.provided.size ? (
    <Environment.Provider value={model.provided}>{output}</Environment.Provider>
  ) : (
    output
  );
}
