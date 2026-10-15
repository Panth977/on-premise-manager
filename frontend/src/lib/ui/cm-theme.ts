/**
 * CodeMirror look shared by every editor of the app (CodeEditor, FileTemplateEditor, ContentCodeEditor): syntax colors
 * and the search / replace panel from the theme tokens, so both light and dark stay readable on --code-bg.
 * (CodeMirror's defaultHighlightStyle is light-only: dark blue booleans, dark red strings on a black background.)
 */
import type { TagStyle } from '@codemirror/language';
import type { tags as Tags } from '@lezer/highlight';

/** HighlightStyle.define(highlightSpec(tags)) */
export function highlightSpec(t: typeof Tags): TagStyle[] {
  return [
    { tag: [t.keyword, t.operatorKeyword, t.modifier, t.controlKeyword, t.definitionKeyword], color: 'var(--violet-text)' },
    { tag: [t.string, t.special(t.string), t.regexp, t.inserted], color: 'var(--green-text)' },
    { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--amber-text)' },
    { tag: [t.propertyName, t.attributeName, t.labelName], color: 'var(--blue-text)' },
    { tag: [t.function(t.variableName), t.function(t.propertyName), t.tagName, t.typeName, t.className], color: 'var(--accent-text)' },
    { tag: [t.comment, t.lineComment, t.blockComment, t.meta], color: 'var(--text-3)', fontStyle: 'italic' },
    { tag: [t.heading], color: 'var(--accent-text)', fontWeight: '600' },
    { tag: [t.invalid, t.deleted], color: 'var(--red-text)' },
    { tag: t.link, color: 'var(--blue-text)', textDecoration: 'underline' },
    { tag: t.emphasis, fontStyle: 'italic' },
    { tag: t.strong, fontWeight: '600' },
  ];
}

/** spread into EditorView.theme({...}): the search / replace panel, matches and bracket marks in the app's tokens */
export const panelTheme: Record<string, Record<string, string>> = {
  '.cm-panels': { backgroundColor: 'var(--surface-2)', color: 'var(--text)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
  '.cm-search': { fontSize: '12.5px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px', padding: '6px 28px 6px 8px' },
  '.cm-search label': { fontSize: '12.5px', color: 'var(--text-2)', display: 'inline-flex', alignItems: 'center', gap: '4px' },
  '.cm-textfield': { backgroundColor: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border-strong)', borderRadius: 'var(--r-sm)', padding: '3px 6px', fontSize: '12.5px' },
  '.cm-textfield:focus': { outline: '2px solid var(--accent)', outlineOffset: '-1px' },
  '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border-strong)', borderRadius: 'var(--r-sm)', padding: '3px 8px', fontSize: '12px' },
  '.cm-button:hover': { backgroundColor: 'var(--surface-hover)' },
  '.cm-panel.cm-search [name=close]': { color: 'var(--text-3)', fontSize: '16px' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--amber) 30%, transparent)', outline: '1px solid color-mix(in srgb, var(--amber) 60%, transparent)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--amber) 55%, transparent)' },
  '.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--accent) 18%, transparent)' },
  '&.cm-focused .cm-matchingBracket': { backgroundColor: 'color-mix(in srgb, var(--accent) 25%, transparent)', outline: '1px solid color-mix(in srgb, var(--accent) 50%, transparent)' },
  '&.cm-focused .cm-nonmatchingBracket': { backgroundColor: 'color-mix(in srgb, var(--red) 25%, transparent)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--surface-2)', border: '1px solid var(--border)', color: 'var(--text-2)' },
};
