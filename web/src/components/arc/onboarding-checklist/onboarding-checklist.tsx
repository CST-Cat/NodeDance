"use client";

import { useEffect, useId, useReducer, useRef, useState, useSyncExternalStore } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { AnimatePresence, motion, useIsPresent, useReducedMotion } from "motion/react";
import type { Transition, Variants } from "motion/react";
import { ArrowRight, ChevronUp } from "lucide-react";
import { motionTokens } from "../lib/motion-tokens";
import styles from "./onboarding-checklist.module.css";

export interface OnboardingStep {
  id: string;
  title: string;
  description?: string;
  /** Label of the button that does the step. Without it, the step is checked off by hand. */
  actionLabel?: string;
  /** Runs the step. Resolve to check it off; throw an Error to keep it open and show the error's message. */
  onAction?: () => void | Promise<void>;
}

/**
 * A getting started checklist for a new account or workspace. The ring and count follow every check, finished steps
 * strike through and fold into a completed row that opens again, and the next step opens and takes focus.
 * Use it for a short, one time setup of three to eight steps; use a step form when the order is strict.
 */
export interface OnboardingChecklistProps {
  title: string;
  /** Title once every step is done. */
  doneTitle?: string;
  steps: OnboardingStep[];
  defaultCompleted?: string[];
  onCompletedChange?: (completed: string[]) => void;
  defaultHidden?: boolean;
  onHiddenChange?: (hidden: boolean) => void;
  className?: string;
}

type State = { done: string[]; settled: string[]; expanded: string | null; pending: string[]; errors: Record<string, string>; groupOpen: boolean; hidden: boolean; message: string };
type Action =
  | { type: "check" | "uncheck" | "settle" | "expand" | "start"; id: string; steps: OnboardingStep[] }
  | { type: "fail"; id: string; message: string }
  | { type: "group" | "hide" };

/** How long a checked step stays in place, struck through, before it folds into the completed row. */
const SETTLE_MS = 720;
const enter = [...motionTokens.ease.enter] as [number, number, number, number];
const standard = [...motionTokens.ease.standard] as [number, number, number, number];
const subscribe = () => () => {};
/** False on the server and during hydration, so reduced motion never changes the first client render. */
const useHydrated = () => useSyncExternalStore(subscribe, () => true, () => false);

const ordered = (ids: string[], steps: OnboardingStep[]) => steps.map(step => step.id).filter(id => ids.includes(id));
const without = (ids: string[], id: string) => ids.filter(item => item !== id);
const omit = (errors: Record<string, string>, id: string) => Object.fromEntries(Object.entries(errors).filter(([key]) => key !== id));
const titleOf = (steps: OnboardingStep[], id: string) => steps.find(step => step.id === id)?.title ?? "Step";

function init({ steps, completed, hidden }: { steps: OnboardingStep[]; completed: string[]; hidden: boolean }): State {
  const done = ordered(completed, steps);
  return { done, settled: done, expanded: steps.find(step => !done.includes(step.id))?.id ?? null, pending: [], errors: {}, groupOpen: false, hidden, message: "" };
}

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "check": {
      if (state.done.includes(action.id)) return state;
      const done = ordered([...state.done, action.id], action.steps);
      const total = action.steps.length;
      return { ...state, done, pending: without(state.pending, action.id), errors: omit(state.errors, action.id), message: done.length === total ? `All ${total} steps done.` : `${titleOf(action.steps, action.id)} done. ${done.length} of ${total} complete.` };
    }
    case "uncheck": {
      if (!state.done.includes(action.id)) return state;
      const done = without(state.done, action.id);
      return { ...state, done, settled: without(state.settled, action.id), expanded: state.expanded ?? action.id, message: `${titleOf(action.steps, action.id)} marked as not done. ${done.length} of ${action.steps.length} complete.` };
    }
    case "settle": {
      if (!state.done.includes(action.id) || state.settled.includes(action.id)) return state;
      let expanded = state.expanded;
      // The step that just finished hands the open panel to the next unfinished step, wrapping to the top.
      if (expanded === action.id) {
        const order = action.steps.map(step => step.id), start = order.indexOf(action.id);
        expanded = order.map((_, offset) => order[(start + offset + 1) % order.length]!).find(id => !state.done.includes(id)) ?? null;
      }
      return { ...state, settled: ordered([...state.settled, action.id], action.steps), expanded };
    }
    case "expand": return { ...state, expanded: state.expanded === action.id ? null : action.id };
    case "start": return { ...state, pending: [...without(state.pending, action.id), action.id], errors: omit(state.errors, action.id) };
    case "fail": return { ...state, pending: without(state.pending, action.id), errors: { ...state.errors, [action.id]: action.message }, message: action.message };
    case "group": return { ...state, groupOpen: !state.groupOpen };
    case "hide": return { ...state, hidden: !state.hidden };
  }
}

