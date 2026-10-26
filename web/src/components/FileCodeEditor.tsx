import { useEffect, useRef, useState } from "react";
import { basicSetup } from "codemirror";
import { Compartment, EditorState, Transaction } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { HighlightStyle, LanguageDescription, syntaxHighlighting } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { closeCompletion, completionStatus } from "@codemirror/autocomplete";
import { redo, redoDepth, undo, undoDepth, indentWithTab } from "@codemirror/commands";
import { closeSearchPanel, openSearchPanel, searchPanelOpen } from "@codemirror/search";
import { tags } from "@lezer/highlight";
import { codeDocument, codeSourceField, codeSourceHistory, restoreCodeSource } from "../file-code-text";
import { useSignals } from "@preact/signals-react/runtime";
import { Button } from "./arc/button/button";
import { locale, t } from "../i18n";

const theme = EditorView.theme({
  "&.cm-focused": { outlineColor: "var(--border-strong)" },
  "&": { height: "min(440px, 55dvh)", color: "var(--text)", backgroundColor: "var(--surface)" },
  ".cm-scroller": { overflow: "auto", fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace" },
  ".cm-content": { caretColor: "var(--text)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--text)" },
  ".cm-gutters": { color: "var(--text-muted)", backgroundColor: "var(--surface-muted)", borderColor: "var(--border)" },
  ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--surface-muted)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "var(--surface-selected)" },
  ".cm-searchMatch": { backgroundColor: "var(--warning-soft)" },
  ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "var(--warning-soft)", outlineColor: "var(--warning)" },
  ".cm-matchingBracket, .cm-selectionMatch": { backgroundColor: "var(--surface-selected)" },
  ".cm-panels, .cm-tooltip": { color: "var(--text)", backgroundColor: "var(--surface-raised)", borderColor: "var(--border)" },
  ".cm-textfield": { color: "var(--text)", backgroundColor: "var(--surface)", borderColor: "var(--border)" },
  ".cm-button": { color: "var(--text)", backgroundImage: "none", backgroundColor: "var(--surface)", borderColor: "var(--border)" },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": { color: "var(--text)", backgroundColor: "var(--surface-selected)" }
});

const highlighting = HighlightStyle.define([
  { tag: [tags.keyword, tags.modifier, tags.controlKeyword], color: "var(--accent-fill)" },
  { tag: [tags.string, tags.regexp, tags.inserted], color: "var(--success)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--warning)" },
  { tag: [tags.function(tags.variableName), tags.typeName, tags.className, tags.tagName], color: "var(--accent-fill)" },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--text-secondary)" },
  { tag: [tags.comment, tags.meta], color: "var(--text-muted)" },
  { tag: tags.deleted, color: "var(--danger)" }
]);

const editorPhrases = () => ({
  "Find": t("files.editor.find"),
  "Replace": t("files.editor.replaceTitle"),
  "next": t("files.editor.next"),
  "previous": t("files.editor.previous"),
  "all": t("files.editor.all"),
  "match case": t("files.editor.matchCase"),
  "regexp": t("files.editor.regexp"),
  "by word": t("files.editor.byWord"),
  "replace": t("files.editor.replace"),
  "replace all": t("files.editor.replaceAll"),
  "close": t("files.editor.close"),
  "Go to line": t("files.editor.goToLine"),
  "go": t("files.editor.go"),
  "Fold line": t("files.editor.fold"),
  "Unfold line": t("files.editor.unfold"),
  "$ fold line": t("files.editor.foldLine"),
  "$ unfold line": t("files.editor.unfoldLine"),
  "to": t("files.editor.to")
});

