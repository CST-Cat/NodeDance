import assert from 'node:assert/strict'
import { EditorState } from '@codemirror/state'
import { history, undo, redo } from '@codemirror/commands'
import { codeDocument, codeSourceField, codeSourceHistory } from '../src/file-code-text.ts'
for (const source of ['alpha\nbeta\n', 'alpha\r\nbeta\r\n', 'alpha\r\nbeta\ngamma\rdelta', '\uFEFF中文🐈\r\nlast']) {
  let state = EditorState.create({doc: codeDocument(source), extensions: [codeSourceField.init(() => source), codeSourceHistory, history()]})
  const original = state
  const transaction = state.update({changes: [{from: 0, to: 1, insert: 'X'}, {from: state.doc.line(2).from, insert: 'new\n'}]})
  state = transaction.state
  assert.equal(codeDocument(state.field(codeSourceField)), state.doc.toString())
  const edited = state.field(codeSourceField)
  const target = {get state() {return state}, dispatch(transaction) {state = transaction.state}}
  assert.ok(undo(target))
  assert.equal(state.field(codeSourceField), source)
  assert.equal(state.doc.toString(), original.doc.toString())
  assert.ok(redo(target))
  assert.equal(state.field(codeSourceField), edited)
}
console.log('Code editing preserves CRLF, mixed endings, BOM and Unicode through edits, undo and redo.')

for (const source of ['a\r\nb\nc\rd', 'a\r\nb\r\n', 'a\nb\n']) {
  let state = EditorState.create({doc: codeDocument(source), extensions: [codeSourceField.init(() => source), codeSourceHistory, history()]})
  const target = {get state() {return state}, dispatch(transaction) {state = transaction.state}}
  state = state.update({changes: {from: 0, to: state.doc.length, insert: 'replaced\ntext'}, userEvent: 'input'}).state
  const edited = state.field(codeSourceField)
  assert.ok(undo(target));assert.equal(state.field(codeSourceField), source)
  assert.ok(redo(target));assert.equal(state.field(codeSourceField), edited)
}

{
 const source='a\r\n\nc\r'
 let state=EditorState.create({doc:codeDocument(source),extensions:[codeSourceField.init(()=>source),codeSourceHistory,history()]})
 state=state.update({changes:{from:1,to:2},userEvent:'delete.backward'}).state
 state=state.update({changes:{from:1,to:2},userEvent:'delete.backward'}).state
 const edited=state.field(codeSourceField)
 const target={get state(){return state},dispatch(transaction){state=transaction.state}}
 assert.ok(undo(target));assert.equal(state.field(codeSourceField),source)
 assert.ok(redo(target));assert.equal(state.field(codeSourceField),edited)
}