/** Rows open and close their height on a spring that never overshoots; closing is a little quicker than opening. */
const collapse = (reduced: boolean) => ({
  initial: { height: 0, opacity: 0 },
  animate: { height: "auto", opacity: 1, transition: reduced ? { height: { duration: 0 }, opacity: { duration: .15 } } : { height: motionTokens.spring.smooth, opacity: { duration: motionTokens.duration.standard, ease: enter, delay: .04 } } },
  exit: { height: 0, opacity: 0, transition: reduced ? { duration: 0 } : { height: { ...motionTokens.spring.smooth, visualDuration: .32 }, opacity: { duration: motionTokens.duration.fast, ease: standard } } },
});
const rise: Variants = {
  enter: (direction: number) => ({ opacity: 0, y: `${.3 * direction}em`, filter: `blur(${motionTokens.blur.soft}px)` }),
  center: { opacity: 1, y: "0em", filter: "blur(0px)", transition: { duration: .22, ease: enter } },
  exit: (direction: number) => ({ opacity: 0, y: `${-.3 * direction}em`, filter: `blur(${motionTokens.blur.subtle}px)`, transition: { duration: .15, ease: standard } }),
};
const fade: Variants = { enter: { opacity: 0 }, center: { opacity: 1, transition: { duration: .15 } }, exit: { opacity: 0, transition: { duration: .1 } } };
const roll: Variants = {
  enter: (direction: number) => ({ opacity: 0, y: `${.45 * direction}em`, filter: `blur(${motionTokens.blur.subtle}px)` }),
  center: { opacity: 1, y: "0em", filter: "blur(0px)", transition: { y: motionTokens.spring.snappy, opacity: { duration: motionTokens.duration.fast }, filter: { duration: motionTokens.duration.fast } } },
  exit: (direction: number) => ({ opacity: 0, y: `${-.45 * direction}em`, filter: `blur(${motionTokens.blur.subtle}px)`, transition: { duration: .14, ease: standard } }),
};
const iconSwap = (reduced: boolean) => reduced
  ? { initial: { opacity: 0 }, animate: { opacity: 1, transition: { duration: .15 } }, exit: { opacity: 0, transition: { duration: .1 } } }
  : { initial: { opacity: 0, scale: .6, filter: `blur(${motionTokens.blur.subtle}px)` }, animate: { opacity: 1, scale: 1, filter: "blur(0px)", transition: { scale: motionTokens.spring.snappy, opacity: { duration: motionTokens.duration.fast, ease: enter }, filter: { duration: motionTokens.duration.fast, ease: enter } } }, exit: { opacity: 0, scale: .6, filter: `blur(${motionTokens.blur.subtle}px)`, transition: { duration: .12, ease: standard } } };

/** Every label the slot can show sits invisibly underneath, so a swap never changes the slot's size. */
function SwapText({ text, options, reduced }: { text: string; options: string[]; reduced: boolean }) {
  return <span className={styles.swap} aria-hidden="true">
    {options.map(option => <span key={option} className={styles.swapSizer}>{option}</span>)}
    <AnimatePresence mode="popLayout" initial={false} custom={1}>
      <motion.span key={text} className={styles.swapText} custom={1} variants={reduced ? fade : rise} initial="enter" animate="center" exit="exit">{text}</motion.span>
    </AnimatePresence>
  </span>;
}