export default function FileCodeEditor({ path, value, onChange, readOnly = false, disabled = false, onSave }: { path: string; value: string; onChange?: (value: string) => void; readOnly?: boolean; disabled?: boolean; onSave?: () => void }) {
  useSignals();
  const attributes = { role: "textbox", "aria-multiline": "true", "aria-label": t("files.ui.contents"), "aria-readonly": String(readOnly), "aria-disabled": String(disabled), spellcheck: "false", tabindex: "0" };
  const [history, setHistory] = useState({ undo: false, redo: false });
  const host = useRef<HTMLDivElement>(null), view = useRef<EditorView | null>(null);
  const save = useRef(onSave); save.current = onSave;
  const source = useRef(value), callback = useRef(onChange), syncing = useRef(false);
  const access = useRef(new Compartment()), language = useRef(new Compartment()), phrases = useRef(new Compartment());
  callback.current = onChange;
  useEffect(() => {
    source.current = value;
    const editor = new EditorView({
      parent: host.current!,
      state: EditorState.create({ doc: codeDocument(value), extensions: [codeSourceField.init(() => value), codeSourceHistory, basicSetup, keymap.of([{ key: "Mod-s", run: editor => { if (save.current && !editor.state.readOnly) save.current(); return true; } }, indentWithTab]), EditorView.lineWrapping, theme, syntaxHighlighting(highlighting), language.current.of([]), phrases.current.of(EditorState.phrases.of(editorPhrases())), access.current.of([EditorState.readOnly.of(readOnly || disabled), EditorView.editable.of(!readOnly && !disabled), EditorView.contentAttributes.of(attributes)]), EditorView.updateListener.of(update => {
        const undo = undoDepth(update.state) > 0, redo = redoDepth(update.state) > 0;
        setHistory(previous => previous.undo === undo && previous.redo === redo ? previous : { undo, redo });
        if (syncing.current || !update.docChanged) return;
        source.current = update.state.field(codeSourceField);
        callback.current?.(source.current);
      })] })
    });
    view.current = editor;
    return () => { view.current = null; editor.destroy(); };
  }, [path]);
  useEffect(() => {
    const editor = view.current;
    if (!editor || value === source.current) return;
    source.current = value;
    syncing.current = true;
    editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: codeDocument(value) }, effects: restoreCodeSource.of(value), annotations: Transaction.addToHistory.of(false) });
    syncing.current = false;
  }, [value, path]);
  useEffect(() => {
    const editor = view.current;
    if (!editor) return;
    editor.dispatch({ effects: [access.current.reconfigure([EditorState.readOnly.of(readOnly || disabled), EditorView.editable.of(!readOnly && !disabled), EditorView.contentAttributes.of(attributes)]), phrases.current.reconfigure(EditorState.phrases.of(editorPhrases()))] });
  }, [readOnly, disabled, locale.value, path]);
  useEffect(() => {
    const editor = view.current;
    if (!editor) return;
    let active = true;
    const name = path.split("/").pop() ?? path;
    const description = LanguageDescription.matchFilename(languages, name) ?? (/^\.(env(?:\.|$)|bashrc$|zshrc$|profile$)/i.test(name) ? LanguageDescription.matchLanguageName(languages, "Shell") : undefined);
    editor.dom.dataset.language = description?.name ?? "text";
    if (description) void description.load().then(extension => { if (active) editor.dispatch({ effects: language.current.reconfigure(extension) }); }).catch(() => { if (active) editor.dom.dataset.language = "text"; });
    return () => { active = false; };
  }, [path]);
  return <div className="file-code-panel"><div className="file-code-toolbar">{!readOnly && <><Button type="button" size="sm" variant="secondary" disabled={disabled || !history.undo} onClick={() => { if (view.current) { undo(view.current); view.current.focus(); } }}>{t("files.editor.undo")}</Button><Button type="button" size="sm" variant="secondary" disabled={disabled || !history.redo} onClick={() => { if (view.current) { redo(view.current); view.current.focus(); } }}>{t("files.editor.redo")}</Button></>}<Button type="button" size="sm" variant="secondary" disabled={disabled} onClick={() => { if (view.current) openSearchPanel(view.current); }}>{t("files.editor.find")}</Button></div><div className="file-code-editor" ref={host} onKeyDownCapture={event => {
    if (event.key === "Escape" && view.current && (searchPanelOpen(view.current.state) || completionStatus(view.current.state))) { event.stopPropagation(); event.preventDefault(); closeSearchPanel(view.current); closeCompletion(view.current); view.current.focus(); }
  }} /></div>;
}