/** Digits turn in the direction the count moved, one column per place value. */
function RollingNumber({ value, reduced }: { value: number; reduced: boolean }) {
  const [track, setTrack] = useState({ value, direction: 1 });
  if (track.value !== value) setTrack({ value, direction: value > track.value ? 1 : -1 });
  const digits = String(value).split("");
  return <span className={styles.number} aria-hidden="true">
    <AnimatePresence initial={false} custom={track.direction}>
      {digits.map((digit, index) => <motion.span key={digits.length - 1 - index} className={styles.place} initial={{ width: 0, opacity: 0 }} animate={{ width: "auto", opacity: 1 }} exit={{ width: 0, opacity: 0 }} transition={reduced ? { duration: 0 } : { width: motionTokens.spring.morph, opacity: { duration: motionTokens.duration.fast } }}>
        <AnimatePresence mode="popLayout" initial={false} custom={track.direction}>
          <motion.span key={digit} className={styles.digit} custom={track.direction} variants={reduced ? fade : roll} initial="enter" animate="center" exit="exit">{digit}</motion.span>
        </AnimatePresence>
      </motion.span>)}
    </AnimatePresence>
  </span>;
}

/** The arc fills on a spring as steps are checked; once every step is done a tick draws in its center. */
function ProgressRing({ done, total, reduced }: { done: number; total: number; reduced: boolean }) {
  const ratio = total ? done / total : 0;
  const complete = total > 0 && done === total;
  return <span className={styles.ring} role="progressbar" aria-label="Setup progress" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-valuetext={`${done} of ${total} steps done`}>
    <svg className={styles.ringArc} viewBox="0 0 40 40" fill="none" aria-hidden="true">
      <circle className={styles.ringTrack} cx="20" cy="20" r="17" strokeWidth={3} />
      <g transform="rotate(-90 20 20)"><motion.circle className={styles.ringFill} cx="20" cy="20" r="17" strokeWidth={3} strokeLinecap="round" initial={false} animate={{ pathLength: ratio, opacity: ratio > 0 ? 1 : 0 }} transition={reduced ? { duration: 0 } : { pathLength: motionTokens.spring.morph, opacity: { duration: motionTokens.duration.instant } }} /></g>
    </svg>
    <AnimatePresence initial={false}>
      {complete ? <motion.svg key="tick" className={styles.ringTick} viewBox="0 0 40 40" fill="none" aria-hidden="true" {...iconSwap(reduced)}>
        <motion.path d="M13.5 20.5 18 25l8.5-9.5" stroke="currentColor" strokeWidth={2.25} strokeLinecap="round" strokeLinejoin="round" initial={reduced ? false : { pathLength: 0 }} animate={{ pathLength: 1 }} transition={{ duration: motionTokens.duration.standard, ease: enter, delay: .12 }} />
      </motion.svg> : null}
    </AnimatePresence>
  </span>;
}

/** The fill grows from the center while the tick draws from its short stroke; unchecking retracts both quickly. */
function CheckButton({ checked, labelledBy, onToggle, reduced }: { checked: boolean; labelledBy: string; onToggle: () => void; reduced: boolean }) {
  const quick: Transition = { duration: motionTokens.duration.fast, ease: standard };
  return <button type="button" role="checkbox" aria-checked={checked} aria-labelledby={labelledBy} className={styles.check} data-check="" onClick={onToggle}>
    <span className={styles.checkVisual} aria-hidden="true">
      <motion.span className={styles.checkFill} initial={false} animate={{ scale: checked ? 1 : .6, opacity: checked ? 1 : 0 }} transition={reduced ? { duration: 0 } : { scale: motionTokens.spring.snappy, opacity: { duration: checked ? motionTokens.duration.instant : motionTokens.duration.fast } }} />
      <svg className={styles.checkMark} viewBox="0 0 22 22" fill="none">
        <motion.path d="M6.75 11.25 9.75 14.25 15.25 8.25" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" initial={false} animate={{ pathLength: checked ? 1 : 0, opacity: checked ? 1 : 0 }} transition={reduced ? { duration: 0 } : checked ? { pathLength: { duration: motionTokens.duration.standard, ease: enter, delay: .05 }, opacity: { duration: .05, delay: .05 } } : { pathLength: quick, opacity: quick }} />
      </svg>
    </span>
  </button>;
}

function ActionIcon({ state, reduced }: { state: "idle" | "pending" | "done"; reduced: boolean }) {
  const icon: ReactNode = state === "pending" ? <span className={styles.spinner} /> : state === "done"
    ? <svg width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"><motion.path d="M5 12.5l4.5 4.5L19 7.5" initial={reduced ? false : { pathLength: 0 }} animate={{ pathLength: 1 }} transition={{ duration: motionTokens.duration.standard, ease: enter, delay: .05 }} /></svg>
    : <ArrowRight size={16} strokeWidth={1.75} />;
  return <span className={styles.actionIcon} aria-hidden="true">
    <AnimatePresence initial={false}><motion.span key={state} className={styles.actionGlyph} {...iconSwap(reduced)}>{icon}</motion.span></AnimatePresence>
  </span>;
}

interface StepItemProps { step: OnboardingStep; baseId: string; done: boolean; expanded: boolean; pending: boolean; error?: string; reduced: boolean; onToggle: () => void; onExpand: () => void; onRun: () => void }

function StepItem({ step, baseId, done, expanded, pending, error, reduced, onToggle, onExpand, onRun }: StepItemProps) {
  const present = useIsPresent();
  const titleId = `${baseId}-${step.id}-title`, panelId = `${baseId}-${step.id}-panel`;
  const actionState = done ? "done" : pending ? "pending" : "idle";
  // A leaving step is inert, so neither Tab nor the arrow keys land on it while it folds away.
  return <motion.li className={styles.step} data-step={step.id} data-done={done || undefined} data-current={expanded || undefined} data-leaving={present ? undefined : ""} inert={!present} {...collapse(reduced)}>
    <div className={styles.stepInner}>
      <CheckButton checked={done} labelledBy={titleId} onToggle={onToggle} reduced={reduced} />
      <div className={styles.stepMain}>
        <button type="button" className={styles.stepTitle} data-title="" aria-expanded={expanded} aria-controls={panelId} onClick={onExpand}><span id={titleId} className={styles.titleText}>{step.title}</span></button>
        <AnimatePresence initial={false}>
          {expanded ? <motion.div key="panel" id={panelId} role="region" aria-labelledby={titleId} className={styles.panel} {...collapse(reduced)}>
            <div className={styles.panelInner}>
              {step.description ? <p className={styles.description}>{step.description}</p> : null}
              <AnimatePresence initial={false}>
                {error ? <motion.div key="error" className={styles.errorSlot} {...collapse(reduced)}><p className={styles.error} role="alert">{error}</p></motion.div> : null}
              </AnimatePresence>
              {step.actionLabel ? <button type="button" className={styles.action} data-primary="" data-state={actionState} aria-disabled={actionState !== "idle" || undefined} aria-busy={pending || undefined} onClick={onRun}>
                {step.actionLabel}<ActionIcon state={actionState} reduced={reduced} />
              </button> : null}
            </div>
          </motion.div> : null}
        </AnimatePresence>
      </div>
    </div>
  </motion.li>;
}

function CompletedItem({ step, baseId, reduced, onToggle }: { step: OnboardingStep; baseId: string; reduced: boolean; onToggle: () => void }) {
  const present = useIsPresent();
  const titleId = `${baseId}-${step.id}-done`;
  return <motion.li className={styles.step} data-completed-step={step.id} data-done="" inert={!present} {...collapse(reduced)}>
    <div className={styles.stepInner}>
      <CheckButton checked labelledBy={titleId} onToggle={onToggle} reduced={reduced} />
      <span className={styles.completedTitle}><span id={titleId} className={styles.titleText}>{step.title}</span></span>
    </div>
  </motion.li>;
}

export function OnboardingChecklist({ title, doneTitle = "You’re all set", steps, defaultCompleted = [], onCompletedChange, defaultHidden = false, onHiddenChange, className }: OnboardingChecklistProps) {
  const baseId = useId();
  const hydrated = useHydrated();
  const prefersReduced = useReducedMotion();
  const reduced = hydrated && !!prefersReduced;
  const [state, dispatch] = useReducer(reducer, { steps, completed: defaultCompleted, hidden: defaultHidden }, init);
  const root = useRef<HTMLElement>(null);
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const focusRequest = useRef<"next" | { id: string } | null>(null);
  const reportCompleted = useRef(onCompletedChange);
  const reported = useRef(state.done);

  useEffect(() => { reportCompleted.current = onCompletedChange; });
  useEffect(() => {
    if (reported.current === state.done) return;
    reported.current = state.done;
    reportCompleted.current?.(state.done);
  }, [state.done]);
  useEffect(() => { const pending = timers.current; return () => pending.forEach(clearTimeout); }, []);
  // Focus moves after the commit that mounts its target: the next step's action, or the step reopened from the completed row.
  useEffect(() => {
    const request = focusRequest.current, node = root.current;
    if (!request || !node) return;
    focusRequest.current = null;
    let target: HTMLElement | null = null;
    if (request === "next") {
      const next = state.expanded ? node.querySelector<HTMLElement>(`[data-step="${state.expanded}"]:not([data-leaving])`) : null;
      target = next?.querySelector<HTMLElement>("[data-primary]") ?? next?.querySelector<HTMLElement>("[data-title]") ?? node.querySelector<HTMLElement>("[data-group-toggle]");
    } else target = node.querySelector<HTMLElement>(`[data-step="${request.id}"]:not([data-leaving]) [data-check]`);
    target?.focus({ preventScroll: true });
  });

  function check(id: string) {
    dispatch({ type: "check", id, steps });
    if (timers.current.has(id)) return;
    timers.current.set(id, setTimeout(() => {
      timers.current.delete(id);
      if (root.current?.querySelector(`[data-step="${id}"]`)?.contains(document.activeElement)) focusRequest.current = "next";
      dispatch({ type: "settle", id, steps });
    }, SETTLE_MS));
  }

  function uncheck(id: string, fromGroup: boolean) {
    clearTimeout(timers.current.get(id));
    timers.current.delete(id);
    if (fromGroup) focusRequest.current = { id };
    dispatch({ type: "uncheck", id, steps });
  }

  async function run(step: OnboardingStep) {
    if (!step.onAction || state.pending.includes(step.id) || state.done.includes(step.id)) return;
    dispatch({ type: "start", id: step.id, steps });
    try {
      await step.onAction();
      check(step.id);
    } catch (error) {
      dispatch({ type: "fail", id: step.id, message: error instanceof Error && error.message ? error.message : "That didn’t work. Try again." });
    }
  }

  function toggleHidden() {
    dispatch({ type: "hide" });
    onHiddenChange?.(!state.hidden);
  }

  /** Up and Down walk the step titles, Home and End jump to the first and last. */
  function navigate(event: KeyboardEvent<HTMLUListElement>) {
    const target = event.target as HTMLElement;
    if (!target.matches("[data-title]") || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const titles = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("li:not([data-leaving]) [data-title]"));
    const index = titles.indexOf(target);
    const next = event.key === "Home" ? 0 : event.key === "End" ? titles.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + titles.length) % titles.length;
    event.preventDefault();
    titles[next]?.focus();
  }

  const total = steps.length, doneCount = state.done.length, allDone = total > 0 && doneCount === total;
  const open = steps.filter(step => !state.settled.includes(step.id));
  const completed = steps.filter(step => state.settled.includes(step.id));
  const titleId = `${baseId}-title`, bodyId = `${baseId}-body`, groupId = `${baseId}-completed`;
  const bodyTransition: Transition = reduced ? { duration: 0 } : state.hidden
    ? { height: { ...motionTokens.spring.smooth, visualDuration: .34 }, opacity: { duration: motionTokens.duration.fast, ease: standard } }
    : { height: motionTokens.spring.smooth, opacity: { duration: motionTokens.duration.standard, ease: enter, delay: .05 } };

  return <section ref={root} className={[styles.card, className].filter(Boolean).join(" ")} aria-labelledby={titleId} data-complete={allDone || undefined} data-hidden={state.hidden || undefined}>
    <div className={styles.header}>
      <ProgressRing done={doneCount} total={total} reduced={reduced} />
      <div className={styles.heading}>
        <h3 id={titleId} className={styles.title}><span className={styles.srOnly}>{allDone ? doneTitle : title}</span><SwapText text={allDone ? doneTitle : title} options={[title, doneTitle]} reduced={reduced} /></h3>
        <p className={styles.count}><span className={styles.srOnly}>{doneCount} of {total} done</span><span className={styles.countVisual} aria-hidden="true"><RollingNumber value={doneCount} reduced={reduced} />&nbsp;of {total} done</span></p>
      </div>
      <button type="button" className={styles.hide} aria-expanded={!state.hidden} aria-controls={bodyId} onClick={toggleHidden}>
        <span className={styles.hideLabel}><SwapText text={state.hidden ? "Show" : "Hide"} options={["Hide", "Show"]} reduced={reduced} /></span>
        <span className={styles.srOnly}>{state.hidden ? "Show" : "Hide"} checklist</span>
        <motion.span className={styles.chevron} aria-hidden="true" initial={false} animate={{ rotate: state.hidden ? 180 : 0 }} transition={reduced ? { duration: 0 } : motionTokens.spring.snappy}><ChevronUp size={16} strokeWidth={1.75} /></motion.span>
      </button>
    </div>
    <motion.div id={bodyId} className={styles.body} initial={false} animate={state.hidden ? { height: 0, opacity: 0 } : { height: "auto", opacity: 1 }} transition={bodyTransition} inert={state.hidden}>
      <div className={styles.bodyInner}>
        <ul className={styles.list} aria-label="Steps to do" aria-hidden={open.length ? undefined : true} onKeyDown={navigate}>
          <AnimatePresence initial={false}>
            {open.map(step => <StepItem key={step.id} step={step} baseId={baseId} done={state.done.includes(step.id)} expanded={state.expanded === step.id} pending={state.pending.includes(step.id)} error={state.errors[step.id]} reduced={reduced}
              onToggle={() => (state.done.includes(step.id) ? uncheck(step.id, false) : check(step.id))} onExpand={() => dispatch({ type: "expand", id: step.id, steps })} onRun={() => run(step)} />)}
          </AnimatePresence>
        </ul>
        <AnimatePresence initial={false}>
          {completed.length ? <motion.div key="completed" className={styles.group} {...collapse(reduced)}>
            <button type="button" className={styles.groupToggle} data-group-toggle="" aria-expanded={state.groupOpen} aria-controls={groupId} onClick={() => dispatch({ type: "group" })}>
              <span className={styles.srOnly}>{completed.length} completed</span>
              <span className={styles.groupLabel} aria-hidden="true"><RollingNumber value={completed.length} reduced={reduced} />&nbsp;completed</span>
              <motion.span className={styles.chevron} aria-hidden="true" initial={false} animate={{ rotate: state.groupOpen ? 0 : 180 }} transition={reduced ? { duration: 0 } : motionTokens.spring.snappy}><ChevronUp size={16} strokeWidth={1.75} /></motion.span>
            </button>
            <AnimatePresence initial={false}>
              {state.groupOpen ? <motion.div key="list" id={groupId} className={styles.groupPanel} {...collapse(reduced)}>
                <ul className={styles.list} aria-label="Completed steps">
                  <AnimatePresence initial={false}>
                    {completed.map(step => <CompletedItem key={step.id} step={step} baseId={baseId} reduced={reduced} onToggle={() => uncheck(step.id, true)} />)}
                  </AnimatePresence>
                </ul>
              </motion.div> : null}
            </AnimatePresence>
          </motion.div> : null}
        </AnimatePresence>
      </div>
    </motion.div>
    <span className={styles.srOnly} role="status">{state.message}</span>
  </section>;
}

export default OnboardingChecklist;
